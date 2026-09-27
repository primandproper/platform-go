package series

import (
	"time"
	"unicode/utf8"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Series is a stored rule, and how far it has been written out.
type Series struct {
	// CreatedAt is when the rule was stored, from the database's clock.
	CreatedAt time.Time `json:"createdAt"`
	// MaterializedUntil is how far the rule has been written out: every
	// occurrence it implies before this instant has a row.
	MaterializedUntil time.Time `json:"materializedUntil"`
	// LastUpdatedAt is when the row last changed — an end, or a pass writing
	// it forward — from the database's clock.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt,omitempty"`
	// ExhaustedAt is when a pass found nothing left to write, because the rule
	// ended before the horizon reached it. An exhausted series is one the
	// horizon worker never reads again.
	ExhaustedAt *time.Time `json:"exhaustedAt,omitempty"`
	// ID is the row's identifier, minted by the store.
	ID string `json:"id"`
	// Scope is the tenant the rule belongs to.
	Scope tenancy.Scope `json:"scope"`
	// Rule is the recurrence itself.
	Rule Rule `json:"rule"`
}

// State is whether an occurrence happens, and it is only that. What happened
// when it did — attended, a no-show, cancelled late — is the consumer's, kept
// in the consumer's own table keyed by occurrence id.
type State string

const (
	// StateScheduled is an occurrence at the instant it was written with: the
	// rule's slot, or the instant a replacement was added at.
	StateScheduled State = "scheduled"
	// StateSkipped is an occurrence that does not happen. The row stays, so a
	// consumer's rows keyed by its id stay attached to something, and so a
	// replacement has something to be added against.
	StateSkipped State = "skipped"
	// StateMoved is an occurrence that happens at an instant other than the one
	// it was written with. It keeps its id, so a consumer's attendees, price and
	// cover teacher move with it.
	StateMoved State = "moved"
)

// Occurrence is one instance of a series.
type Occurrence struct {
	// CreatedAt is when the row was written, from the database's clock.
	CreatedAt time.Time `json:"createdAt"`
	// ScheduledAt is when the occurrence happens.
	ScheduledAt time.Time `json:"scheduledAt"`
	// SlotAt is the instant the rule put the occurrence at, which a move does
	// not change. Nil for a replacement, which no rule implies.
	SlotAt *time.Time `json:"slotAt,omitempty"`
	// LastUpdatedAt is when the row last changed, from the database's clock.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt,omitempty"`
	// ID is the row's identifier, minted by the store. It is the key a
	// consumer's own table hangs its rows from.
	ID string `json:"id"`
	// SeriesID is the series the occurrence belongs to. A replacement belongs
	// to the series of the occurrence it replaced.
	SeriesID string `json:"seriesID"`
	// State is scheduled, skipped or moved.
	State State `json:"state"`
	// ReplacedBy names the occurrence added in a skipped one's place. Empty
	// where there is none.
	ReplacedBy string `json:"replacedBy,omitempty"`
	// Reason is the consumer's free text for the last skip or move, or for the
	// closure that skipped it.
	Reason string `json:"reason,omitempty"`
	// Scope is the tenant the occurrence belongs to.
	Scope tenancy.Scope `json:"scope"`
}

// Window is a span of time, [From, To): an occurrence at From is in it and one
// at To is not, so the windows of consecutive weeks tile without overlap.
type Window struct {
	From time.Time
	To   time.Time
}

// The widths of the columns this package stores a caller's values in, and the
// bounds a write is checked against before one is sent.
//
// They are checked in Go because the three dialects disagree about a value that
// does not fit: MySQL sizes these columns and refuses the row under strict mode,
// Postgres and SQLite store the whole thing — and the materializing insert's
// IGNORE would have MySQL truncate it and report success.
const (
	// MaxReasonLength bounds a skip's, a move's or a closure's reason, in
	// characters.
	MaxReasonLength = 1024
	// MaxOccurrencesPerRead is the most occurrences one window read answers
	// with. A window holding more is refused with ErrWindowTooLarge rather
	// than answered with the first page of it, because a week view drawn from
	// part of a week is wrong without saying so.
	MaxOccurrencesPerRead = 1000
	// MaxSeriesPerPage is the largest page ListSeries reads.
	MaxSeriesPerPage = 250
	// MaxWriteAhead is the furthest past now a write may reach: the through
	// Materialize writes before, a SkipWindow's end, and the horizon worker's
	// Horizon. Two years holds any closure a studio plans and any horizon a
	// schedule shows, and a bound is what keeps a typo'd year from becoming
	// one insert per week for every week in it, on one transaction. A write
	// past it is ErrTooFarAhead.
	MaxWriteAhead = 104 * 7 * 24 * time.Hour
	// MaxWriteBehind is the furthest before now a write may reach: a series'
	// start date, from which Materialize writes, and a SkipWindow's start. A
	// year holds any series a consumer is backfilling from last term, and a
	// bound is what keeps a typo'd year from becoming one insert per week for
	// every week since it — or a closure from skipping every lesson ever held.
	// A write before it is ErrTooFarBack.
	MaxWriteBehind = 365 * 24 * time.Hour

	// sweepBatch is how many occurrences one pass of a closure or an end
	// skips. The store repeats the pass on the caller's transaction until one
	// comes back short.
	sweepBatch = 500
)

// validateReason checks a caller's reason against its column.
func validateReason(reason string) error {
	if n := utf8.RuneCountInString(reason); n > MaxReasonLength {
		return platformerrors.Wrapf(ErrValueTooLong, "reason is %d characters, over %d", n, MaxReasonLength)
	}

	return nil
}

// bounds renders a window as the two arguments the statements take:
// (after, through].
//
// Every instant the store writes is whole seconds, so over what it stores
// [From, To) and (ceil(From) − 1s, ceil(To) − 1s] are the same set, and the
// second is what querygen's comparands can spell. See the window in
// internal/queries.
func (w Window) bounds() (after, through time.Time, err error) {
	if w.From.IsZero() || w.To.IsZero() || !w.From.Before(w.To) {
		return time.Time{}, time.Time{}, platformerrors.Wrapf(ErrInvalidWindow, "[%s, %s)", w.From, w.To)
	}

	return ceilSecond(w.From).Add(-time.Second), ceilSecond(w.To).Add(-time.Second), nil
}

// withinWriteAhead refuses an end further past now than MaxWriteAhead.
func withinWriteAhead(end, now time.Time) error {
	if limit := now.Add(MaxWriteAhead); end.After(limit) {
		return platformerrors.Wrapf(ErrTooFarAhead, "%s is past %s", end.UTC(), limit.UTC())
	}

	return nil
}

// withinWriteBehind refuses a start further before now than MaxWriteBehind.
func withinWriteBehind(start, now time.Time) error {
	if limit := now.Add(-MaxWriteBehind); start.Before(limit) {
		return platformerrors.Wrapf(ErrTooFarBack, "%s is before %s", start.UTC(), limit.UTC())
	}

	return nil
}

// ceilSecond rounds t up to the next whole second, in UTC.
func ceilSecond(t time.Time) time.Time {
	floor := t.UTC().Truncate(time.Second)
	if floor.Before(t) {
		return floor.Add(time.Second)
	}

	return floor
}

// stored is t as the store writes it: UTC, and whole seconds, because SQLite
// keeps no more than that and a value that read back differently on one
// dialect would be three stores rather than one.
func stored(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}

package metering

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Period names the window usage accumulates in.
//
// It is the bucket the durable total is keyed by, so it is a property of the
// meter rather than of a call: changing it changes what every stored total means,
// and there is no migration from one bucketing to another that does not involve
// re-reading the event ledger.
type Period string

const (
	// PeriodDay buckets by UTC calendar day.
	PeriodDay Period = "day"

	// PeriodMonth buckets by UTC calendar month.
	//
	// Note what it is not: it is not the subject's billing period. A calendar
	// month is the same window for everybody, which makes it the right choice for
	// an internal quota and the wrong one for an invoice line, because an invoice
	// covers the subscription's own cycle.
	PeriodMonth Period = "month"

	// PeriodBillingPeriod buckets by the subject's own subscription cycle, and
	// requires a PeriodResolver that can answer it — see
	// ErrNoBillingPeriodResolver.
	//
	// It is the period that makes a total safe to invoice, because it is the only
	// one whose boundaries are the boundaries the provider will draw.
	PeriodBillingPeriod Period = "billing_period"
)

// Valid reports whether p is one of this package's periods.
func (p Period) Valid() bool {
	switch p {
	case PeriodDay, PeriodMonth, PeriodBillingPeriod:
		return true
	default:
		return false
	}
}

// Bounds is a half-open window: usage at Start is in it, usage at End is in the
// next one.
//
// Half-open rather than inclusive because the alternative has no correct answer
// at the boundary. With inclusive ends, an event at exactly midnight belongs to
// two periods, and whichever of the two a query picks, some other query picks the
// other.
type Bounds struct {
	Start time.Time
	End   time.Time
}

// Contains reports whether an instant falls in the window.
func (b Bounds) Contains(t time.Time) bool {
	return !t.Before(b.Start) && t.Before(b.End)
}

// Valid reports whether the window is non-empty and correctly ordered.
func (b Bounds) Valid() bool {
	return !b.Start.IsZero() && b.End.After(b.Start)
}

// PeriodResolver maps an instant to the window it falls in for a subject.
//
// It takes a subject because PeriodBillingPeriod's answer differs per subject —
// two customers who signed up on different days have different cycle boundaries
// for the same instant — and because the calendar periods, which do not, are
// cheap enough not to warrant a second interface.
//
// An implementation must be deterministic for a given (subject, period, instant).
// The bounds it returns become part of the durable total's primary key, so a
// resolver that answered differently on two calls would split one period's usage
// across two rows and invoice both.
type PeriodResolver interface {
	Resolve(ctx context.Context, subject string, p Period, at time.Time) (Bounds, error)
}

// PeriodResolverFunc adapts a function to PeriodResolver.
type PeriodResolverFunc func(ctx context.Context, subject string, p Period, at time.Time) (Bounds, error)

// Resolve implements PeriodResolver.
func (f PeriodResolverFunc) Resolve(ctx context.Context, subject string, p Period, at time.Time) (Bounds, error) {
	return f(ctx, subject, p, at)
}

var _ PeriodResolver = (*CalendarResolver)(nil)

// CalendarResolver resolves the calendar periods in UTC and refuses the billing
// period. It is exported, and returned by NewCalendarPeriodResolver, so a caller
// can depend on the resolver it built rather than on the PeriodResolver seam.
type CalendarResolver struct {
	billing PeriodResolver
}

// NewCalendarPeriodResolver returns the default resolver: UTC calendar days and
// months, and PeriodBillingPeriod delegated to billing.
//
// A nil billing resolver leaves PeriodBillingPeriod unanswerable, which is the
// intended default. There is no plausible library-side guess at when a
// subscription cycle starts, and the failure mode of guessing — usage filed under
// the wrong invoice — is silent, arrives a month later, and looks like a pricing
// bug rather than a metering one.
//
// UTC rather than a configurable zone, and deliberately. A period boundary that
// moves twice a year is a period that is 23 hours long once and 25 hours long
// once, and a daily quota that is 25 hours long is a quota somebody will notice
// on exactly one day and never reproduce. An application that must bill on local
// midnights supplies its own resolver and owns that decision.
func NewCalendarPeriodResolver(billing PeriodResolver) *CalendarResolver {
	return &CalendarResolver{billing: billing}
}

// Resolve implements PeriodResolver.
func (r *CalendarResolver) Resolve(ctx context.Context, subject string, p Period, at time.Time) (Bounds, error) {
	utc := at.UTC()

	switch p {
	case PeriodDay:
		start := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)

		return Bounds{Start: start, End: start.AddDate(0, 0, 1)}, nil
	case PeriodMonth:
		start := time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)

		return Bounds{Start: start, End: start.AddDate(0, 1, 0)}, nil
	case PeriodBillingPeriod:
		if r.billing == nil {
			return Bounds{}, platformerrors.Wrapf(ErrNoBillingPeriodResolver, "subject %q", subject)
		}

		bounds, err := r.billing.Resolve(ctx, subject, p, at)
		if err != nil {
			return Bounds{}, err
		}

		// Vetted rather than trusted. These bounds become the primary key of a
		// durable total and the window an invoice is drawn against, and a
		// resolver that returns a zero or inverted window would key every period
		// to the same row — which reads as one enormous month of usage.
		if !bounds.Valid() {
			return Bounds{}, platformerrors.Newf(
				"metering billing period resolver returned invalid bounds %s..%s for subject %q",
				bounds.Start, bounds.End, subject,
			)
		}

		return Bounds{Start: bounds.Start.UTC(), End: bounds.End.UTC()}, nil
	default:
		return Bounds{}, platformerrors.Wrapf(ErrUnknownPeriod, "period %q", p)
	}
}

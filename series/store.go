package series

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Store is the series and occurrence tables: standing rules, the instances they
// imply, and the exceptions those instances accumulate.
//
// Every consumer write takes a database.Tx and every consumer read takes the
// wider database.SQLQueryExecutor, which is the module's store convention. A
// skip is rarely the only row a consumer writes — "cancel one lesson" is a skip
// here and an outcome in the consumer's own table, and the two are one fact —
// so a store that opened its own transaction would be one whose companion
// landed in a second one. A caller with nothing to join opens one with
// Client.WithTransaction.
//
// Every consumer method is scoped and there is no unscoped variant. An
// application with a single tenant passes tenancy.Global() everywhere. The one
// method that names no scope is DueSeries, which is the horizon worker's and
// says so.
//
// # Five commands, one audience each
//
// Skipping one occurrence, closing a date range, ending a series, moving one
// occurrence and adding a replacement against a skipped one are five methods
// rather than one "cancel" with a mode, because they are five different things
// a person meant, and a consumer that offers one button for all five has its
// software guessing which. Each is one guarded write the consumer can audit as
// the one action it was.
type Store interface {
	// CreateSeries stores a rule, through the caller's transaction, and
	// answers with the row it wrote. It writes no occurrence: Materialize does,
	// on the same transaction if the caller wants the rows at once, and the
	// horizon worker does otherwise.
	//
	// The rule is validated first — see ErrInvalidRule, ErrUnknownTimeZone —
	// and is not modified. A nil tx is an error wrapping ErrNilExecutor.
	CreateSeries(ctx context.Context, tx database.Tx, scope tenancy.Scope, rule *Rule) (*Series, error)

	// GetSeries reads one live series by id. One in another scope reads as
	// ErrSeriesNotFound. A nil q is an error wrapping ErrNilExecutor.
	GetSeries(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, id string) (*Series, error)

	// ListSeries reads up to limit of a scope's series in id order, after the
	// id afterID names; the empty string starts at the beginning. A page
	// shorter than limit is the last one. A limit outside 1 to
	// MaxSeriesPerPage is ErrInvalidPageSize. A nil q is an error wrapping
	// ErrNilExecutor.
	ListSeries(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, afterID string, limit int) ([]*Series, error)

	// EndSeries ends a series from a date, through the caller's transaction:
	// from is the first date with no occurrence, and every occurrence the rule
	// put on or after it that is not already skipped is skipped with reason.
	// It answers with the series as it now stands.
	//
	// It keys on where the rule put an occurrence rather than where it now
	// happens, because what an end removes is the rule's instances from a
	// date on: one moved out of that range goes, one moved into it from
	// before stays, and a replacement — which no rule implies — is never
	// touched. An end only brings the end nearer; one on or after the date the
	// series already ends on is ErrSeriesEnded. An end on or before the start
	// is a series with no occurrences at all.
	//
	// A series in another scope or absent is ErrSeriesNotFound. A nil tx is an
	// error wrapping ErrNilExecutor.
	EndSeries(ctx context.Context, tx database.Tx, scope tenancy.Scope, id string, from Date, reason string) (*Series, error)

	// Materialize writes the occurrences a series implies before through,
	// through the caller's transaction, from wherever the series was last
	// written to, and answers with how many rows it wrote.
	//
	// It is idempotent. A slot that already has a row — written by another
	// pass, or skipped, or moved away — is left alone, so two materializers
	// racing over one series leave one row per slot and the counts they report
	// add up to it. A through at or before what is already written writes
	// nothing and is not an error. A series written to its end is stamped
	// exhausted and leaves the horizon worker's list.
	//
	// It writes from where the series was last written to, not from now: a
	// series whose start date is in the past is written from its start. That
	// is the rows the rule implies, and a consumer who did not want the past
	// ones starts the series today.
	//
	// A series in another scope or absent is ErrSeriesNotFound. A nil tx is an
	// error wrapping ErrNilExecutor.
	Materialize(ctx context.Context, tx database.Tx, scope tenancy.Scope, seriesID string, through time.Time) (int64, error)

	// GetOccurrence reads one live occurrence by id. One in another scope
	// reads as ErrOccurrenceNotFound. A nil q is an error wrapping
	// ErrNilExecutor.
	GetOccurrence(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, id string) (*Occurrence, error)

	// ListOccurrences reads a scope's occurrences happening in a window, every
	// state included, in the order they happen. It is the week view.
	//
	// It reads what has been written. A window past the horizon a series has
	// been materialized to has none of that series' occurrences in it until
	// the worker — or a caller's Materialize — writes them. A window holding
	// more than MaxOccurrencesPerRead is ErrWindowTooLarge, and one whose end
	// is not after its start is ErrInvalidWindow. A nil q is an error wrapping
	// ErrNilExecutor.
	ListOccurrences(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, window Window) ([]*Occurrence, error)

	// ListSeriesOccurrences is ListOccurrences narrowed to one series. A
	// series that does not exist has no occurrences, and is not an error.
	ListSeriesOccurrences(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		seriesID string,
		window Window,
	) ([]*Occurrence, error)

	// SkipOccurrence marks one occurrence skipped with the consumer's reason,
	// through the caller's transaction, and answers with the row it left. The
	// row stays, so the consumer's rows keyed by its id stay attached, and a
	// replacement can be added against it.
	//
	// One already skipped is ErrOccurrenceSkipped. One in another scope or
	// absent is ErrOccurrenceNotFound. A nil tx is an error wrapping
	// ErrNilExecutor.
	SkipOccurrence(ctx context.Context, tx database.Tx, scope tenancy.Scope, id, reason string) (*Occurrence, error)

	// SkipWindow is a closure: every occurrence of every series in the scope
	// happening in the window, skipped with reason, through the caller's
	// transaction. It answers with how many it skipped.
	//
	// It writes every series in the scope out to the window's end before it
	// skips, so a closure three months away skips the occurrences the rules
	// imply there rather than only the ones the horizon worker has reached —
	// and the worker, finding those slots written, leaves them skipped. A
	// series created after the closure is not closed by it: a closure is an
	// action over the series that exist when it is taken, and a studio that
	// adds a student during its holiday has decided something about that
	// student.
	//
	// Occurrences already skipped are left as they are, reason included.
	// Replacements in the window are skipped like any other occurrence. A nil
	// tx is an error wrapping ErrNilExecutor.
	SkipWindow(ctx context.Context, tx database.Tx, scope tenancy.Scope, window Window, reason string) (int64, error)

	// MoveOccurrence gives one occurrence a new instant and marks it moved,
	// through the caller's transaction, and answers with the row it left. It
	// keeps its id, so the consumer's rows move with it, and the rule's slot
	// stays occupied by it, so the horizon worker does not write the slot
	// again.
	//
	// The instant is stored in whole seconds, rounded down. One already skipped
	// is ErrOccurrenceSkipped. One in another scope or absent is
	// ErrOccurrenceNotFound. A nil tx is an error wrapping ErrNilExecutor.
	MoveOccurrence(ctx context.Context, tx database.Tx, scope tenancy.Scope, id string, to time.Time, reason string) (*Occurrence, error)

	// AddReplacement adds an occurrence at an instant in place of a skipped
	// one, through the caller's transaction, and answers with the new row. The
	// replacement belongs to the skipped occurrence's series, has no slot, and
	// is scheduled; the skipped one's ReplacedBy names it.
	//
	// One not skipped is ErrOccurrenceNotSkipped; one that already has a
	// replacement is ErrOccurrenceReplaced. One in another scope or absent is
	// ErrOccurrenceNotFound. A nil tx is an error wrapping ErrNilExecutor.
	AddReplacement(ctx context.Context, tx database.Tx, scope tenancy.Scope, skippedID string, at time.Time) (*Occurrence, error)

	// DueSeries reads up to limit live, unexhausted series written out no
	// further than horizon, across every scope, least far written first.
	//
	// It is the horizon worker's, and it is the one read here with no scope:
	// it is the component servicing itself rather than answering a consumer,
	// and each Series it returns carries the scope the Materialize that
	// follows binds. A consumer has no reason to call it. A nil q is an error
	// wrapping ErrNilExecutor.
	DueSeries(ctx context.Context, q database.SQLQueryExecutor, horizon time.Time, limit int) ([]*Series, error)
}

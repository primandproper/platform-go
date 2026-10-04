package issuereports

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Hooks is what a consumer hangs off the store's writes: one method per write,
// each called inside the transaction that write ran in, once its statements have
// landed.
//
// The transaction is the point, as it is for identity.Hooks. A report's ordinary
// companions — an audit entry naming who filed or decided it, a data change
// event on an outbox — are the same fact as the row, so a hook receives the
// database.Tx and writes on it, and returning an error fails the write: the
// store answers with that error and no row, and the caller's transaction rolls
// back with it.
//
// That is the whole of what a hook adds over a caller writing the companions
// beside each store call itself, and it is worth having because of what the
// caller would otherwise have to hold. A revision's entry wants what the
// revision changed, and a decision's wants the status and the note the report
// was moved away from, and neither is readable once the write has run. A
// consumer wrapping the Store to record those writes ends up reimplementing
// every method of it to call through, and reading rows the store is about to
// read anyway. A hook is handed them.
//
// What it costs is what identity.Hooks says it costs: a hook runs with a write
// transaction open. Work that is slow, talks to a network, or can fail for
// reasons the write should survive belongs behind an outbox row the hook
// enqueues, not in the hook. Mailing the reporter from AfterTransitionReport
// makes the resolution fail when the mail provider is down.
//
// An update is handed two rows, the one before it and the one after, because
// what an update means is the difference between them and that is not readable
// once the write has run. A transition counts: the status it left is the one
// the caller named, but the note and the closing stamp it replaced are not
// named anywhere. The before row costs the write one keyed read on the
// transaction, which the store makes only when the hooks are not NoopHooks.
//
// Every method is "After", and none is a veto. Whether somebody may file,
// revise, decide or archive a report is decided before the store is called; a
// hook returning an error is an abort of a decision already taken.
//
// The rows a hook is handed are the rows the write answers its caller with, and
// they are the same values: a hook that modifies one modifies what the caller
// receives. Do not.
//
// It is one interface rather than a function type per write so that a
// consumer's recording layer is one type. Embed NoopHooks and override the
// methods that matter; an operation added here later then arrives as a no-op
// rather than a compile failure.
type Hooks interface {
	// AfterCreateReport is called with the report CreateReport filed, carrying
	// the id it was minted under, StatusOpen, and the creation time the
	// database stamped.
	AfterCreateReport(ctx context.Context, tx database.Tx, scope tenancy.Scope, report *Report) error

	// AfterUpdateReport is called with the report as it stood before
	// UpdateReport and as UpdateReport left it, both read on the transaction —
	// so a hook can say what the reporter changed, which the row alone cannot.
	// audit.Diff takes the pair.
	AfterUpdateReport(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Report) error

	// AfterTransitionReport is called with the report as it stood before
	// TransitionReport and as TransitionReport left it, both read on the
	// transaction. The before row's status is the from the caller named — the
	// guard saw to that — and its resolution and ClosedAt are the ones the move
	// replaced or cleared, which nothing else hands a caller.
	AfterTransitionReport(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Report) error

	// AfterArchiveReport is called with the report ArchiveReport hid, **as the
	// archive left it** — ArchivedAt set, everything else as it was — read back
	// on the transaction through the statement that still sees archived rows.
	// It is the same row ArchiveReport answers its caller with.
	AfterArchiveReport(ctx context.Context, tx database.Tx, scope tenancy.Scope, report *Report) error

	// AfterDeleteReportsByReporter is called with the reporter an erasure
	// named and how many of their reports it destroyed. It is called when the
	// count is zero, too: an erasure that found nothing to erase still ran, and
	// a consumer recording erasures records that one.
	//
	// It is handed a count and not the rows, for the reason the write itself
	// answers with a count — the rows are free text the reporter wrote, and the
	// erasure exists to remove it.
	AfterDeleteReportsByReporter(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		reporter string,
		deleted int64,
	) error
}

// NoopHooks does nothing. It is what a caller passes, by name, when it commits
// nothing alongside these writes — a seed import, a bootstrap tool, a test — and
// it is still the type to embed in a Hooks that overrides some:
//
//	type recordingHooks struct {
//		issuereports.NoopHooks
//
//		audit audit.Recorder
//	}
//
// Embedding it rather than implementing every method is what makes a method
// added to Hooks later additive: an embedder gains a no-op rather than a compile
// failure. A consumer implementing the interface outright — which the generated
// HooksMock invites — is the consumer the next method breaks. That can
// be the point: a consumer that records every write may prefer a new one to
// fail to compile until somebody decides what it records.
type NoopHooks struct{}

var _ Hooks = NoopHooks{}

// AfterCreateReport implements Hooks.
func (NoopHooks) AfterCreateReport(context.Context, database.Tx, tenancy.Scope, *Report) error {
	return nil
}

// AfterUpdateReport implements Hooks.
func (NoopHooks) AfterUpdateReport(context.Context, database.Tx, tenancy.Scope, *Report, *Report) error {
	return nil
}

// AfterTransitionReport implements Hooks.
func (NoopHooks) AfterTransitionReport(context.Context, database.Tx, tenancy.Scope, *Report, *Report) error {
	return nil
}

// AfterArchiveReport implements Hooks.
func (NoopHooks) AfterArchiveReport(context.Context, database.Tx, tenancy.Scope, *Report) error {
	return nil
}

// AfterDeleteReportsByReporter implements Hooks.
func (NoopHooks) AfterDeleteReportsByReporter(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}

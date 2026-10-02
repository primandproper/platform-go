package waitlists

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Hooks is what a consumer hangs off the store's writes: one method per write,
// each called inside the transaction that write ran in, once its statements have
// landed.
//
// The transaction is the point, as it is for identity.Hooks. A waitlist write's
// ordinary companions — an audit entry naming who did it, a data change event on
// an outbox — are the same fact as the row, so a hook receives the database.Tx
// and writes on it, and returning an error fails the write: the store answers
// with that error and no row, and the caller's transaction rolls back with it.
//
// That is the whole of what a hook adds over a caller writing the companions
// beside each store call itself, and it is worth having because of what the
// caller would otherwise have to hold. Withdraw blanks the contact, the notes and
// the subject, so a companion written after it needs the row from before; a
// transition's companion wants the status the guard moved the row from. A
// consumer wrapping the Store to record those writes ends up reimplementing
// every method of it to call through, and reading rows the store had already
// read. A hook is handed them.
//
// What it costs is what identity.Hooks says it costs: a hook runs with a write
// transaction open. Work that is slow, talks to a network, or can fail for
// reasons the write should survive belongs behind an outbox row the hook
// enqueues, not in the hook. Mailing an invitation from AfterInvite makes the
// invitation fail when the mail provider is down.
//
// Every method is "After", and none is a veto. Whether somebody may join, be
// invited or be withdrawn is decided before the store is called; a hook
// returning an error is an abort of a decision already taken.
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
	// AfterCreateList is called with the list CreateList wrote, carrying the id
	// it was minted under and the creation time the database stamped.
	AfterCreateList(ctx context.Context, tx database.Tx, scope tenancy.Scope, list *List) error

	// AfterUpdateList is called with the list UpdateList left, read back on the
	// transaction after the write.
	AfterUpdateList(ctx context.Context, tx database.Tx, scope tenancy.Scope, list *List) error

	// AfterArchiveList is called with the list ArchiveList hid, read back on the
	// transaction through the statement that still sees archived rows.
	AfterArchiveList(ctx context.Context, tx database.Tx, scope tenancy.Scope, list *List) error

	// AfterJoin is called with the signup Join wrote, in the status it was
	// written at — StatusPending or StatusWaiting.
	AfterJoin(ctx context.Context, tx database.Tx, scope tenancy.Scope, signup *Signup) error

	// AfterUpdateSignupNotes is called with the signup UpdateSignupNotes left,
	// read back after the write.
	AfterUpdateSignupNotes(ctx context.Context, tx database.Tx, scope tenancy.Scope, signup *Signup) error

	// AfterConfirm is called with the signup Confirm moved from StatusPending to
	// StatusWaiting, carrying the StatusChangedAt the move stamped.
	AfterConfirm(ctx context.Context, tx database.Tx, scope tenancy.Scope, signup *Signup) error

	// AfterInvite is called with the signup Invite moved from StatusWaiting to
	// StatusInvited, carrying the contact to write to and the StatusChangedAt
	// the move stamped. Queue the invitation from here; do not send it.
	AfterInvite(ctx context.Context, tx database.Tx, scope tenancy.Scope, signup *Signup) error

	// AfterConvert is called with the signup Convert moved from StatusInvited to
	// StatusConverted.
	AfterConvert(ctx context.Context, tx database.Tx, scope tenancy.Scope, signup *Signup) error

	// AfterWithdraw is called with the signup Withdraw took off the list, **as it
	// stood before the withdrawal** — contact, notes, subject and status all as
	// they were — which is the same row Withdraw answers its caller with.
	//
	// It is the one hook whose row is not the row the table now holds, and that
	// is deliberate: the table now holds a status and a digest, and a record of
	// who came off which list written from that would be a record of nobody. The
	// status on it is the one the signup was withdrawn from; the one it is in
	// now is StatusWithdrawn, which is what the hook being called says.
	AfterWithdraw(ctx context.Context, tx database.Tx, scope tenancy.Scope, signup *Signup) error

	// AfterWithdrawSignupsForSubject is called with the subject an erasure
	// withdrew and how many of their signups that was. It is called when the
	// count is zero, too: an erasure that found nothing to erase still ran, and
	// a consumer recording erasures records that one.
	//
	// It is handed a count and not the rows, for the reason the write itself
	// answers with a count — the rows are the subject's own data, and the
	// erasure exists to remove it.
	AfterWithdrawSignupsForSubject(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		subject Subject,
		withdrawn int64,
	) error

	// AfterArchiveSignup is called with the signup ArchiveSignup hid, read back
	// on the transaction through the statement that still sees archived rows.
	AfterArchiveSignup(ctx context.Context, tx database.Tx, scope tenancy.Scope, signup *Signup) error
}

// NoopHooks does nothing, and is what a store built without WithHooks runs. It is
// also the type to embed in a Hooks that overrides some:
//
//	type recordingHooks struct {
//		waitlists.NoopHooks
//
//		audit audit.Recorder
//	}
//
// Embedding it rather than implementing every method is what makes a method
// added to Hooks later additive: an embedder gains a no-op rather than a compile
// failure. A consumer implementing the interface outright — which the generated
// HooksMock invites — is the consumer the next method breaks.
type NoopHooks struct{}

var _ Hooks = NoopHooks{}

// AfterCreateList implements Hooks.
func (NoopHooks) AfterCreateList(context.Context, database.Tx, tenancy.Scope, *List) error {
	return nil
}

// AfterUpdateList implements Hooks.
func (NoopHooks) AfterUpdateList(context.Context, database.Tx, tenancy.Scope, *List) error {
	return nil
}

// AfterArchiveList implements Hooks.
func (NoopHooks) AfterArchiveList(context.Context, database.Tx, tenancy.Scope, *List) error {
	return nil
}

// AfterJoin implements Hooks.
func (NoopHooks) AfterJoin(context.Context, database.Tx, tenancy.Scope, *Signup) error {
	return nil
}

// AfterUpdateSignupNotes implements Hooks.
func (NoopHooks) AfterUpdateSignupNotes(context.Context, database.Tx, tenancy.Scope, *Signup) error {
	return nil
}

// AfterConfirm implements Hooks.
func (NoopHooks) AfterConfirm(context.Context, database.Tx, tenancy.Scope, *Signup) error {
	return nil
}

// AfterInvite implements Hooks.
func (NoopHooks) AfterInvite(context.Context, database.Tx, tenancy.Scope, *Signup) error {
	return nil
}

// AfterConvert implements Hooks.
func (NoopHooks) AfterConvert(context.Context, database.Tx, tenancy.Scope, *Signup) error {
	return nil
}

// AfterWithdraw implements Hooks.
func (NoopHooks) AfterWithdraw(context.Context, database.Tx, tenancy.Scope, *Signup) error {
	return nil
}

// AfterWithdrawSignupsForSubject implements Hooks.
func (NoopHooks) AfterWithdrawSignupsForSubject(context.Context, database.Tx, tenancy.Scope, Subject, int64) error {
	return nil
}

// AfterArchiveSignup implements Hooks.
func (NoopHooks) AfterArchiveSignup(context.Context, database.Tx, tenancy.Scope, *Signup) error {
	return nil
}

package comments

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Hooks is what a consumer hangs off the store's writes: one method per write,
// each called inside the transaction that write ran in, once its statements have
// landed.
//
// The transaction is the point, as it is for identity.Hooks. A comment write's
// ordinary companions — an audit entry naming who said it, a data change event on
// an outbox — are the same fact as the row, so a hook receives the database.Tx
// and writes on it, and returning an error fails the write: the store answers
// with that error and no row, and the caller's transaction rolls back with it.
//
// That is the whole of what a hook adds over a caller writing the companions
// beside each store call itself, and it is worth having because of what the
// caller would otherwise have to hold. An edit's companion wants what the body
// said before the edit, which no read can reach once the statement has run; an
// archive's wants the row the archive hid from every keyed read. A consumer
// wrapping the Store to record those writes ends up reimplementing every method
// of it to call through, and reading rows the store had already read. A hook is
// handed them.
//
// What it costs is what identity.Hooks says it costs: a hook runs with a write
// transaction open. Work that is slow, talks to a network, or can fail for
// reasons the write should survive belongs behind an outbox row the hook
// enqueues, not in the hook. Notifying the author of a root from AfterCreateComment
// makes every reply fail when the mail provider is down.
//
// An update is handed two rows, the one before it and the one after, because
// what an update means is the difference between them and that is not readable
// once the write has run. The before row costs the update one keyed read on the
// transaction, which the store makes only when hooks are installed.
//
// Every method is "After", and none is a veto. Whether somebody may comment,
// edit or remove a comment is decided before the store is called; a hook
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
	// AfterCreateComment is called with the comment CreateComment wrote, carrying
	// the id it was minted under, the target a reply adopted from its parent and
	// the creation time the database stamped.
	AfterCreateComment(ctx context.Context, tx database.Tx, scope tenancy.Scope, comment *Comment) error

	// AfterUpdateComment is called with the comment as it stood before
	// UpdateComment and as UpdateComment left it, both read on the transaction —
	// so a hook can say what the author changed, which the row alone cannot.
	// audit.Diff takes the pair.
	AfterUpdateComment(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Comment) error

	// AfterArchiveComment is called with the comment ArchiveComment hid, **as the
	// archive left it** — ArchivedAt set — read back on the transaction through
	// the statement that still sees archived rows. It is the row ArchiveComment
	// answers its caller with, and the body on it is the body that was removed.
	AfterArchiveComment(ctx context.Context, tx database.Tx, scope tenancy.Scope, comment *Comment) error

	// AfterDeleteCommentsForTarget is called with the target a sweep cleared and
	// how many comments that destroyed — the count the DELETE itself reported,
	// so it is every row the statement removed rather than a page of them read
	// beforehand. It is called when the count is zero, too: a target nobody
	// commented on was still swept.
	//
	// It is handed a count and not the rows or their ids, for the reason the
	// write itself answers with a count. The rows are free text the sweep exists
	// to remove; and a list of ids would have to be read before the DELETE, which
	// on any isolation level short of serializable can miss a row a concurrent
	// writer commits between the two statements and the DELETE then removes.
	AfterDeleteCommentsForTarget(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		target Target,
		deleted int64,
	) error

	// AfterDeleteCommentsByAuthor is called with the author an erasure cleared
	// and how many of their comments that was. It is called when the count is
	// zero, too: an erasure that found nothing to erase still ran, and a consumer
	// recording erasures records that one.
	//
	// It is handed a count and not the rows, for the reason the write itself
	// answers with a count — the rows are the subject's own words, and the
	// erasure exists to remove them.
	AfterDeleteCommentsByAuthor(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		author string,
		deleted int64,
	) error
}

// NoopHooks does nothing, and is what a store built without WithHooks runs. It is
// also the type to embed in a Hooks that overrides some:
//
//	type recordingHooks struct {
//		comments.NoopHooks
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

// AfterCreateComment implements Hooks.
func (NoopHooks) AfterCreateComment(context.Context, database.Tx, tenancy.Scope, *Comment) error {
	return nil
}

// AfterUpdateComment implements Hooks.
func (NoopHooks) AfterUpdateComment(context.Context, database.Tx, tenancy.Scope, *Comment, *Comment) error {
	return nil
}

// AfterArchiveComment implements Hooks.
func (NoopHooks) AfterArchiveComment(context.Context, database.Tx, tenancy.Scope, *Comment) error {
	return nil
}

// AfterDeleteCommentsForTarget implements Hooks.
func (NoopHooks) AfterDeleteCommentsForTarget(context.Context, database.Tx, tenancy.Scope, Target, int64) error {
	return nil
}

// AfterDeleteCommentsByAuthor implements Hooks.
func (NoopHooks) AfterDeleteCommentsByAuthor(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}

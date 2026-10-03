package webhooks

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Hooks is what a consumer hangs off the store's endpoint and subscription
// writes: one method per write, each called inside the transaction that write
// ran in, once its statements and its read-back have landed.
//
// The transaction is the point, as it is for waitlists.Hooks. An endpoint
// write's ordinary companions — an audit entry naming who registered or retired
// it, a data change event on an outbox — are the same fact as the row, so a
// hook receives the database.Tx and writes on it, and returning an error fails
// the write: the store answers with that error and no row, and the caller's
// transaction rolls back with it.
//
// That is the whole of what a hook adds over a caller writing the companions
// beside each store call itself, and it is worth having because of what the
// caller would otherwise have to hold. A save is an upsert, so what it changed
// is the difference between the row before it and the row after, and the row
// before is gone once it has run. A consumer wrapping the Store to record its
// writes ends up reimplementing each of them to call through, reading rows the
// store had already read. A hook is handed them.
//
// What it costs is what waitlists.Hooks says it costs: a hook runs with a write
// transaction open. Work that is slow, talks to a network, or can fail for
// reasons the write should survive belongs behind an outbox row the hook
// enqueues, not in the hook. Dispatching a webhook from a hook is exactly that
// — Dispatch writes rows on the transaction it is handed and sends nothing.
//
// A write that changes a row that already existed is handed two, the one
// before it and the one after, because what such a write means is the
// difference between them and that is not readable once the write has run. The
// before row costs the write one keyed read on the transaction, which the store
// makes only when hooks are installed.
//
// Only the consumer writes have hooks. Enqueue is not here, although it takes a
// Tx: its only caller is Dispatcher.Dispatch, and a consumer wanting a
// companion for a fan-out writes it beside the Dispatch call that caused it,
// which already holds every field of the delivery. Claim, MarkDelivered,
// RecordFailure, RecordAttempt, Requeue and Reap are not here either: they take
// no transaction, because they are the queue servicing itself on the store's
// own handle, and there is no caller transaction for a companion to commit
// with. The attempts table is the record of those.
//
// Every method is "After", and none is a veto. Whether somebody may register
// an endpoint or subscribe it to an event type is decided before the store is
// called; a hook returning an error is an abort of a decision already taken.
//
// The rows a hook is handed are the rows the write answers its caller with, and
// they are the same values: a hook that modifies one modifies what the caller
// receives. Do not. They carry the endpoint's Secret, as the write's answer
// does; a hook recording an endpoint reads it through the json tags, which
// leave the Secret out, or not at all.
//
// It is one interface rather than a function type per write so that a
// consumer's recording layer is one type. Embed NoopHooks and override the
// methods that matter; an operation added here later then arrives as a no-op
// rather than a compile failure.
type Hooks interface {
	// AfterSaveEndpoint is called with the endpoint as it stood before
	// SaveEndpoint and as SaveEndpoint left it, both read on the transaction
	// with their live subscriptions. before is nil when the save created the
	// row, which after.Created also says; on a re-registration it is the row the
	// upsert overwrote, so a hook can say what changed — audit.Diff takes the
	// pair.
	AfterSaveEndpoint(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Endpoint) error

	// AfterArchiveEndpoint is called with the endpoint ArchiveEndpoint answers
	// with: the row as the archive left it, read back through the statement that
	// still sees archived rows, and without its subscriptions.
	//
	// It is called for an endpoint that was already archived, too, whose
	// ArchivedAt then predates this call — that archive answers with the row, so
	// it calls the hook with it. It is not called where the identifier named
	// nothing in scope, because that archive answers with no row, and there is
	// nothing to hand.
	AfterArchiveEndpoint(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpoint *Endpoint) error

	// AfterRotateSecret is called with the identifier of the endpoint whose
	// signing key RotateSecret replaced, and nothing else. The row is the one
	// RotateSecret exists not to read back, and the keys are not a hook's to
	// hold: that the key changed, on which endpoint, is the whole of what a
	// rotation's companion can say.
	//
	// It is called for a rotation to the key already in force, which RotateSecret
	// answers as success.
	AfterRotateSecret(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpointID string) error

	// AfterAddSubscription is called with the subscription for the pair as it
	// stood before AddSubscription and as AddSubscription left it, both read on
	// the transaction. before is nil when the pair was new and the row was
	// created; otherwise it is the row the upsert found, which is an archived
	// row AddSubscription revived or a live one it left as it was. after is
	// always live.
	AfterAddSubscription(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Subscription) error

	// AfterArchiveSubscription is called with the subscription
	// ArchiveSubscription answers with: the row as the archive left it. Like
	// AfterArchiveEndpoint it is called for a row that was already archived, and
	// not where the identifier named nothing in scope.
	AfterArchiveSubscription(ctx context.Context, tx database.Tx, scope tenancy.Scope, subscription *Subscription) error
}

// NoopHooks does nothing, and is what a store built without WithHooks runs. It is
// also the type to embed in a Hooks that overrides some:
//
//	type recordingHooks struct {
//		webhooks.NoopHooks
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

// AfterSaveEndpoint implements Hooks.
func (NoopHooks) AfterSaveEndpoint(context.Context, database.Tx, tenancy.Scope, *Endpoint, *Endpoint) error {
	return nil
}

// AfterArchiveEndpoint implements Hooks.
func (NoopHooks) AfterArchiveEndpoint(context.Context, database.Tx, tenancy.Scope, *Endpoint) error {
	return nil
}

// AfterRotateSecret implements Hooks.
func (NoopHooks) AfterRotateSecret(context.Context, database.Tx, tenancy.Scope, string) error {
	return nil
}

// AfterAddSubscription implements Hooks.
func (NoopHooks) AfterAddSubscription(context.Context, database.Tx, tenancy.Scope, *Subscription, *Subscription) error {
	return nil
}

// AfterArchiveSubscription implements Hooks.
func (NoopHooks) AfterArchiveSubscription(context.Context, database.Tx, tenancy.Scope, *Subscription) error {
	return nil
}

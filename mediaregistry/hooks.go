package mediaregistry

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Hooks is what a consumer hangs off the store's writes: one method per write,
// each called inside the transaction that write ran in, once its statements have
// landed.
//
// The transaction is the point, as it is for identity.Hooks. A registration's
// ordinary companions — an audit entry naming who uploaded what, a data change
// event on an outbox — are the same fact as the row, so a hook receives the
// database.Tx and writes on it, and returning an error fails the write: the
// store answers with that error and no row, and the caller's transaction rolls
// back with it.
//
// That is the whole of what a hook adds over a caller writing the companions
// beside each store call itself, and it is worth having because of where the
// alternative ends up. A consumer that wants every registration and every
// archive recorded, whoever called the store, wraps the Store — and a wrapper
// has to reimplement each write to call through, forward or embed the reads it
// adds nothing to, and be kept in step with an interface it does not own. A
// hook is handed the row the write already read back.
//
// What it costs is what identity.Hooks says it costs: a hook runs with a write
// transaction open. Work that is slow, talks to a network, or can fail for
// reasons the write should survive belongs behind an outbox row the hook
// enqueues, not in the hook. Removing the archived object's bytes from
// AfterArchiveObject makes the archive fail when the bucket is unreachable, and
// leaves bytes gone under a row that rolled back.
//
// No hook is handed a before and an after, because there is no update to hand
// them for: every column is a fact about bytes already in a bucket, and the
// registry has no statement that assigns one after the insert. An archive is
// handed the row as the archive left it, which is the row as it stood plus the
// ArchivedAt it gained.
//
// Every method is "After", and none is a veto. Whether somebody may upload or
// remove an object is decided before the store is called; a hook returning an
// error is an abort of a decision already taken.
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
	// AfterRecordObject is called with the row RecordObject wrote, read back on
	// the transaction: the id it was minted under, the scope the call named and
	// the CreatedAt the database stamped.
	AfterRecordObject(ctx context.Context, tx database.Tx, scope tenancy.Scope, object *Object) error

	// AfterArchiveObject is called with the row ArchiveObject hid, **as the
	// archive left it** — read back on the transaction through the statement
	// that still sees archived rows, carrying the ArchivedAt the archive
	// stamped. It is the same row ArchiveObject answers with, and the last
	// place the key the surviving bytes are at can be read from.
	AfterArchiveObject(ctx context.Context, tx database.Tx, scope tenancy.Scope, object *Object) error

	// AfterArchiveObjectsForOwner is called with the principal an erasure
	// archived the uploads of and how many rows that hid. It is called when the
	// count is zero, too: an erasure that found nothing to archive still ran,
	// and a consumer recording erasures records that one.
	//
	// It is handed a count and not the rows, for the reason the write itself
	// answers with a count — an erasure is about removing a subject's footprint,
	// and a hook handed every row of it would be handed the footprint.
	AfterArchiveObjectsForOwner(ctx context.Context, tx database.Tx, scope tenancy.Scope, ownerID string, archived int64) error
}

// NoopHooks does nothing. It is what a caller passes, by name, when it commits
// nothing alongside these writes — a seed import, a bootstrap tool, a test. It
// is also the type to embed in a Hooks that overrides some:
//
//	type recordingHooks struct {
//		mediaregistry.NoopHooks
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

// AfterRecordObject implements Hooks.
func (NoopHooks) AfterRecordObject(context.Context, database.Tx, tenancy.Scope, *Object) error {
	return nil
}

// AfterArchiveObject implements Hooks.
func (NoopHooks) AfterArchiveObject(context.Context, database.Tx, tenancy.Scope, *Object) error {
	return nil
}

// AfterArchiveObjectsForOwner implements Hooks.
func (NoopHooks) AfterArchiveObjectsForOwner(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}

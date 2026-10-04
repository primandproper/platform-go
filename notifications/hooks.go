package notifications

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Hooks is what a consumer hangs off the store's writes: one method per write
// that takes the caller's transaction, each called inside that transaction once
// the write's statements have landed.
//
// The transaction is the point, as it is for waitlists.Hooks. A notification
// write's ordinary companions — an audit entry saying who was told what, a data
// change event on an outbox — are the same fact as the row, so a hook receives
// the database.Tx and writes on it, and returning an error fails the write: the
// store answers with that error and no row, and the caller's transaction rolls
// back with it.
//
// That is the whole of what a hook adds over a caller writing the companions
// beside each store call itself, and it is worth having because of what the
// caller would otherwise have to hold. RevokeDevice deletes the row, so a
// companion written after it needs the registration from before; a
// re-registration that moved a handset to somebody else is only readable as
// such beside the registration it replaced. A consumer wrapping the Inbox and
// the Registry to record those writes ends up reimplementing every write on
// both to call through, and reading rows the store had already read. A hook is
// handed them.
//
// What it costs is what identity.Hooks says it costs: a hook runs with a write
// transaction open. Work that is slow, talks to a network, or can fail for
// reasons the write should survive belongs behind an outbox row the hook
// enqueues, not in the hook. Pushing to a handset from AfterCreateNotification
// makes filing the notification fail when APNs is down.
//
// An update is handed two rows, the one before it and the one after, because
// what an update means is the difference between them and that is not readable
// once the write has run. The before row costs the update one keyed read on the
// transaction, which the store makes only when hooks are installed.
//
// Every method is "After", and none is a veto. Whether somebody may be told
// something, or may register a handset, is decided before the store is called;
// a hook returning an error is an abort of a decision already taken.
//
// The rows a hook is handed are the rows the write answers its caller with, and
// they are the same values: a hook that modifies one modifies what the caller
// receives. Do not.
//
// [Registry.InvalidateDeviceToken] has no hook, and that is the transaction
// argument read the other way. It takes no transaction — there is no consumer
// request behind it to join — so there is nothing a hook could write its
// companions on that would share the deletion's fate, and no scope to hand it.
// A consumer that wants the provider's verdicts counted reads the store's
// invalidated-token counter.
//
// It is one interface rather than a function type per write so that a
// consumer's recording layer is one type. Embed NoopHooks and override the
// methods that matter; an operation added here later then arrives as a no-op
// rather than a compile failure.
type Hooks interface {
	// AfterCreateNotification is called with the notification
	// CreateNotification filed, carrying the id it was filed under and the
	// creation time the database stamped.
	AfterCreateNotification(ctx context.Context, tx database.Tx, scope tenancy.Scope, notification *Notification) error

	// AfterMarkNotificationRead is called with the notification as it stood
	// before MarkNotificationRead and as MarkNotificationRead left it, both read
	// on the transaction. The write is idempotent, so the two may carry the same
	// ReadAt: a notification already read is stamped by nobody, and a hook that
	// records only first reads compares before.ReadAt with nil.
	AfterMarkNotificationRead(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Notification) error

	// AfterMarkAllNotificationsRead is called with the principal whose inbox
	// MarkAllNotificationsRead stamped and how many notifications that was. It
	// is called when the count is zero, too: the write still ran.
	//
	// It is handed a count and not the rows for the reason the write answers
	// with one — it moves a set, and an inbox can be large.
	AfterMarkAllNotificationsRead(ctx context.Context, tx database.Tx, scope tenancy.Scope, principal string, marked int64) error

	// AfterArchiveNotification is called with the notification
	// ArchiveNotification dismissed, as the archive left it — carrying the
	// ArchivedAt the statement stamped. It is the same row the write answers
	// with, read back through the statement that still sees archived rows.
	AfterArchiveNotification(ctx context.Context, tx database.Tx, scope tenancy.Scope, notification *Notification) error

	// AfterDeleteNotificationsForPrincipal is called with the principal an
	// erasure emptied the inbox of and how many notifications went. It is called
	// when the count is zero, too: an erasure that found nothing to erase still
	// ran, and a consumer recording erasures records that one.
	//
	// It is handed a count and not the rows, for the reason the write itself
	// answers with a count — the rows are the subject's own data, and the
	// erasure exists to remove it.
	AfterDeleteNotificationsForPrincipal(ctx context.Context, tx database.Tx, scope tenancy.Scope, principal string, deleted int64) error

	// AfterRegisterDevice is called with the registration the token had in this
	// scope before RegisterDevice, and the registration it has now — the row
	// the write answers with.
	//
	// before is nil on a first registration. It is also nil when the token
	// arrives from another scope: the write converges on the token across the
	// whole registry, but the before read is keyed on the scope like every
	// other read a consumer reaches, and what a handset was registered as in
	// somebody else's tenant is not this scope's to be told. Where before is
	// not nil the write was a re-registration, and a before.Principal that
	// differs from after.Principal is a handset that changed hands.
	AfterRegisterDevice(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Device) error

	// AfterRevokeDevice is called with the registration RevokeDevice removed,
	// **as it stood before the removal**, which is the same row RevokeDevice
	// answers with. The table no longer holds it.
	AfterRevokeDevice(ctx context.Context, tx database.Tx, scope tenancy.Scope, device *Device) error

	// AfterDeleteDevicesForPrincipal is called with the principal an erasure
	// removed every handset of and how many registrations went, zero included,
	// for the reasons AfterDeleteNotificationsForPrincipal gives.
	AfterDeleteDevicesForPrincipal(ctx context.Context, tx database.Tx, scope tenancy.Scope, principal string, deleted int64) error
}

// NoopHooks does nothing, and is what a store built without WithHooks runs. It is
// also the type to embed in a Hooks that overrides some:
//
//	type recordingHooks struct {
//		notifications.NoopHooks
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

// AfterCreateNotification implements Hooks.
func (NoopHooks) AfterCreateNotification(context.Context, database.Tx, tenancy.Scope, *Notification) error {
	return nil
}

// AfterMarkNotificationRead implements Hooks.
func (NoopHooks) AfterMarkNotificationRead(context.Context, database.Tx, tenancy.Scope, *Notification, *Notification) error {
	return nil
}

// AfterMarkAllNotificationsRead implements Hooks.
func (NoopHooks) AfterMarkAllNotificationsRead(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}

// AfterArchiveNotification implements Hooks.
func (NoopHooks) AfterArchiveNotification(context.Context, database.Tx, tenancy.Scope, *Notification) error {
	return nil
}

// AfterDeleteNotificationsForPrincipal implements Hooks.
func (NoopHooks) AfterDeleteNotificationsForPrincipal(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}

// AfterRegisterDevice implements Hooks.
func (NoopHooks) AfterRegisterDevice(context.Context, database.Tx, tenancy.Scope, *Device, *Device) error {
	return nil
}

// AfterRevokeDevice implements Hooks.
func (NoopHooks) AfterRevokeDevice(context.Context, database.Tx, tenancy.Scope, *Device) error {
	return nil
}

// AfterDeleteDevicesForPrincipal implements Hooks.
func (NoopHooks) AfterDeleteDevicesForPrincipal(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}

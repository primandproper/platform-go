package billing

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Hooks is what a consumer hangs off the store's writes: one method per write,
// each called inside the transaction that write ran in, once its statements have
// landed.
//
// The transaction is the point, as it is for identity.Hooks. A billing write's
// ordinary companions — an audit entry naming who was billed, a data change
// event on an outbox — are the same fact as the row, so a hook receives the
// database.Tx and writes on it, and returning an error fails the write: the
// store answers with that error and no row, and the caller's transaction rolls
// back with it. A redelivered webhook the store refuses — ErrStatusUnchanged,
// ErrAlreadyCompleted, one of the exists sentinels — calls no hook, so a replay
// records nothing a second time.
//
// That is the whole of what a hook adds over a caller writing the companions
// beside each store call itself, and it is worth having because of what the
// caller would otherwise have to hold. An archive hides the row from every read
// by id, so a companion written after it needs the row the archive moved; a
// status move returns nothing, so a companion naming whose subscription moved
// needs a read the store could have made. A consumer wrapping the Store to
// record those writes ends up reimplementing every method of it to call
// through, and reading rows the store had already read. A hook is handed them.
//
// What it costs is what identity.Hooks says it costs: a hook runs with a write
// transaction open. Work that is slow, talks to a network, or can fail for
// reasons the write should survive belongs behind an outbox row the hook
// enqueues, not in the hook. Mailing a receipt from AfterCompletePurchase makes
// the provider's webhook fail when the mail provider is down, and the provider
// retries the delivery.
//
// An update is handed two rows, the one before it and the one after, because
// what an update means is the difference between them and that is not readable
// once the write has run. The before row costs the update one keyed read on the
// transaction, which the store makes only when hooks are installed. The two
// status moves are updates in this sense — which status a subscription moved
// from is the half of a churn event the provider's delivery does not carry — and
// they are handed both rows too, read only when hooks are installed, so a store
// with none still answers a status write without a read.
//
// Every method is "After", and none is a veto. Whether a sale may be recorded or
// an agreement retired is decided before the store is called; a hook returning
// an error is an abort of a decision already taken.
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
	// AfterCreateProduct is called with the product CreateProduct wrote,
	// carrying the id it was minted under and the creation time the database
	// stamped.
	AfterCreateProduct(ctx context.Context, tx database.Tx, scope tenancy.Scope, product *Product) error

	// AfterUpdateProduct is called with the product as it stood before
	// UpdateProduct and as UpdateProduct left it, both read on the transaction —
	// so a hook can say what was repriced, which the row alone cannot.
	// audit.Diff takes the pair.
	AfterUpdateProduct(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Product) error

	// AfterArchiveProduct is called with the product ArchiveProduct withdrew, as
	// the archive left it — ArchivedAt set — read back on the transaction
	// through the statement that still sees archived rows.
	AfterArchiveProduct(ctx context.Context, tx database.Tx, scope tenancy.Scope, product *Product) error

	// AfterCreateSubscription is called with the subscription
	// CreateSubscription opened.
	AfterCreateSubscription(ctx context.Context, tx database.Tx, scope tenancy.Scope, subscription *Subscription) error

	// AfterUpdateSubscription is called with the subscription as it stood
	// before UpdateSubscription and as UpdateSubscription left it, both read on
	// the transaction. The account on both is the stored one, which a sync
	// cannot move.
	AfterUpdateSubscription(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Subscription) error

	// AfterSetSubscriptionStatus is called with the subscription as it stood
	// before SetSubscriptionStatus and as it stands after, both read on the
	// transaction. It is not called for a redelivery: ErrStatusUnchanged is a
	// refusal, and a refused write calls no hook.
	AfterSetSubscriptionStatus(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Subscription) error

	// AfterArchiveSubscription is called with the subscription
	// ArchiveSubscription retired, as the archive left it, read back through the
	// statement that still sees archived rows.
	AfterArchiveSubscription(ctx context.Context, tx database.Tx, scope tenancy.Scope, subscription *Subscription) error

	// AfterCreatePurchase is called with the purchase CreatePurchase recorded,
	// which is always outstanding.
	AfterCreatePurchase(ctx context.Context, tx database.Tx, scope tenancy.Scope, purchase *Purchase) error

	// AfterCompletePurchase is called with the purchase CompletePurchase
	// settled, carrying the CompletedAt it stamped. It is handed one row rather
	// than two because the row before is implied: a completion is guarded on
	// the purchase being outstanding, so before it, CompletedAt was nil.
	AfterCompletePurchase(ctx context.Context, tx database.Tx, scope tenancy.Scope, purchase *Purchase) error

	// AfterArchivePurchase is called with the purchase ArchivePurchase retired,
	// as the archive left it, read back through the statement that still sees
	// archived rows.
	AfterArchivePurchase(ctx context.Context, tx database.Tx, scope tenancy.Scope, purchase *Purchase) error

	// AfterRecordTransaction is called with the ledger row RecordTransaction
	// wrote, before the store's transactions instrument counts it — so a hook
	// that fails leaves no count behind.
	AfterRecordTransaction(ctx context.Context, tx database.Tx, scope tenancy.Scope, transaction *Transaction) error

	// AfterSetTransactionStatus is called with the ledger row as it stood
	// before SetTransactionStatus and as it stands after, both read on the
	// transaction, and before the instrument counts the move. Like its
	// subscription counterpart it is not called for a redelivery.
	AfterSetTransactionStatus(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Transaction) error

	// AfterArchiveTransaction is called with the ledger row ArchiveTransaction
	// retired, as the archive left it, read back through the statement that
	// still sees archived rows.
	AfterArchiveTransaction(ctx context.Context, tx database.Tx, scope tenancy.Scope, transaction *Transaction) error
}

// NoopHooks does nothing, and is what a store built without WithHooks runs. It is
// also the type to embed in a Hooks that overrides some:
//
//	type recordingHooks struct {
//		billing.NoopHooks
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

// AfterCreateProduct implements Hooks.
func (NoopHooks) AfterCreateProduct(context.Context, database.Tx, tenancy.Scope, *Product) error {
	return nil
}

// AfterUpdateProduct implements Hooks.
func (NoopHooks) AfterUpdateProduct(context.Context, database.Tx, tenancy.Scope, *Product, *Product) error {
	return nil
}

// AfterArchiveProduct implements Hooks.
func (NoopHooks) AfterArchiveProduct(context.Context, database.Tx, tenancy.Scope, *Product) error {
	return nil
}

// AfterCreateSubscription implements Hooks.
func (NoopHooks) AfterCreateSubscription(context.Context, database.Tx, tenancy.Scope, *Subscription) error {
	return nil
}

// AfterUpdateSubscription implements Hooks.
func (NoopHooks) AfterUpdateSubscription(context.Context, database.Tx, tenancy.Scope, *Subscription, *Subscription) error {
	return nil
}

// AfterSetSubscriptionStatus implements Hooks.
func (NoopHooks) AfterSetSubscriptionStatus(context.Context, database.Tx, tenancy.Scope, *Subscription, *Subscription) error {
	return nil
}

// AfterArchiveSubscription implements Hooks.
func (NoopHooks) AfterArchiveSubscription(context.Context, database.Tx, tenancy.Scope, *Subscription) error {
	return nil
}

// AfterCreatePurchase implements Hooks.
func (NoopHooks) AfterCreatePurchase(context.Context, database.Tx, tenancy.Scope, *Purchase) error {
	return nil
}

// AfterCompletePurchase implements Hooks.
func (NoopHooks) AfterCompletePurchase(context.Context, database.Tx, tenancy.Scope, *Purchase) error {
	return nil
}

// AfterArchivePurchase implements Hooks.
func (NoopHooks) AfterArchivePurchase(context.Context, database.Tx, tenancy.Scope, *Purchase) error {
	return nil
}

// AfterRecordTransaction implements Hooks.
func (NoopHooks) AfterRecordTransaction(context.Context, database.Tx, tenancy.Scope, *Transaction) error {
	return nil
}

// AfterSetTransactionStatus implements Hooks.
func (NoopHooks) AfterSetTransactionStatus(context.Context, database.Tx, tenancy.Scope, *Transaction, *Transaction) error {
	return nil
}

// AfterArchiveTransaction implements Hooks.
func (NoopHooks) AfterArchiveTransaction(context.Context, database.Tx, tenancy.Scope, *Transaction) error {
	return nil
}

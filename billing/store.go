package billing

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Store is the whole of what this package persists: what a deployment sells,
// what each account has bought, and what happened when they paid for it.
//
// It is four interfaces because they have four callers, and the split is the
// useful part. A checkout path reaches a PurchaseStore, a webhook handler
// reaches the SubscriptionStore and the TransactionStore, an admin console
// reaches the ProductStore, and an entitlement check reaches one method on the
// SubscriptionStore. Splitting them is what lets a component depend on the part
// it uses; Store is here for the wiring that provides all four.
//
// # The transaction is the caller's
//
// Every write takes a database.Tx and every read takes the wider
// database.SQLQueryExecutor, which is the module's store convention rather than
// anything this package invented. There is no form of any write that opens a
// transaction of its own, and in this package that absence is worth more than it
// is anywhere else: a payment provider's event is rarely one row. The audit
// entry naming who was billed and the data change event on an outbox somebody
// fans out are the ordinary companions, and a companion written after this
// store's own transaction has committed is one that can go missing while the row
// stays. A subscription status write whose audit entry landed separately is a
// billing state change nobody can attribute, in the one domain where attribution
// is the point.
//
// A database.Tx is producible only by database.RunInTransaction, so the
// obligation is the compiler's rather than a doc comment's. A caller with
// genuinely nothing to join opens one with Client.WithTransaction and passes the
// Tx it is handed.
//
// The read takes the wider type so that one method serves both moments. An
// entitlement check holds no transaction and passes Client.Reader(); a webhook
// handler that has just recorded a charge passes the Tx it wrote through, and
// sees it. A read narrowed to Tx would have forced the first caller into a
// transaction it has no use for, and one narrowed to Client.Reader() would have
// read a database that does not yet hold the row its caller just wrote.
//
// That widening is load-bearing here rather than incidental, because three of
// these reads are also the checks the writes make. ProductExists is what a
// subscription or a purchase write is gated on, and the three
// GetXByExternalID reads are the collision checks. Run on the caller's
// executor, a product stocked and subscribed to in one transaction is visible to
// the check that would otherwise refuse the subscription, and a redelivery
// arriving in the same transaction as the row it collides with is named by a
// snapshot that can see that row rather than mis-blamed by one that cannot.
//
// A Store that is not a SQL store still takes these types; an implementation
// with no transaction of its own ignores the executor, and the seam stays one
// signature rather than one per backing.
//
// # The scope is an argument, on every method
//
// Every method takes a tenancy.Scope, and none of them offers a variant that
// omits it. A deployment with one catalog passes tenancy.Global() everywhere and
// behaves exactly as it would have without the column.
//
// That includes the four writes that take a whole entity. They read the scope
// off the argument rather than off Product.Scope and its siblings, for the
// reason comments.Store gives about Comment.Scope: an entity field is exactly
// the derivation the column rule exists to rule out. The entity's own scope is
// overwritten with the argument on the way in.
//
// # A write answers with the row it wrote
//
// Every write here but two returns its row, read back on the caller's own
// transaction after the statement: the four creates, the two updates, the
// completion and the four archives. A consumer's audit entry, receipt or outbox
// event is written from what the statement left behind rather than from what
// the caller assembled, and the archives are the sharpest case — every keyed
// read over these tables filters archived_at IS NULL, so the row a withdrawal
// or a retirement moved is a row no read by id reaches afterwards, and a caller
// that wants it back is left paging for archived rows to find one. The row
// comes back only alongside a nil error; a refused write answers with nil and
// its existing sentinel.
//
// The read-back is a second statement rather than RETURNING, which MySQL has
// none of — see billing/internal/queries. There is no gap between the two: the
// guarded write holds the row until the caller commits, and the read runs on
// the same transaction.
//
// The two exceptions are SetSubscriptionStatus and SetTransactionStatus, and
// what makes them exceptions is that each assigns one fact the caller already
// holds to a row every read still reaches. A provider event carries the status,
// the caller passed it in, and GetSubscription or GetTransaction answers with
// the rest on the transaction that wrote it — so returning the row would charge
// every status write a read for a value nobody learned anything from.
type Store interface {
	ProductStore
	SubscriptionStore
	PurchaseStore
	TransactionStore
}

// ProductStore is the catalog: what a deployment sells, at what price, on what
// recurrence.
//
// Every method takes a tenancy.Scope, and a deployment with one catalog passes
// tenancy.Global() to all of them. There is deliberately no unscoped variant of
// any of these — see the tenancy package.
type ProductStore interface {
	// CreateProduct adds a product to the scope's catalog through the caller's
	// transaction, so the product commits with whatever the caller writes beside
	// it. It returns the product as stored, with the id it was minted under and
	// the creation time the database assigned. A nil tx is an error wrapping
	// ErrNilExecutor.
	//
	// It refuses a product with no name, an unrecognized kind, a currency that
	// is not three characters, a negative price, a recurring product with no
	// billing interval, and a one-time product that has one. A provider-side id
	// already claimed in this scope returns an error wrapping ErrProductExists
	// rather than a driver's constraint violation.
	//
	// Every statement runs on tx: the insert-ignore, the read-back of the
	// creation time, and the attribution read the insert makes when it loses. A
	// subscription opened against this product later in the same transaction
	// finds it.
	CreateProduct(ctx context.Context, tx database.Tx, scope tenancy.Scope, product *Product) (*Product, error)

	// GetProduct reads one live product by id, on the caller's executor — so a
	// caller inside a transaction reads the product that transaction has written
	// and not yet committed.
	GetProduct(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, productID string) (*Product, error)

	// GetProductByExternalID reads one live product by the payment provider's
	// identifier for it, which is the lookup a catalog sync makes.
	//
	// It refuses an empty identifier with ErrEmptyExternalID rather than
	// answering "not found": the column is NULL for a product with no provider
	// behind it, and a comparison against NULL matches nothing, so the honest
	// answer to an empty argument is that the caller did not supply one.
	GetProductByExternalID(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		externalProductID string,
	) (*Product, error)

	// ProductExists reports whether the scope has a live product by that id,
	// without reading it.
	//
	// It is the check a subscription or a purchase write makes before it
	// inserts, on whatever executor that write is running on — so a write inside
	// a transaction sees a product that transaction stocked. That is why this is
	// the one table here that keeps its existence query.
	ProductExists(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, productID string) (bool, error)

	// ListProducts pages the scope's catalog.
	ListProducts(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Product], error)

	// UpdateProduct rewrites a product's name, description, kind, price,
	// currency, billing interval and provider-side id, through the caller's
	// transaction, and returns the product as stored. A nil tx is an error
	// wrapping ErrNilExecutor.
	//
	// Repricing a product changes what the next sale costs and nothing about
	// what anybody already paid — the amount on a purchase and on a ledger row
	// is that sale's own. What it will not do is revive an archived product.
	//
	// It does not touch the Product it was handed. What comes back is the row
	// the statement left, read back on tx, which carries the columns this write
	// does not assign — so a caller writing an audit entry describes what was
	// stored rather than what it proposed.
	//
	// The collision check against the provider-side id runs on tx, so a catalog
	// sync writing several products in one transaction is checked against what
	// that transaction has written rather than against what was committed before
	// it began.
	UpdateProduct(ctx context.Context, tx database.Tx, scope tenancy.Scope, product *Product) (*Product, error)

	// ArchiveProduct withdraws a product from sale, through the caller's
	// transaction, and returns the product it withdrew. A nil tx is an error
	// wrapping ErrNilExecutor.
	//
	// The subscriptions already on it keep renewing and the purchases already
	// made stay readable, because archiving is taking something off the shelf
	// rather than cancelling what has been sold. A deployment that means to end
	// the agreements ends them through the payment provider, and the statuses
	// arrive here as events.
	//
	// The row comes back because this is the last moment it can be read by id:
	// GetProduct stops finding a withdrawn product, and what still finds one is
	// a page somebody asked for archived rows on. A caller that means to say
	// what was taken off the shelf — its price, its provider id, what it was
	// called — has this answer or that search.
	ArchiveProduct(ctx context.Context, tx database.Tx, scope tenancy.Scope, productID string) (*Product, error)
}

// SubscriptionStore is the recurring half: who is paying for what, and until
// when.
type SubscriptionStore interface {
	// CreateSubscription opens an agreement through the caller's transaction and
	// returns it as stored. A nil tx is an error wrapping ErrNilExecutor.
	//
	// A provider-side id already claimed in this scope returns an error wrapping
	// ErrSubscriptionExists, which is what a redelivered subscription webhook
	// gets — the uniqueness is what keeps one paying customer from ending up
	// with two agreements.
	//
	// This is the write the caller's transaction is sharpest for: the
	// subscription, the audit entry naming the event that opened it, and the data
	// change event the dispatcher fans out land together or not at all. A store
	// owning its own transaction would leave a window in which a subscription
	// exists and an entitlement check can read it while nothing has been told
	// about it.
	//
	// Two reads run on tx with the write. The product check the create is gated
	// on runs there, so a product created through CreateProduct earlier in the
	// same transaction is one a subscription can be opened against; and so does
	// the attribution read on the losing path, so a redelivery arriving in the
	// same transaction as the row it collides with is named rather than
	// mis-blamed.
	//
	// It is not an RPC. billing/grpc serves no write that opens an agreement:
	// the caller is a processor callback or the checkout flow, already inside
	// the transaction this method's own documentation is about, and a network
	// hop in the middle of it is the one thing that transaction cannot have.
	CreateSubscription(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		subscription *Subscription,
	) (*Subscription, error)

	// GetSubscription reads one live subscription by id, on the caller's
	// executor.
	GetSubscription(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		subscriptionID string,
	) (*Subscription, error)

	// GetSubscriptionByExternalID reads one live subscription by the payment
	// provider's identifier for it, which is the lookup every subscription
	// webhook begins with. It refuses an empty identifier — see
	// GetProductByExternalID.
	GetSubscriptionByExternalID(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		externalSubscriptionID string,
	) (*Subscription, error)

	// ListSubscriptions pages every subscription in the scope. It is the
	// administrative read.
	ListSubscriptions(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Subscription], error)

	// ListSubscriptionsForAccount pages one account's subscriptions, current and
	// lapsed alike. It is the customer-facing read, and a separate statement
	// rather than ListSubscriptions filtered afterwards, because a page filtered
	// after the fact is a page whose size the caller cannot rely on.
	ListSubscriptionsForAccount(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		accountID string,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Subscription], error)

	// ListCurrentSubscriptions pages the account's subscriptions whose paid
	// period covers the store's clock.
	//
	// It is the read every entitlement check ultimately makes, and it
	// deliberately does not filter on the status: which reported status leaves
	// an account entitled is policy, it differs between deployments selling the
	// same thing, and capitalism's documentation is where that ruling lives. So
	// this answers the part that is a fact about the row, and the caller reads
	// Subscription.Status off what comes back. PlanSource is the worked example.
	ListCurrentSubscriptions(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		accountID string,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Subscription], error)

	// UpdateSubscription rewrites the plan, the provider-side id, the status and
	// the paid period — everything a provider's own subscription can move —
	// through the caller's transaction. A nil tx is an error wrapping
	// ErrNilExecutor.
	//
	// It is the sync write, and it assigns all of them together because this row
	// is a restatement of a fact the provider owns — a sync able to write half of
	// it would leave the row disagreeing with the truth in a way nothing reports.
	// What it does not touch is the account: moving a subscription between
	// accounts is not an edit, and a store that allowed it would let one
	// customer's payments settle another's bill.
	//
	// The collision check against the provider-side id runs on tx, for the reason
	// UpdateProduct's does.
	//
	// It returns the subscription as stored and does not touch the one it was
	// handed, for the reason UpdateProduct does — and here the difference is
	// visible: the account is not assignable, so a sync that restated one is
	// answered by the row still naming the account it always did.
	//
	// It is not an RPC, for the reason CreateSubscription is not: a sync write
	// is the provider's event restated, and its caller is the receiver holding
	// that event.
	UpdateSubscription(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		subscription *Subscription,
	) (*Subscription, error)

	// SetSubscriptionStatus moves the standing and nothing else, through the
	// caller's transaction. A nil tx is an error wrapping ErrNilExecutor.
	//
	// It is the narrow write for the event that carries only a status, and it is
	// guarded: the statement is `SET status = X WHERE status <> X`, so a
	// redelivered event finds the row already holding X, touches nothing, and is
	// reported as ErrStatusUnchanged. That is an answer rather than a failure —
	// a caller processing provider events acknowledges the delivery on it.
	//
	// The statement runs on tx, and so does the read that tells ErrStatusUnchanged
	// from ErrSubscriptionNotFound — so a status write against a subscription
	// this transaction opened is answered by the row it wrote rather than by a
	// snapshot that predates it.
	//
	// It is not an RPC, and of the four callback writes this is the sharpest
	// case. The caller is a callback, arriving at a Stripe or RevenueCat
	// receiver the consumer owns, and the guard above is what makes a
	// redelivery safe:
	// the answer to a replayed event comes back to the handler that has to
	// decide whether to acknowledge the delivery. billing/grpc puts nothing
	// between those two.
	SetSubscriptionStatus(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		subscriptionID string,
		status capitalism.SubscriptionStatus,
	) error

	// ArchiveSubscription retires a subscription administratively, through the
	// caller's transaction, and returns the subscription it retired. A nil tx is
	// an error wrapping ErrNilExecutor.
	//
	// It is not a cancellation and must not be used as one: a cancelled
	// subscription is one whose status says so, which is a fact the provider
	// reports, and archiving hides the row from every read that does not ask for
	// archived rows while changing nothing about what it holds. The ledger rows
	// pointing at it are left alone.
	//
	// The row comes back for ArchiveProduct's reason, and it carries the two
	// facts somebody reading those ledger rows afterwards needs: the provider's
	// own id for the agreement, and the period it was paid through.
	ArchiveSubscription(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		subscriptionID string,
	) (*Subscription, error)
}

// PurchaseStore is the one-time half: what an account bought outright.
type PurchaseStore interface {
	// CreatePurchase records a sale through the caller's transaction and returns
	// it as stored, outstanding. A nil tx is an error wrapping ErrNilExecutor.
	//
	// The row is written when the attempt starts rather than when it settles, so
	// that the transaction recording the attempt has something of ours to point
	// at. A provider-side transaction id already claimed in this scope returns an
	// error wrapping ErrPurchaseExists.
	//
	// The same two reads run on tx as for CreateSubscription: the product check
	// the create is gated on, and the attribution read on the losing path.
	//
	// It is not an RPC. Its caller is the checkout handler that created the
	// payment intent through capitalism, writing this row in the same
	// transaction so the intent has something of ours to point at.
	CreatePurchase(ctx context.Context, tx database.Tx, scope tenancy.Scope, purchase *Purchase) (*Purchase, error)

	// GetPurchase reads one live purchase by id, on the caller's executor.
	GetPurchase(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		purchaseID string,
	) (*Purchase, error)

	// GetPurchaseByExternalID reads one live purchase by the payment provider's
	// identifier for the payment. It refuses an empty identifier — see
	// GetProductByExternalID.
	GetPurchaseByExternalID(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		externalTransactionID string,
	) (*Purchase, error)

	// ListPurchases pages every purchase in the scope. It is the administrative
	// read.
	ListPurchases(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Purchase], error)

	// ListPurchasesForAccount pages one account's purchases.
	ListPurchasesForAccount(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		accountID string,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Purchase], error)

	// CompletePurchase stamps the moment the money arrived, through the caller's
	// transaction. A nil tx is an error wrapping ErrNilExecutor.
	//
	// at is the provider's own settlement time rather than this store's clock,
	// because a webhook can arrive hours after the payment it describes and
	// revenue is recognized against when the money moved. The zero time falls
	// back to the store's clock, for a completion that has no provider behind it
	// — a comped order, a migration.
	//
	// It is guarded on completed_at being NULL, so a purchase completes exactly
	// once and a redelivered event gets ErrAlreadyCompleted rather than restamping
	// the moment it settled. Both the statement and the read that tells
	// ErrAlreadyCompleted from ErrPurchaseNotFound run on tx, so a sale created
	// and settled in one transaction — a comped order, a migration — is answered
	// by the row that transaction wrote.
	//
	// It returns the settled purchase, which is what a receipt and an audit
	// entry are written from — including the stamp itself, this store's clock's
	// whenever the caller passed the zero time. That read is the one the guard's
	// refusal already made; a completion and a replay now cost the same two
	// statements.
	//
	// It is not an RPC. A client does not decide that money arrived; a
	// processor's callback reports it, carrying the settlement time this method
	// takes and the delivery it may repeat, and it writes here inside its own
	// transaction. See billing/grpc.
	CompletePurchase(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		purchaseID string,
		at time.Time,
	) (*Purchase, error)

	// ArchivePurchase retires a purchase administratively, through the caller's
	// transaction, and returns the purchase it retired. A nil tx is an error
	// wrapping ErrNilExecutor. It is not a refund — a refund is a transaction of
	// its own, recorded through the ledger.
	//
	// The row comes back for ArchiveProduct's reason, and what a caller wants
	// off it is whether the money ever arrived: to every read by id afterwards,
	// a retired sale that settled and one that never did are the same absence.
	ArchivePurchase(ctx context.Context, tx database.Tx, scope tenancy.Scope, purchaseID string) (*Purchase, error)
}

// TransactionStore is the ledger: what each attempt to move money left behind.
type TransactionStore interface {
	// RecordTransaction writes one attempt through the caller's transaction and
	// returns it as stored. A nil tx is an error wrapping ErrNilExecutor.
	//
	// It is named for what it is rather than Create, because a ledger row is a
	// record of something that happened elsewhere rather than a resource this
	// application decided to make.
	//
	// A provider-side id already recorded in this scope returns an error
	// wrapping ErrTransactionExists. That is the sentinel this table is shaped
	// around: payment providers redeliver, and a ledger that recorded one charge
	// twice is a number somebody reconciles by hand.
	//
	// This is the write the caller's transaction is sharpest for. A charge
	// arrives as a webhook, and what a consumer records about it — who was
	// billed, what the dispatcher has to fan out — is written in the same breath;
	// a ledger row that committed ahead of its companions is a charge nothing
	// downstream heard about.
	//
	// The attribution the insert makes when it loses runs on tx, which for this
	// table means the referent checks too: a ledger row naming a purchase created
	// earlier in the same transaction is attributed against a snapshot that can
	// see it, rather than refused for naming a row nobody has.
	//
	// The store's transactions instrument is incremented when the statement
	// writes the row rather than when the caller commits — nothing here can
	// observe somebody else's commit, and leaving this path uncounted would
	// remove the instrument entirely. A rolled back transaction therefore leaves
	// a count with no row behind it.
	//
	// It is not an RPC. This is the write billing/grpc's absence is most about:
	// a charge arrives as a webhook, and the ledger row, the audit entry naming
	// who was billed and the outbox event somebody fans out are one fact. A
	// ledger row that committed ahead of its companions is a charge nothing
	// downstream heard about, and an RPC is exactly that separation.
	RecordTransaction(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		transaction *Transaction,
	) (*Transaction, error)

	// GetTransaction reads one live ledger row by id, on the caller's executor.
	GetTransaction(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		transactionID string,
	) (*Transaction, error)

	// GetTransactionByExternalID reads one live ledger row by the payment
	// provider's identifier for the attempt, which is how a handler tells a
	// redelivery from a new charge before it writes. It refuses an empty
	// identifier — see GetProductByExternalID.
	GetTransactionByExternalID(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		externalTransactionID string,
	) (*Transaction, error)

	// ListTransactions pages every ledger row in the scope. It is the
	// administrative read, and the one a reconciliation walks.
	ListTransactions(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Transaction], error)

	// ListTransactionsForAccount pages one account's ledger, oldest first by
	// default, which is the order the attempts were made in.
	ListTransactionsForAccount(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		accountID string,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Transaction], error)

	// SetTransactionStatus moves an attempt's outcome — pending becoming
	// succeeded, succeeded becoming refunded — through the caller's transaction.
	// A nil tx is an error wrapping ErrNilExecutor.
	//
	// It is the only write this table has beyond the insert, and it is guarded
	// exactly as SetSubscriptionStatus is — a redelivered event gets
	// ErrStatusUnchanged, decided on tx along with the read behind it.
	// Everything else about a ledger row is a fact about money that has already
	// moved, and there is deliberately no statement able to assign any of it.
	//
	// The counter is incremented on the write rather than on the caller's commit,
	// for the reason RecordTransaction gives.
	//
	// It is not an RPC, for the reason SetSubscriptionStatus is not: the guard
	// answers a redelivery, and the caller who acts on that answer is the
	// receiver that took the delivery.
	SetTransactionStatus(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		transactionID string,
		status TransactionStatus,
	) error

	// ArchiveTransaction retires a ledger row administratively, through the
	// caller's transaction, and returns the row it retired. A nil tx is an error
	// wrapping ErrNilExecutor.
	//
	// It exists for the row written in error — a test charge, a duplicate that
	// predates the uniqueness — and not for a refund, which is a transaction of
	// its own.
	//
	// The row comes back for ArchiveProduct's reason, and this is where it
	// matters most: a ledger row taken out of a reconciliation is an amount of
	// money that stops being counted, and the entry saying which amount is
	// written from a row no read by id reaches once the transaction commits.
	ArchiveTransaction(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		transactionID string,
	) (*Transaction, error)
}

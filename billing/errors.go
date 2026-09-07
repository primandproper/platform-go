package billing

import (
	platformerrors "github.com/primandproper/platform-go/v14/errors"
)

// The sentinels this package returns. They live together because a caller
// deciding what to do next is choosing between them, and a set spread across the
// files that happen to return each one cannot be read as the set it is.
var (
	// ErrNilDatabaseClient indicates a nil database.Client. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil billing database client")

	// ErrNilProduct indicates a nil *Product where one was required.
	ErrNilProduct = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil product")

	// ErrNilSubscription indicates a nil *Subscription where one was required.
	ErrNilSubscription = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil subscription")

	// ErrNilPurchase indicates a nil *Purchase where one was required.
	ErrNilPurchase = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil purchase")

	// ErrNilTransaction indicates a nil *Transaction where one was required.
	ErrNilTransaction = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil transaction")

	// ErrNilExecutor indicates a nil executor. Every method here runs on one the
	// caller supplies — a database.Tx for a write, an executor for a read — so
	// there is no method that can fall back to a connection of the store's own.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil billing query executor")

	// ErrEmptyProductName indicates a product with no name. A product nobody
	// can name is a product nobody can put on an invoice.
	ErrEmptyProductName = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty product name")

	// ErrEmptyAccount indicates a write or a read that named no account.
	//
	// It is refused rather than treated as "every account", which is what an
	// empty predicate would make it: a page of one customer's invoices is not a
	// read that should degrade into a page of everybody's.
	ErrEmptyAccount = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty account")

	// ErrEmptyProduct indicates a subscription or a purchase naming no product.
	ErrEmptyProduct = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty product id")

	// ErrEmptyExternalID indicates a lookup by a payment provider's identifier
	// with no identifier.
	//
	// The column is nullable and the empty string is stored as NULL, so a read
	// that passed one through would compare against NULL and match nothing —
	// reporting "no such subscription" for what is really a caller that failed
	// to read the id off the event it is handling. See billing/migrations.
	ErrEmptyExternalID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty external id")

	// ErrEmptyPeriod indicates a subscription with no paid period.
	//
	// It is refused rather than defaulted, because every default available is a
	// policy: the provider's own dates are the only correct answer, and a
	// subscription with no period is one nothing can decide the standing of.
	ErrEmptyPeriod = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "subscription has no paid period")

	// ErrBackwardsPeriod indicates a paid period that ends before it starts.
	ErrBackwardsPeriod = platformerrors.New("subscription period ends before it starts")

	// ErrInvalidKind indicates a product kind this package does not implement.
	ErrInvalidKind = platformerrors.New("unrecognized product kind")

	// ErrInvalidStatus indicates a status this package does not implement: a
	// transaction status outside the four, or a subscription status capitalism
	// does not recognize.
	//
	// The subscription case is deliberately strict. capitalism's unknown status
	// is the zero value and means "the provider said something no adapter could
	// place", which is a fact worth keeping in a variable and not one worth
	// writing into a column that entitlement decisions are read from.
	ErrInvalidStatus = platformerrors.New("unrecognized status")

	// ErrInvalidCurrency indicates a currency that is not three characters.
	//
	// Only the length is checked — see [Product.Currency]. What this catches is
	// the empty string and the caller who passed a symbol or an amount into the
	// wrong argument, which are the two mistakes a length check can actually
	// find.
	ErrInvalidCurrency = platformerrors.New("currency is not a three-character code")

	// ErrNegativeAmount indicates a negative amount.
	//
	// A refund is a transaction of its own carrying the amount returned, with
	// [TransactionRefunded] as its status — not a negative row against the
	// original. One sign convention, decided here, is what keeps a sum over the
	// ledger from depending on who wrote each row.
	ErrNegativeAmount = platformerrors.New("amount is negative")

	// ErrEmptyBillingInterval indicates a recurring product with no billing
	// interval, which is a subscription nothing knows when to renew.
	ErrEmptyBillingInterval = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "recurring product has no billing interval")

	// ErrUnexpectedBillingInterval indicates a one-time product carrying a
	// billing interval. It is refused rather than ignored, because a product
	// holding a recurrence nothing acts on is a product somebody will eventually
	// believe recurs.
	ErrUnexpectedBillingInterval = platformerrors.New("one-time product has a billing interval")

	// ErrAmbiguousTransaction indicates a transaction naming both a
	// subscription and a purchase. An attempt settles one or the other, and a
	// row claiming both is one a reconciliation would count twice.
	ErrAmbiguousTransaction = platformerrors.New("transaction names both a subscription and a purchase")

	// ErrProductNotFound indicates no live product by that id in this scope.
	ErrProductNotFound = platformerrors.New("product not found")

	// ErrSubscriptionNotFound indicates no live subscription by that id in this
	// scope.
	ErrSubscriptionNotFound = platformerrors.New("subscription not found")

	// ErrPurchaseNotFound indicates no live purchase by that id in this scope.
	ErrPurchaseNotFound = platformerrors.New("purchase not found")

	// ErrTransactionNotFound indicates no live transaction by that id in this
	// scope.
	ErrTransactionNotFound = platformerrors.New("transaction not found")

	// ErrProductExists indicates a product whose provider-side id is already
	// claimed by another product in this scope.
	ErrProductExists = platformerrors.New("a product with that external id already exists")

	// ErrSubscriptionExists indicates a subscription whose provider-side id is
	// already claimed in this scope.
	//
	// It is what a redelivered subscription webhook gets, and it is a distinct
	// error rather than a raw constraint violation because the difference
	// between "this event has already been handled" and "the database is unwell"
	// decides whether the caller acknowledges the delivery or lets it retry.
	ErrSubscriptionExists = platformerrors.New("a subscription with that external id already exists")

	// ErrPurchaseExists indicates a purchase whose provider-side transaction id
	// is already claimed in this scope.
	ErrPurchaseExists = platformerrors.New("a purchase with that external id already exists")

	// ErrTransactionExists indicates a ledger row whose provider-side id is
	// already recorded in this scope.
	//
	// This is the one the ledger is shaped around. Payment providers redeliver,
	// and a ledger that recorded the same charge twice is a number somebody
	// reconciles by hand; the uniqueness makes the second insert collide, and
	// this is what the caller is told so it can acknowledge the duplicate rather
	// than retry it forever. See billing/migrations.
	ErrTransactionExists = platformerrors.New("a transaction with that external id already recorded")

	// ErrIDTaken indicates a create whose id another row in this scope already
	// carries.
	//
	// It is reachable only from a caller that supplied the id — a create that
	// leaves it empty is given a minted one — and it is distinct from the four
	// exists sentinels because it names a different mistake: those are a
	// provider's identifier arriving twice, which is the ordinary redelivery,
	// and this is an application handing out an identifier it has used before.
	//
	// MySQL and SQLite report it here. Postgres raises its own primary key
	// violation instead, because the create's conflict target names the external
	// id index and a Postgres ON CONFLICT absorbs only the index it names, where
	// the IGNORE the other two spell covers every constraint on the table. The
	// three dialects agree on refusing the row and differ on which error says so;
	// nothing that is not a caller's own bug reaches either.
	ErrIDTaken = platformerrors.New("another row in this scope already has that id")

	// ErrStatusUnchanged indicates a status write that found the row already
	// holding the status it was going to write.
	//
	// It is an answer rather than a failure, and a caller processing provider
	// events should treat it as one: the guard is the affected-row count of
	// `SET status = X WHERE status <> X`, so this is what a redelivered event
	// gets, and the work it describes has already been done. It is a distinct
	// error from the not-found sentinels precisely so that the two can be told
	// apart — a redelivery is fine, and a status write against a subscription
	// that is not there is not.
	ErrStatusUnchanged = platformerrors.New("row already holds that status")

	// ErrAlreadyCompleted indicates a completion of a purchase whose money has
	// already arrived.
	//
	// A replay reports it rather than restamping, because the moment a payment
	// settled is a fact about it and a second delivery should not move it.
	ErrAlreadyCompleted = platformerrors.New("purchase has already been completed")
)

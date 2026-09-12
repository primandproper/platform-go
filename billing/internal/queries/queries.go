package queries

import (
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// The tables this package owns, at their canonical spelling — what the emitted
// .sql names, and what the billing store's own prefix rendering starts from.
const (
	ProductsTable      = "billing_products"
	SubscriptionsTable = "billing_subscriptions"
	PurchasesTable     = "billing_purchases"
	TransactionsTable  = "billing_transactions"
)

// TableNames is every table billing owns, in the order the DDL creates them.
//
// That order is the reference order — products before what references them,
// subscriptions and purchases before the transactions that point at either — and
// it is the order [Render] feeds the querygen registry.
//
// billing/migrations is where a consumer gets these names rendered at their
// prefix. This list is the canonical spelling, and migrations.Tables reads the
// DDL, so the two are cross-checked against each other in this package's tests
// rather than one being derived from the other.
var TableNames = []string{
	ProductsTable,
	SubscriptionsTable,
	PurchasesTable,
	TransactionsTable,
}

// ScopeColumn is the tenancy dimension all four tables carry and every statement
// over any of them is keyed on. It is a column, not a convention: an unscoped
// read of this schema is not expressible, because there is no statement that
// omits it.
//
// Each table carries its own copy rather than reaching the referenced row's
// through a foreign key. A scope predicate that had to join to find its column
// is a predicate a read can omit, and every read here is one that must not.
const ScopeColumn = "scope"

// The columns more than one table here declares. Exported, and spelled once,
// because two spellings of one column is the drift this package exists to
// prevent — and because a ledger row and the purchase it settles must agree
// about what "amount_cents" is called before anybody can reconcile them.
const (
	AccountColumn     = querygen.BelongsToAccountColumn
	ProductColumn     = "product_id"
	StatusColumn      = "status"
	AmountCentsColumn = "amount_cents"
	CurrencyColumn    = "currency"

	// ExternalTransactionColumn is the provider's identifier for a movement of
	// money, and it is on two tables: the purchase the money was for, and the
	// ledger row recording the attempt. Both are nullable and both are unique
	// within a scope — see billing/migrations.
	ExternalTransactionColumn = "external_transaction_id"
)

// The product columns nothing else declares.
const (
	ProductNameColumn        = "name"
	ProductDescriptionColumn = "description"
	ProductKindColumn        = "kind"
	ProductIntervalColumn    = "billing_interval_months"
	ExternalProductColumn    = "external_product_id"
)

// The subscription columns nothing else declares.
const (
	ExternalSubscriptionColumn = "external_subscription_id"
	PeriodStartColumn          = "current_period_start"
	PeriodEndColumn            = "current_period_end"
)

// CompletedAtColumn is the purchase's one lifecycle column: NULL until the
// payment behind it succeeds.
const CompletedAtColumn = "completed_at"

// The transaction columns naming what a payment attempt was for. Both are
// nullable and at most one is set — see billing/migrations.
const (
	SubscriptionColumn = "subscription_id"
	PurchaseColumn     = "purchase_id"
)

// CurrentAsOfArg is the instant "this subscription's paid period covers now" is
// decided against.
//
// It is bound rather than read off the server's clock, which is the choice
// querygen.AtMostArgument documents: current_period_end is the provider's date,
// written by the application from whatever clock the store was handed, so
// comparing it against CURRENT_TIMESTAMP would be two clocks deciding one row —
// and under a test clock that only moves when a test moves it, the two are years
// apart.
const CurrentAsOfArg = "current_as_of"

// Products is the catalog: what a deployment sells, at what price, on what
// recurrence.
//
// It takes the standard set bar the create — every table here renders that from
// [guardedCreates] instead — existence check included. The existence question is
// a real one here and nowhere else in this schema: writing a subscription or a
// purchase means naming a product, and the create of either asks it before it
// inserts, which is what keeps a bad product id one answer on all three dialects
// rather than a foreign key error on two of them and a skipped row on the third.
//
// Nullable is the two columns a product may genuinely lack. A one-time product
// has no billing interval, and a product never mirrored to a payment provider
// has no external id — and the second of those is what lets the unique index
// beside it mean anything. See billing/migrations.
var Products = Table{
	Name:     ProductsTable,
	Singular: "Product",
	Plural:   "Products",
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		ProductNameColumn,
		ProductDescriptionColumn,
		ProductKindColumn,
		AmountCentsColumn,
		CurrencyColumn,
		ProductIntervalColumn,
		ExternalProductColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Nullable: []string{ProductIntervalColumn, ExternalProductColumn},
	Updatable: []string{
		ProductNameColumn,
		ProductDescriptionColumn,
		ProductKindColumn,
		AmountCentsColumn,
		CurrencyColumn,
		ProductIntervalColumn,
		ExternalProductColumn,
	},
	Omitted: []querygen.StandardQuery{querygen.CreateQuery},
}

// Subscriptions is a recurring agreement: one account, one product, for as long
// as it is paid.
//
// Updatable is everything a provider's own subscription can move — the plan, the
// provider-side id, the standing, and the paid period — because this row is a
// restatement of a fact the provider owns, and a sync that could not write half
// of it would be a sync that left the row disagreeing with the truth. What is
// not updatable is belongs_to_account: moving a subscription between accounts is
// not an edit, it is a cancellation and a new agreement, and a column that
// allowed it would let one customer's payments settle another customer's bill.
//
// The existence check is omitted. Nothing asks whether a subscription is there
// without also wanting its status and its period, since those are the whole of
// what the question is ever asked for.
var Subscriptions = Table{
	Name:     SubscriptionsTable,
	Singular: "Subscription",
	Plural:   "Subscriptions",
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		AccountColumn,
		ProductColumn,
		ExternalSubscriptionColumn,
		StatusColumn,
		PeriodStartColumn,
		PeriodEndColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Nullable: []string{ExternalSubscriptionColumn},
	Updatable: []string{
		ProductColumn,
		ExternalSubscriptionColumn,
		StatusColumn,
		PeriodStartColumn,
		PeriodEndColumn,
	},
	Omitted: []querygen.StandardQuery{querygen.CreateQuery, querygen.ExistsQuery},
}

// Purchases is a one-time sale: bought once, owned afterwards.
//
// It declares no Updatable and omits the standard update, which is the whole
// shape of this table. A purchase has exactly one thing that changes about it —
// whether the money arrived — and that is CompletePurchase below, a guarded
// write of one column. A standard update would additionally let a caller rewrite
// the amount somebody paid, which is not an edit anybody should be able to make
// by handing back a struct they read a moment ago.
//
// The existence check is omitted for the reason it is omitted on subscriptions.
var Purchases = Table{
	Name:     PurchasesTable,
	Singular: "Purchase",
	Plural:   "Purchases",
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		AccountColumn,
		ProductColumn,
		ExternalTransactionColumn,
		AmountCentsColumn,
		CurrencyColumn,
		CompletedAtColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Nullable: []string{ExternalTransactionColumn, CompletedAtColumn},
	Omitted:  []querygen.StandardQuery{querygen.CreateQuery, querygen.ExistsQuery, querygen.UpdateQuery},
}

// Transactions is the ledger: what each payment attempt left behind.
//
// Like Purchases it declares no Updatable and omits the standard update, and for
// a stronger reason. Every column here except the status is a fact about a
// movement of money that has already been attempted: the amount, the currency,
// the account, what it was for, and the provider's id for it. A statement able
// to assign those is a statement able to rewrite history, and the one thing that
// legitimately moves — pending becoming succeeded, succeeded becoming refunded —
// is SetTransactionStatus below, which assigns that column and nothing else.
var Transactions = Table{
	Name:     TransactionsTable,
	Singular: "Transaction",
	Plural:   "Transactions",
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		AccountColumn,
		SubscriptionColumn,
		PurchaseColumn,
		ExternalTransactionColumn,
		StatusColumn,
		AmountCentsColumn,
		CurrencyColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Nullable: []string{SubscriptionColumn, PurchaseColumn, ExternalTransactionColumn},
	Omitted:  []querygen.StandardQuery{querygen.CreateQuery, querygen.ExistsQuery, querygen.UpdateQuery},
}

// Emitted is the tables the canonical .sql covers with the standard set, in the
// order they appear in it — all four, which is not the usual case and is worth
// saying rather than leaving to be inferred. Every table in this schema is a
// resource addressed by its own id within a scope, which is exactly the shape
// StandardCRUD emits; what each adds beside it is the keyed statements below.
var Emitted = []*Table{&Products, &Subscriptions, &Purchases, &Transactions}

// Render returns the canonical sqlc input for d: each table's standard queries
// and every keyed statement the store runs beside them, in one file's worth of
// text.
//
// It is what billing/internal/queriesgen writes to the .sql beside this file and
// what CI regenerates to check the committed copy still matches. That .sql is
// sqlc-gen-unison's input, so what the store executes is this text exactly: the
// generated billingdb package carries it per dialect, with the consumer's table
// prefix substituted once at construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// Every table billing owns. StandardCRUD registers what it emits and that
	// happens to be all four here, so this call is currently redundant — and it
	// stays, because the day one of these tables stops taking a standard set is
	// the day a consumer reading the registry back to truncate a database would
	// otherwise miss it.
	querygen.RegisterTable(TableNames...)

	var rendered []*querygen.Query
	for _, table := range Emitted {
		rendered = append(rendered, g.StandardCRUD(table.Name, table.Columns, table.Options()...)...)
	}

	rendered = append(rendered, guardedCreates(g)...)
	rendered = append(rendered, referentChecks(g)...)
	rendered = append(rendered, createdAtReads(g)...)
	rendered = append(rendered, archivedReads(g)...)
	rendered = append(rendered, externalIDReads(g)...)
	rendered = append(rendered, accountReads(g)...)
	rendered = append(rendered, guardedWrites(g)...)

	return querygen.RenderFile(rendered)
}

// guardedCreates is every table's insert, rendered so that a row already holding
// the provider's identifier wins rather than raising.
//
// All four are here rather than in [querygen.Generator.StandardCRUD]'s standard
// set, and the reason is the property the whole schema is shaped around. A
// payment provider redelivers, and the plain create answers a redelivery with
// whatever SQLSTATE the driver raises — so a store built on it has to decide
// beforehand whether the identifier is free, which is a read and a write with a
// gap between them. Two deliveries that cross in that gap both find it free, the
// unique index stops the second row, and the caller gets a driver error where the
// documented answer is billing.ErrTransactionExists — on precisely the delivery
// the sentinel exists for.
//
// [querygen.Generator.InsertIgnoreQuery] closes the gap by putting the decision
// in the statement: the row already there wins unchanged, and the affected-row
// count is how the caller learns it lost. There is no window, because there is
// only one statement.
//
// The conflict target is each table's (scope, external id) unique index, spelled
// exactly as the index is — which is what Postgres requires, and what makes a
// create with no provider behind it insert rather than collide, since all three
// engines treat NULLs in a unique index as distinct.
//
// # What the count does not say, and what the store does about it
//
// A zero count says the row lost and not what it lost to, and on MySQL that is
// broader than it looks: IGNORE downgrades every constraint on the table, a
// foreign key included, so a create naming a product nobody has reports zero
// there where Postgres and SQLite raise. Left alone that would make one mistake
// answer as another, on one dialect only.
//
// So it is not left alone at either end. The creates of the two tables that
// reference a product ask [querygen.ExistsQuery] first, inside the same
// transaction, so a bad product id is ErrProductNotFound before any dialect's
// insert sees it; and the store attributes a zero count by reading, on the losing
// path only, which of the identifiers is taken. Between them the three dialects
// answer every one of these the same way.
func guardedCreates(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}

	return []*querygen.Query{
		g.InsertIgnoreQuery("CreateProduct", ProductsTable,
			Products.InsertColumns(), Products.Nullable,
			scope, querygen.Match{Column: ExternalProductColumn}),

		g.InsertIgnoreQuery("CreateSubscription", SubscriptionsTable,
			Subscriptions.InsertColumns(), Subscriptions.Nullable,
			scope, querygen.Match{Column: ExternalSubscriptionColumn}),

		g.InsertIgnoreQuery("CreatePurchase", PurchasesTable,
			Purchases.InsertColumns(), Purchases.Nullable,
			scope, querygen.Match{Column: ExternalTransactionColumn}),

		g.InsertIgnoreQuery("CreateTransaction", TransactionsTable,
			Transactions.InsertColumns(), Transactions.Nullable,
			scope, querygen.Match{Column: ExternalTransactionColumn}),
	}
}

// referentChecks answer whether the row a ledger write names is there at all,
// archived or not.
//
// A transaction points at the subscription or the purchase it settled, and both
// foreign keys reference the row rather than a live one — archiving a
// subscription deliberately leaves the ledger rows pointing at it alone. So
// these are rendered from the table's columns without archived_at, which is how
// a statement says it must see archived rows, and they project the id alone
// because the question is presence rather than content.
//
// They are read on a losing insert rather than before a winning one. Every
// RecordTransaction would otherwise pay for them, and the answer only matters
// when the insert already wrote nothing: on MySQL, where IGNORE downgrades the
// foreign key to a warning and a zero count, this is what tells a ledger row
// naming a subscription nobody has from the redelivery the zero count usually
// means. Postgres and SQLite raise that case at the insert and never reach here.
func referentChecks(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}

	return []*querygen.Query{
		g.ReadQuery("CheckSubscriptionPresence", SubscriptionsTable,
			Subscriptions.ColumnsExcept(querygen.ArchivedAtColumn),
			querygen.Read{Projection: []string{querygen.IDColumn}}, scope),

		g.ReadQuery("CheckPurchasePresence", PurchasesTable,
			Purchases.ColumnsExcept(querygen.ArchivedAtColumn),
			querygen.Read{Projection: []string{querygen.IDColumn}}, scope),
	}
}

// createdAtReads is the read-back of the one column a create does not carry: the
// creation time the database assigned it.
//
// created_at is database-owned — it is not in any create's column list, and the
// schema gives it a DEFAULT — so the value the caller handed over still holds the
// zero time when the INSERT returns, and the store reads it back inside the same
// transaction.
//
// Each keys on the id alone. The scope is absent because this is not a read a
// caller reaches: it is the create's read-back of the row it has just written, by
// the id it minted for it, and the row is not visible to anything else until the
// transaction commits. The column list is the id and nothing else, which is also
// what leaves the archived predicate off a row that cannot be archived yet.
func createdAtReads(g *querygen.Generator) []*querygen.Query {
	rendered := make([]*querygen.Query, 0, len(Emitted))

	for _, table := range Emitted {
		rendered = append(rendered, g.ReadQuery(
			"Get"+table.Singular+"CreatedAt", table.Name,
			[]string{querygen.IDColumn},
			querygen.Read{Projection: []string{querygen.CreatedAtColumn}},
		))
	}

	return rendered
}

// archivedReads are the four reads an archive answers with: the row it just
// withdrew, on the transaction that withdrew it.
//
// They exist because archiving is the one write in this schema whose result no
// read by id can see. Every keyed read here filters archived_at IS NULL — which
// is what makes an archived product absent from the catalog and an archived
// ledger row absent from a reconciliation — so a store that archived a row and
// read it back through one of those would find nothing. What is left once the
// transaction commits is a page asked for archived rows, and the four
// provider-id lookups, which need an identifier the row may not carry.
//
// Each is rendered from no column list at all, which is the opposite half of
// [Table.UnarchivedBlindColumns]' reason: querygen derives the archived
// predicate from the columns it is handed, so a read that must see archived
// rows is one keyed entirely on its matches. What takes that predicate's place
// is its complement — archived_at IS NOT NULL — so the read-back asserts the
// thing it was called to confirm, and a guard that matched nothing cannot be
// read back as a success.
//
// The scope is bound, unlike [createdAtReads]: this is a read of a row the
// caller named rather than one the store has just minted an id for, and every
// read of a row somebody named binds the column it belongs to.
//
// There is no companion for the two updates or for the completion. None of them
// takes the row out of its table, so the ordinary keyed read reaches all three
// on the transaction that wrote them, and the read-back each makes is that
// statement rather than one of its own.
func archivedReads(g *querygen.Generator) []*querygen.Query {
	var (
		scope    = querygen.Match{Column: ScopeColumn}
		id       = querygen.Match{Column: querygen.IDColumn}
		archived = querygen.Match{Column: querygen.ArchivedAtColumn, Against: querygen.NoValue, Exclude: true}
	)

	rendered := make([]*querygen.Query, 0, len(Emitted))

	for _, table := range Emitted {
		rendered = append(rendered, g.ReadQuery(
			"GetArchived"+table.Singular, table.Name, nil,
			querygen.Read{Projection: table.Columns},
			id, scope, archived,
		))
	}

	return rendered
}

// externalIDReads is the lookup every payment provider webhook begins with:
// the row this event is about, found by the identifier the provider put in it.
//
// Each is rendered from the table's shape without its id, which is how a
// statement says it keys on something else — querygen derives the id predicate
// from the column list it is handed — and without archived_at, which is how it
// says it must see archived rows.
//
// That second omission is not an optimization, it is the whole reason these
// four are the collision checks as well as the reads. Each unique index this
// schema ships covers archived rows deliberately, so a check that skipped them
// would report a provider id free and hand the write to the index — a driver
// error where the caller wanted ErrTransactionExists, on the one obligation
// these tables carry that somebody else enforces on us. The projection is still
// the whole row, so the store decides what an archived hit means: a collision to
// a write, and a not-found to a read.
//
// There is one statement per table rather than one over a union, because a
// purchase and the transaction that settled it can carry the same provider
// identifier and the caller always knows which of the two its event is about.
func externalIDReads(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}

	return []*querygen.Query{
		g.ReadQuery("GetProductByExternalID", ProductsTable, Products.UnarchivedBlindColumns(),
			querygen.Read{Projection: Products.Columns},
			scope, querygen.Match{Column: ExternalProductColumn}),

		g.ReadQuery("GetSubscriptionByExternalID", SubscriptionsTable, Subscriptions.UnarchivedBlindColumns(),
			querygen.Read{Projection: Subscriptions.Columns},
			scope, querygen.Match{Column: ExternalSubscriptionColumn}),

		g.ReadQuery("GetPurchaseByExternalID", PurchasesTable, Purchases.UnarchivedBlindColumns(),
			querygen.Read{Projection: Purchases.Columns},
			scope, querygen.Match{Column: ExternalTransactionColumn}),

		g.ReadQuery("GetTransactionByExternalID", TransactionsTable, Transactions.UnarchivedBlindColumns(),
			querygen.Read{Projection: Transactions.Columns},
			scope, querygen.Match{Column: ExternalTransactionColumn}),
	}
}

// accountReads are the pages a customer's own billing history is made of, plus
// the one read an entitlement check ultimately makes.
//
// The three history pages are the standard list with the account in the key.
// They exist beside the scope-wide lists the standard set already emits because
// those are the administrative reads — every subscription in a deployment — and
// answering "what has this account bought" by paging all of them and filtering
// afterwards is a page whose size the caller cannot rely on.
//
// ListCurrentSubscriptions is the fourth, and it is the read this table's second
// index was built for: the account's subscriptions whose paid period covers a
// bound instant. The comparand is querygen.AtMostArgument inverted — uninverted
// it is the lapsed half, everything at or past the horizon, and Exclude is its
// complement, so "current" and "lapsed" are one predicate with one bool between
// them rather than two spellings that can come to disagree about the boundary.
//
// What it deliberately does not do is filter on the status. Which reported
// status leaves an account entitled is policy — it differs between deployments
// selling the same thing, and capitalism's documentation is where that ruling
// lives — so this statement answers the part that is a fact about the row, and
// the caller reads the status off what comes back.
func accountReads(g *querygen.Generator) []*querygen.Query {
	var (
		scope   = querygen.Match{Column: ScopeColumn}
		account = querygen.Match{Column: AccountColumn}

		current = querygen.Match{
			Column:  PeriodEndColumn,
			Against: querygen.AtMostArgument,
			Arg:     CurrentAsOfArg,
			Exclude: true,
		}
	)

	rendered := g.ListQueries("ListSubscriptionsForAccount", SubscriptionsTable,
		Subscriptions.Columns, scope, account)

	rendered = append(rendered, g.ListQueries("ListCurrentSubscriptions", SubscriptionsTable,
		Subscriptions.Columns, scope, account, current)...)

	rendered = append(rendered, g.ListQueries("ListPurchasesForAccount", PurchasesTable,
		Purchases.Columns, scope, account)...)

	return append(rendered, g.ListQueries("ListTransactionsForAccount", TransactionsTable,
		Transactions.Columns, scope, account)...)
}

// guardedWrites are the three statements that put their own correctness in the
// affected-row count rather than in a read the caller made first.
//
// Every one of them is written from a payment provider's webhook, and every
// payment provider redelivers. Deciding on a read instead leaves a window as
// wide as whatever the caller does next, which for a completed purchase is a
// receipt email and for a refund is a support ticket.
//
// The two status writes guard on the column they assign, under one argument
// name, and that is deliberate — it is the inverted case, where one name is what
// makes the guard say what it means: `SET status = X WHERE status <> X`. A
// replayed event finds the row already holding the status it was going to write
// and touches nothing, which the store reports as the replay it is. This is the
// opposite of the case Match.Arg exists for, where a write assigning the column
// it requires would set it to the value it was requiring, and needs two names to
// stop being a guard that guards nothing.
//
// CompletePurchase guards on absence instead: completed_at IS NULL is what makes
// a purchase complete once, so a second delivery cannot restamp the moment the
// money arrived.
func guardedWrites(g *querygen.Generator) []*querygen.Query {
	var (
		scope       = querygen.Match{Column: ScopeColumn}
		statusMoves = querygen.Match{Column: StatusColumn, Exclude: true}
		notYetPaid  = querygen.Match{Column: CompletedAtColumn, Against: querygen.NoValue}
	)

	return []*querygen.Query{
		g.UpdateQuery("SetSubscriptionStatus", SubscriptionsTable, Subscriptions.Columns,
			[]string{StatusColumn}, Subscriptions.Nullable,
			scope, statusMoves),

		g.UpdateQuery("SetTransactionStatus", TransactionsTable, Transactions.Columns,
			[]string{StatusColumn}, Transactions.Nullable,
			scope, statusMoves),

		g.UpdateQuery("CompletePurchase", PurchasesTable, Purchases.Columns,
			[]string{CompletedAtColumn}, Purchases.Nullable,
			scope, notYetPaid),
	}
}

// FileName is the file one dialect's rendered queries are committed to.
//
// The _generated suffix is in the path rather than only in the header comment,
// because a path is what a reviewer sees in a diff, what CI's glob selects, and
// what a reader scanning this directory reads first — and these are the files
// whose answer to "this line is wrong" is to edit something else.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}

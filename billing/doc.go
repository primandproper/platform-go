/*
Package billing stores what a deployment sells and what its customers paid.

This module has shipped a payments seam for a long time and has never shipped a
place to put the answers. capitalism talks to Stripe and RevenueCat; metering
counts what was consumed; entitlements gates on the result. Between them sat four
tables — a catalog, the recurring agreements, the one-time sales, and the ledger
of attempts — which every consumer wrote by hand, and which are the same four
tables in every one of them. Measured in the consumer this package was written
against, that came to roughly four and a half thousand hand-written non-test
lines, and not one column in them was that application's own.

So this package owns those four, in the same way
[github.com/primandproper/platform-go/v14/identity] owns users: a Store
interface, a SQL implementation of it, the DDL for three dialects, and a mock. A
consumer keeps its checkout flow and every judgement about what a status means;
it does not keep a subscriptions table.

It also no longer keeps the read side of a service over one.
[github.com/primandproper/platform-go/v14/billing/grpc] serves eighteen of these
thirty methods, and the last section here says what crossed, what did not, and
why the checkout flow and the judgement are still in the first list.

# This reverses a ruling, and the reasoning is worth stating

Until this package existed, two documents in this module said the opposite.
capitalism said the mapping from a processor's status onto an account's standing
is policy that lives with the application; entitlements said the join between an
account and a purchased plan is application data that lives in a column next to
the account. Both are still true, and neither was ever an argument about who owns
the table.

The distinction is the same one identity draws. Registration policy is the
consumer's — whether an invitation is required, what a username may look like —
and the users table is not. Here, which of capitalism's eight statuses leaves an
account entitled is the consumer's, and the row recording which status the
processor reported is not. This package stores facts a payment provider owns and
declines to interpret a single one of them: nothing here reads
[Subscription.Status] and decides anything, and there is no column into which a
deployment's idea of "entitled" could be written.

What that buys is the thing entitlements said it could not have. Its PlanSource
seam is filled by [github.com/primandproper/platform-go/v14/billing/plans], which
is this store's current-subscription read plus a function the consumer writes —
so the policy stays exactly where that package put it, and stops being written
against a hand-rolled table.

# Where the boundary with capitalism falls

capitalism is the wire and this is the record, and neither imports the other's
concerns. [capitalism.PaymentManager] takes a
[capitalism.PaymentIntentCreationInput] and hands back a
[capitalism.PaymentIntent]: arguments to a provider call, gone once it returns.
[Purchase] is what the attempt left behind — an account, a product, an amount, a
provider identifier held as a foreign key rather than as its identity, read back
by the application afterwards. The two overlap in vocabulary and in nothing else.

One type does cross: [Subscription.Status] is [capitalism.SubscriptionStatus],
not a set of this package's own. That is deliberate. capitalism's is already the
closed, documented set every adapter maps its provider's words onto, and a second
enumeration here would be the same judgement — which of Stripe's words is
"cancelled" — made twice, in two places that could disagree. The one status this
package will not store is capitalism's unknown: a provider said something no
adapter could place is a fact worth keeping in a variable, and not one worth
writing into the column entitlement decisions are read from.

# Redelivery is the property this schema is shaped around

Every write here that a payment provider triggers can arrive twice, because every
payment provider redelivers. Three mechanisms answer that, and all three are in
the statement rather than in whatever the caller does next:

The three provider-identifier columns are unique within a scope, so a second
delivery of a charge collides instead of recording it twice — which is the
difference between a ledger somebody can sum and a number somebody reconciles by
hand. Every create is an insert-ignore over that index rather than a plain insert:
the row already there wins unchanged and the affected count is how the caller
learns it lost, so nothing decides the identifier is free in a statement before
the one that uses it. The store reports the loss as [ErrTransactionExists] and its
siblings, so a handler acknowledges the delivery rather than retrying it forever.

A zero count says the row lost and not what it lost to, so the store reads once
more to attribute it, on the losing path and therefore never on the hot one. That
read is also what keeps the three engines saying the same thing: MySQL's IGNORE
covers every constraint on the table rather than the one index, so a create naming
a product nobody has arrives there as a zero count where Postgres and SQLite raise
a foreign key. The two creates that reference a product ask for it before they
insert, and the ledger's create asks about its referents when it loses. See
[ErrIDTaken] for the one case the three engines report differently, and why
nothing but a caller's own bug reaches it.

The two status writes are guarded on the column they assign — `SET status = X
WHERE status <> X` — so a redelivered event touches nothing and is told
[ErrStatusUnchanged], which is an answer rather than a failure.

[PurchaseStore.CompletePurchase] is guarded on completed_at being NULL, so a
purchase completes exactly once and a second delivery cannot restamp the moment
the money arrived.

Each of the three columns is also nullable, and that is what makes the uniqueness
usable rather than an obstacle. A free tier, a comped plan and a subscription
granted by hand have no provider-side counterpart at all, and all three engines
treat NULLs in a unique index as distinct — so the rows that have a provider id
are unique and the rows that do not stay out of each other's way. See
billing/migrations.

# The transaction is the caller's

Every write here takes a database.Tx and every read takes the wider
database.SQLQueryExecutor. There is no form of any write that opens a
transaction of its own, and the type is what says so — only
database.RunInTransaction produces a Tx, so the obligation is the compiler's
rather than a doc comment's. A consumer with nothing to join writes
client.WithTransaction(ctx, func(tx database.Tx) error { ... }) and passes what
it is handed.

The reason is that a payment provider's event is rarely one row. An audit entry
naming who was billed and a data change event on an outbox somebody fans out are
the ordinary companions, and a companion written after this store's own
transaction had committed was one that could go missing while the row stayed.
The gap was narrow and one-directional — a subscription with no event, never an
event naming a subscription that was not written — and nothing outside this
package could close it.

It is sharper here than in the packages that made the same argument first,
because of the section above. The store already makes a replayed webhook collide
rather than record twice; a consumer wants that same property for what it records
*about* the write, and a first delivery whose audit entry the database refuses
must not leave a subscription row with no provenance and an entitlement check
reading it. A subscription status change nobody can attribute is the expensive
kind of missing record, in the one domain where attribution is the point.

Every read a write depends on runs on the caller's executor too, and that is the
reason the reads take the wider type rather than a Tx. The product check gating
[Store.CreateSubscription] and [Store.CreatePurchase] runs there, so a product
stocked and subscribed to in one transaction is visible to the check that would
otherwise refuse the subscription. The attribution read the insert-ignore makes
on the losing path runs there, so a redelivery arriving in the same transaction
as the row it collides with is named by a snapshot that can see that row rather
than mis-blamed by one that cannot. The guarded writes' refusals —
[ErrStatusUnchanged], [ErrAlreadyCompleted] — read through the same executor for
the same reason. And a consumer holding no transaction at all passes
Client.Reader() to any of the reads, which is what an entitlement check does.

One thing does not follow the transaction. The ledger's instrument is incremented
when the statement writes the row rather than when the caller commits, because
nothing here can observe somebody else's commit. A caller that rolls back leaves
a count with no row behind it; the alternative was to leave the write uncounted,
which would quietly remove the one instrument a payment integration's health is
read from. [Store.RecordTransaction] says so.

The two consumers in this module take their executor at construction, because
neither seam they implement hands one over: billing/plans is an
entitlements.PlanSource, whose PlanFor takes an account and nothing else, and
billing/privacy is a dataprivacy.Collector, whose Collect takes a subject.

# A write answers with the row it wrote

Every write here bar the two status moves returns the row: the four creates, the
two updates, the completion and the four archives. Each reads it back on the
caller's own transaction after the statement, so what a consumer's audit entry,
receipt or outbox event describes is what the database holds rather than what the
caller assembled and hoped for.

The archives are the case with no alternative. Every keyed read over these tables
filters archived_at IS NULL, which is what makes a withdrawn product absent from
a catalog and a retired ledger row absent from a reconciliation, so once the
transaction commits the row an archive moved is reachable only by paging for
archived rows and picking it out. The read-back is a statement of its own for
them, carrying the complement — archived_at IS NOT NULL — so it describes the
row this call moved rather than one that was already gone.

It is a second statement rather than RETURNING because MySQL has none and the
corpus is one text per dialect rendered from one column list. There is no gap
between the two: the guarded write holds the row until the caller commits, and
the read runs on the same transaction.

[SubscriptionStore.SetSubscriptionStatus] and
[TransactionStore.SetTransactionStatus] are the two exceptions, and the boundary
is worth stating because it is what keeps "returns the row" from meaning "every
write pays for a read". Each assigns one fact the caller already holds — the
status came in on the provider's event — to a row that stays in its table, which
[SubscriptionStore.GetSubscription] and [TransactionStore.GetTransaction] still
reach on the transaction that wrote it.

# A price is a fact about a moment, not a lookup

[Purchase] and [Transaction] each carry their own amount and currency rather than
reading them through the product. Repricing a product must not rewrite what
somebody already paid, and a partial refund is a transaction whose amount was
never any product's price. A schema that joined to find an amount could hold
neither.

The same reasoning is why there is no statement able to assign an amount. The
only thing that legitimately changes about a ledger row is its status, and the
only thing that changes about a purchase is whether the money arrived.

# Getting the tables

The DDL lives in [github.com/primandproper/platform-go/v14/billing/migrations],
rendered per dialect and table prefix, and hands to database/migrate's
WithGeneratedMigration so nothing is copied into a consumer's repository. See that
package for why no numbered migration file ships.

That package also answers which tables exist, at your prefix, through its Tables
function — the list is complete and read from the DDL, so a between-tests
TRUNCATE, a backup policy or a privacy inventory names every one of them without
anybody copying four names out of the schema.

# What a consumer still writes

The service layer, the checkout flow, and every judgement about what a status
means.

Concretely: the handler that creates a payment intent through capitalism and then
writes the [Purchase] it will settle into; the webhook endpoint that verifies a
signature through webhooks/inbound, maps the payload through a capitalism adapter,
and calls one method here; the function billing/plans takes, saying which
statuses leave an account entitled; and the mapping onto identity.BillingStatus,
which is the coarse standing an application gates on and includes a suspension no
processor reports.

# Subject access, and the erasure that is deliberately absent

[github.com/primandproper/platform-go/v14/billing/privacy] is a
dataprivacy.Collector and no Eraser. Financial records carry a statutory retention
that outranks a right to erasure in every jurisdiction that grants both, so an
Eraser here would be a seam whose only correct implementation erases nothing.
That package states the ruling in full.

# There is a transport now, and it does not take the bargain back

[github.com/primandproper/platform-go/v14/billing/grpc] serves eighteen of this
store's thirty methods over gRPC, with billing.proto shipped inside the module and
a typed client beside it. The line the module draws between what it stores and
what it serves has moved for this package, and the README states where it now
falls, under "Stores and Transports".

What the bargain above said a consumer keeps, they still keep. The checkout flow
is not on the wire and cannot be: seven writes here have a caller who is a
processor callback or the handler that created a payment intent, already holding
the transaction that is writing an audit entry and an outbox event beside the
billing row, and an RPC would move the write out of the transaction that was the
point of it. Nor is the judgement: there is no RPC and no field on any message in
that schema that says whether an account is entitled, because that reading is the
consumer's and billing/plans is the seam it already lives in. The surface hands
back [Subscription.Status] and stops, exactly as this package does.

What a consumer no longer writes is the eighteen: a catalog screen, "my
subscriptions", "my purchases", "my invoices", an operator's page over each, and
the conversions and permission names underneath all of it. billing/grpc's own
documentation carries the ruling on each absence, including the one on the
GetXByExternalID reads.
*/
package billing

//go:generate go run ./internal/queriesgen

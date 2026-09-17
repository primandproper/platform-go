/*
Package metering counts what customers consume and enforces what they are allowed
to, durably enough to invoice from.

The capitalism package can charge. It cannot count. Metering is the missing half,
and the hard parts are all guarantees rather than opinions: counting without
losing events under concurrency, enforcing a limit cheaply on the read path, and
pushing usage to a billing provider so that a retry is not a second charge. None
of those are application decisions, and every SaaS hand-rolls all three.

# Why this is not ratelimiting

The ratelimiting package answers "how fast?" over a sliding window and then
forgets, which is the correct design for its question — a rate limiter that
remembered last Tuesday would be enforcing something nobody asked for.

Metering answers "how much, this billing period?" and must never forget, because
the answer becomes an invoice. Same shape, opposite durability requirement. A
plan will typically want both, configured independently: a hundred requests a
second and a million requests a month are different limits that fail at different
times for different reasons.

# The shape

	registry := metering.NewRegistry()

	if err := registry.RegisterMeter(metering.Meter{
	    Name:        "llm_tokens",
	    Unit:        "tokens",
	    Aggregation: metering.AggregationSum,
	    Period:      metering.PeriodMonth,
	}); err != nil {
	    return err
	}

	if err := registry.RegisterQuota(metering.Quota{
	    Meter:    "llm_tokens",
	    Limit:    5_000_000,
	    Behavior: metering.BehaviorAllowOverage,
	    Period:   metering.PeriodMonth,
	}); err != nil {
	    return err
	}

Then three components over one Store, each wanted by a different process:

A Recorder ingests usage. It is the write path, says nothing about limits, and is
all a deployment that meters for billing but enforces nothing ever needs.

An Enforcer answers quota questions, on the request path.

A Flusher posts accumulated usage to the billing provider, on a jobs.Scheduler
tick in a worker.

# Every consumer call names a tenant

Usage belongs to somebody, and that somebody is a
[github.com/primandproper/primitives-go/v2/tenancy.Scope] on every consumer-facing
method here. Both tables carry it in a column, leading both natural keys: two
tenants counting the same meter in the same window hold two totals and produce two
invoice lines, and one tenant's idempotency key cannot dedupe another tenant's
usage away. It is an argument rather than a field on [Usage], because a scope read
off a record the caller assembled somewhere else makes "whose usage is this" a
question answerable only by reading that record — which is the derivation the
column exists to rule out.

An application with one tenant passes [github.com/primandproper/primitives-go/v2/tenancy.Global]
everywhere and gets exactly the behavior it had before the column existed, because
the global scope is stored as the empty identifier. What it does not get is a
default: the column carries none, and an unset scope is refused rather than filed
under the global one — see [Store].

There is no unscoped read. What crosses every scope is this package's own
machinery and nothing a consumer calls: [Store.ClaimFlushable] drains the flush
backlog for every tenant at once, [Store.ReapEvents] draws one retention horizon
across all of them, and [Flusher] is the worker on a timer that calls both. A
per-tenant flusher would be a worker nobody could schedule without first
enumerating the tenants. The two settlements the flusher reaches through —
[Store.MarkFlushed] and [Store.ReleaseFlush] — take no scope either, and bind the
one on the [Total] the claim handed them: the row this flusher holds, rather than
a tenant a caller named.

# The transaction is the caller's

Every write a consumer calls takes a
[github.com/primandproper/primitives-go/v2/database.Tx], and that is the point of
the package rather than a tax it charges:

	err := client.WithTransaction(ctx, func(tx database.Tx) error {
	    if err := saveTheThing(ctx, tx, thing); err != nil {
	        return err
	    }

	    return recorder.Record(ctx, tx, scope, metering.Usage{
	        Subject:        accountID,
	        Meter:          "stored_bytes",
	        Quantity:       thing.Size,
	        IdempotencyKey: thing.ID,
	    })
	})

The work and the usage it incurred commit together or not at all. Recorded in a
transaction of the store's own, they do not: a crash between the two leaves usage
counted for work that rolled back, or work committed that nobody was billed for,
and no ordering of two commits avoids both. A caller with genuinely nothing to
join — an ingest endpoint whose only job is to record usage — writes the same
[github.com/primandproper/primitives-go/v2/database.Client.WithTransaction] call
with one statement inside it.

[Enforcer.Consume] takes one for the stronger version of the same reason: a nil
error from it is permission to do the work, and permission that commits
separately from the work is permission to do work nobody was charged for. The
lock it holds lives as long as that transaction, which is what makes the decision
a reservation.

[Enforcer.Check] takes none. It writes nothing, so there is nothing for a
transaction to hold; the executor its durable fallback reads on is settled once,
at [NewQuotaEnforcer].

The store's own machinery — the flush claim, its two settlements, and the event
reaper — takes no transaction either, and [Store] says why on each. In short:
they run from a scheduler tick rather than a request, and the flush protocol
depends on the claim being committed before the provider is posted to, which a
caller holding the transaction open across that round trip would break.

# Idempotent ingest

Usage.IdempotencyKey is required, not optional. Every ingest path this package has
can present the same usage twice — an HTTP client that retries on a timeout, a
queue that redelivers on a lost ack — and usage that can be double-counted is
usage that produces wrong invoices.

Choosing one: it must be stable across the retries of a single logical event and
distinct across different events. The identifier of the thing that caused the
usage is almost always right — a request ID, a completion ID, a message ID —
because it is stable by construction and the caller already has it. A timestamp,
a random value generated at the call site, or a hash of the quantity are all
wrong, in the three different ways that are each obvious in hindsight.

Dedupe is the primary key of the event ledger table, and this is a considered
departure from the issue that specified the package, which proposed reusing the
idempotency package. That package is cache-backed with a TTL measured in hours,
and it is the right tool for its own problem: making an HTTP handler safe to
retry, where the window is a client's retry budget. Here the window is a billing
period. A dedupe that expired after a day would let a queue's dead-letter
redelivery, or a batch replayed by hand after a bad deploy, be counted a second
time — three weeks after the fact, into a total nobody has invoiced yet. So the
mechanism is one the database enforces, for as long as the row is retained, which
FlusherConfig.EventRetention defaults to ninety days.

# Durable counting

Every accepted record is written to the event ledger and folded into a period
total in the same transaction. The fold happens in the UPDATE statement rather
than in a read-modify-write, so two recorders folding into the same period at the
same instant cannot lose one of the two.

Note what the cache is and is not. The issue that specified this package proposed
buffering increments in the cache and reconciling them into the durable store on a
cron tick, with the stated requirement that "losing a Redis instance must cost
accuracy for seconds, not money". Buffered increments cannot meet that
requirement — they make the cache authoritative for everything since the last
reconciliation, so losing it loses exactly the usage that had not yet been
reconciled, which is money. So the direction is reversed: the durable total is
always the source of truth, and the cache is a read-through copy of it with a TTL.
Losing the cache costs latency until it repopulates and nothing else.

# Check is fast and slightly stale; Consume is exact

	// on a cheap path — a cached read, no write, no transaction
	decision, err := enforcer.Check(ctx, scope, accountID, "api_requests", 1)

	// on an expensive one — locks the total, decides, and records the usage in
	// the same transaction the work it authorizes is written in
	err := client.WithTransaction(ctx, func(tx database.Tx) error {
	    decision, err := enforcer.ConsumeUsage(ctx, tx, scope, metering.Usage{
	        Subject:        accountID,
	        Meter:          "llm_tokens",
	        Quantity:       tokens,
	        IdempotencyKey: completionID,
	    })
	    if err != nil {
	        return err
	    }

	    if !decision.Allowed {
	        return errOverQuota
	    }

	    return doTheWork(ctx, tx)
	})

Two methods rather than one, because gating a cheap read on a durable write is how
metering becomes the latency bottleneck of the system it was added to measure.
Callers pick per call site.

Check's staleness is bounded by the cache TTL, which is Meter.Staleness or
EnforcerConfig.Staleness — ten seconds by default. The worst case is explicit:
a subject sitting exactly at their limit can consume, unenforced, for up to the
staleness budget, which is bounded by whatever one subject can push through in ten
seconds. For an API request quota that is a rounding error. For a meter whose unit
is worth real money, set Staleness lower, or use Consume, which has no staleness
at all.

Consume's signature on the Enforcer interface generates its own idempotency key,
which makes a retried Consume count twice. That is unavoidable — the signature has
no key to be stable across retries — so any path that can retry should use
ConsumeUsage instead.

# Overage is a behavior, not an error

	metering.BehaviorBlock         // refuse past the limit
	metering.BehaviorWarn          // allow, record, and report the overage
	metering.BehaviorAllowOverage  // allow, record, and consider it normal

BehaviorAllowOverage is how most usage billing actually works: a limit is where
the price changes, not where the service stops. Decision.Overage carries the
excess, which is the quantity an overage price is applied to.

# Idempotent flush

	scheduler.Register(flusher.Job(jobs.MustCron("0,5,10,15,20,25,30,35,40,45,50,55 * * * *"), 10*time.Minute))

This is the single most expensive thing in the package to get wrong, so four
mechanisms hold it together.

Each post carries the delta since the last successful post, not the running total.
Providers aggregate the records inside a billing period, so posting a cumulative
total every five minutes for a month would invoice roughly nine thousand times the
right number.

Each post's idempotency key is derived from (subject, meter, period, sequence),
where the sequence is a counter stored beside the total and incremented only on a
successful post. A retry of the same post computes the same key and the provider
ignores it; the next genuine post computes a different one. FlushIdempotencyKey is
exported so an operator reconciling an invoice by hand can compute what a given
post's key would have been.

The amount a key stands for is pinned when the total is claimed, not recomputed
per attempt. Since the provider keeps the first amount it accepted under a key, a
retry that measured its delta from the running quantity would post a larger
figure after usage arrived in between, have it discarded as a duplicate, and then
settle the row past the difference — which is billed to nobody and reported by
nothing. Total.ClaimedQuantity is that pin: the claim snapshots it, the settle
advances the flushed quantity to it and no further, and whatever accumulated
meanwhile goes out under the next sequence.

The settle is guarded on the sequence the flusher read. A flusher whose lease
lapsed mid-post cannot advance a sequence another flusher has already moved,
which is the one race that would put the same delta on the wire under two
different keys — and the one an idempotency key cannot undo after the fact.

# Plans are not modeled here

Which plan a subject is on, what it entitles them to, and when it changed are
questions the billing provider's product catalog already answers. Modeling them
here would duplicate that catalog in a second place that can disagree with it, so
the package models none of them and asks instead: a QuotaSource maps a subject to
a limit, and a ProviderMapper maps a subject and meter to the provider-side handle
usage posts against. Both are one-method interfaces with function adapters.

The default QuotaSource serves the Registry's static quotas to every subject,
which is right for a deployment with one set of limits and wrong the moment two
customers can buy different amounts.

# The rung between those two

Between "one set of limits for everybody" and "write a QuotaSource from scratch"
there is a resolution ladder every subscription business implements identically,
and NewPlanLimitSource is it:

	source, err := metering.NewPlanLimitSource(registry, map[string]metering.PlanLimits{
	    "llm_tokens": {
	        ByProduct:    map[string]int64{"prod_pro": 5_000_000, "prod_team": 50_000_000},
	        Unsubscribed: 100_000,
	        Behavior:     metering.BehaviorAllowOverage,
	    },
	}, subscriptions)

where subscriptions is an EntitlementReader — one method, answering which
product entitles a subject right now. The application supplies the numbers and
the subscription lookup, both of which are its own; the library supplies the
order they resolve in:

	meter absent from the table  → unlimited, without reading anything
	subject entitled to product  → the limit for that product
	subject not entitled         → PlanLimits.Unsubscribed

and the three-way distinction that order depends on. Unlimited is
metering.Unlimited paired with BehaviorAllowOverage — a limit nobody reaches, not
a value the enforcer special-cases. Unmetered is a meter with no quota at all,
which is ErrNoQuota and not a synonym for unlimited. Zero is no usage allowed,
which is a real configuration for a feature switched off on a tier. Getting the
three confused is either a customer blocked who should not be or a limit that
silently never applies, and neither announces itself.

It is still not a plan catalog: no products, no prices, no notion of what a
subscription is beyond "there is one, and it names this". An application that
wants a catalog — features that are not meters, one Check that answers "may this
account use this at all" — wants the entitlements package, which holds one and
serves metering a QuotaSource off it.

# Dimensions describe; they do not enforce

Usage.Dimensions — model, region, endpoint — are stored against the event for
later analysis and are deliberately absent from the aggregate key and from
enforcement. A dimensioned quota needs a total per combination of dimension
values, so the number of rows to keep becomes the product of every dimension's
cardinality, and a dimension whose values come from user input has no bound. A
caller that needs per-model limits registers a meter per model, where the
cardinality is a decision somebody made on purpose.

# There is deliberately no privacy adapter

Ten packages in this module ship a privacy subpackage — a dataprivacy.Collector,
and where deletion is right a dataprivacy.Eraser, registered at the composition
root so a subject access request fans out over them. This package ships neither,
and the omission is the ruling rather than an unfinished job.

Usage.Subject is the account or tenant being billed. It is not a person, and
nothing here maps one to the other: the field's own documentation says what it
is, the totals table's primary key leads with it, and a deployment that bills
individuals has an account per individual rather than a second vocabulary this
package knows about.

Both tables are the substrate of an invoice. The events table is the evidence
behind a disputed one — which is why the dedupe is a primary key held for a
billing period rather than a cache — and the totals table is what was actually
posted to the provider, sequence and all. billing/privacy's ruling applies to
both without modification: every jurisdiction that grants a right to erasure also
requires financial records kept, the retention obligation wins, and an Eraser
here would be a seam whose only correct implementation erases nothing. Shipping
one would invite a deployment to register it and believe its usage history had
been deleted.

The right of access is not the right of erasure, and the access half is answered
where the facts are legible. What a subject is entitled to see is what they were
charged for, which is billing/privacy's export — a subscription, a purchase and a
ledger row, in a vocabulary somebody can read. A quantity folded into a period on
a meter named in a registry is the arithmetic behind that line rather than a
second answer to the same question, and neither table carries the id a cursor
walks, so the read such a collector would need is one this schema does not offer.

Usage.Dimensions is the one place a deployment can put a person into these
tables, and it is why the observability keys above name every field of an event
except that one. The field is application-chosen keys and values, and a
deployment that dimensions by email address has put personal data somewhere this
package cannot enumerate it, cannot index it, and does not claim to erase it. The
answer is to not do that — a dimension whose values come from user input has no
bound, which is already the reason it cannot be a quota key.

A deployment whose counsel reads any of this differently deletes the rows through
its own migration. It is a decision somebody has made, and this package declines
to make it for them.

# What is not implemented

AggregationUniqueCount — monthly active users, distinct seats — is named and
refused at registration. Every other aggregation folds a record into a single
integer as it arrives; that one has to remember which values it has already seen,
which is a set or a HyperLogLog per period rather than a column. Treating it as a
sum would look right on a dashboard and be wrong on an invoice, so it fails at
wiring time instead.

# Storage

The store is SQL, and this package ships the DDL for it (metering/migrations) for
Postgres, MySQL, and SQLite. Two tables: an append-only event ledger keyed by
(scope, meter, idempotency key), and a totals table keyed by (scope, subject,
meter, period start). Both keys lead with the scope; see above.

The library owns the schema because the counting logic is inseparable from it —
the dedupe is a primary key, the concurrent fold is an UPDATE expression, and the
atomicity of Consume is a row lock. A repository interface over those would be an
interface over three SQL features, implemented once. Store is still an interface,
for an application whose schema conventions differ enough to be worth
reimplementing it against — which is why its methods name SQL types even though
nothing forces a backing store to be SQL. An implementation with no transaction
of its own ignores the executor it is handed; the alternative is one signature
per backend, which costs every caller the guarantee the type is there to give.

None of that SQL is written here. The statements are described as data in
metering/internal/queries, rendered from there into one canonical .sql per
dialect, checked against this package's own DDL by sqlc, and executed through
the querier sqlc-gen-unison generates from them. A column renamed in the
migrations is then a failed `make unison` with no database running, rather than
a runtime error on whichever of the three servers a deployment happens to run.

# Event time on SQLite

Postgres and MySQL store an event time to the microsecond. SQLite has no date
type — a DATETIME column holds text, and a comparison between two of them
compares two strings — so a bound time is written in the shape SQLite's own
CURRENT_TIMESTAMP writes, which is whole seconds. Every instant this store binds
is stored truncated down on that engine, and so is every instant it is compared
against.

Sum and Max do not notice: their arithmetic is over quantities, and the event
time only decides how far forward last_occurred_at moves. AggregationLast is the
one that does, because the event time is what decides which reading the period
keeps. Two records inside one second are indistinguishable there, and the rule
is that the first one folded in is the one that stands: a later arrival stamped
inside a second already recorded — an at-least-once queue redelivering behind
the reading that superseded it — leaves the total where it is rather than
dragging it back. What it costs is the mirror of that, and it is bounded by the
same second: where two genuine readings fall inside one second, the one folded
in first is the one the period keeps until the next record arrives. A gauge
sampled more than once a second is a reason to reach for Postgres or MySQL
rather than SQLite.
*/
package metering

//go:generate go run ./internal/queriesgen

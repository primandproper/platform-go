/*
Package queries is metering's two tables described as data: their canonical
names, their columns in the order every read projects them, the subsets each
write assigns, and the natural key every statement addresses a row by.

It exists because those facts have two consumers that must not disagree. The
generator behind `make unison` renders them through database/querygen into the
canonical .sql files sqlc is run over; the generated querier beside them is what
the store executes. A column list spelled in both places could differ in one
name, and the symptom would be a check that passes over SQL nobody executes.

So it is spelled once, here. The .sql files beside this one are the generator's
output — see [Render] and metering/internal/queriesgen.

# The two tables, and why the split is load-bearing

metering_events is the ingest ledger: one row per (scope, meter,
idempotency_key), written once and never updated. It is what makes counting exactly-once, and it
is the evidence behind an invoice when somebody disputes one.

metering_totals is the aggregate the read path and the flusher use. It is small
— one row per scope, subject, meter, and period — and it is the only thing Consume
locks. Deriving it from the ledger on every read would be a group-by over a
table that grows with traffic, on the path this package exists to keep cheap.

Both are keyed on a natural key and neither carries an id. The ledger's is
(scope, meter, idempotency_key) rather than the key alone, because callers are
told to use a request ID and one request routinely feeds several meters — keyed
on the key alone the second meter's insert is silently deduped against the first,
and that customer is under-billed forever. The totals table's is (scope, subject,
meter, period_start), which is the row a period's usage accumulates in.

The scope leads both, and it is a key component rather than a filter laid over
one. Two tenants' request IDs come from two sequences nobody reconciled, so a
ledger key without it dedupes one tenant's usage against another's; and two
tenants counting one meter in one window are two invoice lines, so a totals key
without it folds them into a row neither can be billed from.

Neither shape needs anything querygen did not already have. A single-row
statement's id predicate is rendered from the column list it is handed — so a
list with no id renders none — and the key goes in [querygen.Match] values
instead, which is what Match has always been for. The conflict targets name the
same columns, because Postgres matches ON CONFLICT against a unique index the
table actually has and both of these are the primary key.

# The fourteen statements

	InsertMeteringEvent            the ingest write, and the dedupe
	MeteringEventExists            the read-only dedupe probe
	PruneMeteringEvents            the retention pass

	InsertMeteringTotal            opens a period's total
	GetMeteringTotal               reads one
	GetMeteringTotalForUpdate      reads one and holds it
	FoldMeteringTotalSum           the additive fold
	FoldMeteringTotalMax           the peak
	FoldMeteringTotalLast          the most recent reading
	ApplyMeteringConsume           the decision Consume made under the lock
	SelectFlushableMeteringTotals  the flush claim's read
	ClaimMeteringTotal             one total's lease
	MarkMeteringTotalFlushed       a settled post
	ReleaseMeteringFlush           a failed one

Five of them are querygen's own shapes; the rest are written out here, and the
line between the two halves is the one database/querygen's own doc draws.
querygen assigns bound values, and these do not: the folds add, maximize, and
choose between two columns with a CASE; the claim increments an attempt counter
server-side; the settle advances a sequence by one and clears an error to a
literal; the flushable read compares two columns to each other, which no
argument can express and no index can serve. Rendering those would need an
expression language in querygen, which is what its closed comparand set exists
to refuse.

What they do not give up is the tier. Each is a complete statement in the
committed corpus, checked by sqlc against metering's own schema on all three
dialects, executed through the generated querier — so a renamed column is a
failed `make unison` for these exactly as it is for the get.

# What replaced the upsert

The fold used to be one statement: an INSERT whose conflict branch did the
arithmetic. It said the same thing twice — once in the VALUES list for the row
that was not there, once in the conflict branch for the row that was — in two
spellings the three dialects spell six ways between them, and it was the one
statement in this package whose text depended on a value, since the conflict
branch's quantity assignment was chosen per call from the meter's aggregation.

It is now a seed and a fold. [InsertTotalQuery] writes a zero row and skips one
already there; the fold that follows is an UPDATE the server evaluates against
whatever the row holds. The arithmetic is still the server's, which is the whole
of what made the upsert safe: two recorders folding into one period cannot both
read the same total, both add their own quantity, and between them lose one.

Three things came out of that. The aggregation chooses between three named
statements rather than deciding one line of one statement's text, so sqlc checks
all three and an aggregation with no fold is a missing case at the call site
rather than a write that leaves the total where it found it. The seed is the
same statement Consume already ran to have a row to lock, so the two paths that
write a total open it the same way instead of two ways that stamped
last_updated_at differently. And the conflict branch's dialect spellings —
EXCLUDED against VALUES(), ON CONFLICT against ON DUPLICATE KEY — are gone from
this package entirely.

# Which columns each statement is handed

[TotalColumns] is the whole totals row and [TotalProjection] is what every read
of it projects. The gap between them is four columns and each absence is a
decision the projection's own comment gives; the two that matter most are
archived_at, whose absence is how these statements say they render no archived
predicate, and last_updated_at, whose absence is how they say they render no
server-clock stamp. A total's timeline is one clock's, and it is the clock the
flusher schedules against.

[EventColumns] is the ledger row entire, because the ledger row is written once
and read back whole. Its recorded_at is the caller's like every other time here,
so the horizon a retention pass runs to and the deadline a flush runs to are
measured by one clock.

# The claim, and why it is one row at a time

The flush claim used to be three statements over a batch: select the keys, lease
them all in one UPDATE whose predicate was a row-value IN list, and read the
leased rows back. None of that can be checked. A row-value IN list over a
three-column key has no static arity for sqlc to read, because its shape is the
caller's cardinality — and the re-read that followed had no guard on it at all,
so a total another flusher settled in between came back as one this flusher
held.

So the read projects whole rows and the lease is keyed per row: one statement
per total, inside the transaction the read already opened, each reporting
whether it took. A total that stopped owing the provider anything between the
read and the lease matches nothing and drops out of the batch, which is the
answer the re-read was reaching for and could not give. The attempt count a
flusher sees is the one it read plus the one this lease just added, which is
exact because the lease matched and the row is held.

A statement per row is what this package already does on ingest, for the reason
[InsertEventQuery] gives: a batch that cannot say which of its rows were new is
a batch that guesses, and guessing is how usage gets counted twice.
*/
package queries

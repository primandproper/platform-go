package queries

import (
	"fmt"
	"strings"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// The two tables metering owns, at their canonical spelling — what the emitted
// .sql names, and what metering/migrations renders at the consumer's prefix.
const (
	// EventsTable is the ingest ledger: one row per (meter, idempotency key),
	// written once and never updated.
	EventsTable = "metering_events"
	// TotalsTable is the aggregate, one row per (subject, meter, period).
	TotalsTable = "metering_totals"
)

// TableNames is every table metering owns, in the order the DDL creates them.
//
// metering/migrations is where a consumer gets these names rendered at their
// own prefix. This list is the canonical spelling and migrations.Tables reads
// the DDL, so the two are cross-checked against each other in this package's
// tests rather than one being derived from the other.
var TableNames = []string{EventsTable, TotalsTable}

// The columns the statements below name, spelled once each.
//
// They are constants because most of them appear in several statements — the
// period start in a key, a projection and an insert; the quantity in three
// folds and a comparison against the flushed quantity — and a typo in one of
// those renders SQL sqlc rejects, which is the good case, or names a different
// column, which is not.
const (
	// IdempotencyKeyColumn is the caller's key for one usage record, and the
	// second half of the events table's natural key.
	IdempotencyKeyColumn = "idempotency_key"
	// SubjectColumn is whose usage a row records.
	SubjectColumn = "subject"
	// MeterColumn is what is being counted. It is the first half of the events
	// table's natural key, because one request routinely feeds several meters
	// under one idempotency key.
	MeterColumn = "meter"
	// QuantityColumn is the usage: one record's on an event row, the period's
	// folded total on a totals row.
	QuantityColumn = "quantity"
	// OccurredAtColumn is when the usage happened, by the caller's clock.
	OccurredAtColumn = "occurred_at"
	// RecordedAtColumn is when the ledger row was written, and the column the
	// retention pass draws its horizon against.
	RecordedAtColumn = "recorded_at"
	// PeriodStartColumn is the window the usage falls in, and the third
	// component of the totals table's natural key.
	PeriodStartColumn = "period_start"
	// DimensionsColumn is the record's encoded dimensions, or NULL.
	DimensionsColumn = "dimensions"

	// PeriodEndColumn closes the half-open window PeriodStartColumn opens.
	PeriodEndColumn = "period_end"
	// AggregationColumn is how the total was folded, stored beside it so a
	// flusher reading the row does not need the meter catalog to read it.
	AggregationColumn = "aggregation"
	// LastOccurredAtColumn is the event time of the newest record folded in. It
	// only ever moves forward — see [Render].
	LastOccurredAtColumn = "last_occurred_at"
	// FlushedQuantityColumn is how much of the quantity the provider has been
	// told about.
	FlushedQuantityColumn = "flushed_quantity"
	// FlushSequenceColumn counts successful posts, and is the guard every
	// settle carries.
	FlushSequenceColumn = "flush_sequence"
	// FlushAttemptsColumn counts claims rather than failures, so a total whose
	// post reliably kills its flusher eventually gives up.
	FlushAttemptsColumn = "flush_attempts"
	// NextFlushColumn is when the total may next be posted.
	NextFlushColumn = "next_flush"
	// ClaimedUntilColumn is the lease, and NULL whenever nobody holds one.
	ClaimedUntilColumn = "claimed_until"
	// LastErrorColumn is why the last post failed, rendered.
	LastErrorColumn = "last_error"
)

// The arguments the statements below bind beyond the ones named for their own
// column.
const (
	// HorizonArg is the instant the retention pass draws its line at. It is
	// named rather than defaulted to the column, because "recorded_at" reads as
	// the value in a row and this is the line the pass is drawn at.
	HorizonArg = "horizon"

	// DueAtArg is the instant a total's next flush must have arrived by.
	DueAtArg = "due_at"
	// LeaseExpiredByArg is the instant a lease must have lapsed by for the row
	// to be claimable again. The store binds the same time as DueAtArg — one
	// pass asks one question about one moment — and they are two arguments
	// because they are two comparisons against two columns, and a reader of the
	// statement should not have to work out that they must agree.
	LeaseExpiredByArg = "lease_expired_by"
	// MaxAttemptsArg is the attempt budget a total is refused a claim past.
	MaxAttemptsArg = "max_attempts"
)

// EventColumns is the ledger row's shape, in the order the DDL declares it and
// the order the insert supplies values for.
//
// Every column is supplied by the caller, including recorded_at: the ingest
// clock is the one the rest of this package schedules against, and a
// server-stamped one would put a row's retention horizon on a different clock
// from the flush deadline beside it.
var EventColumns = []string{
	IdempotencyKeyColumn,
	SubjectColumn,
	MeterColumn,
	QuantityColumn,
	OccurredAtColumn,
	RecordedAtColumn,
	PeriodStartColumn,
	DimensionsColumn,
}

// EventNullableColumns names the one ledger column a write may leave NULL.
// Nil dimensions and empty dimensions are the same fact, so they collapse to
// one rendering — see metering's encodeDimensions.
var EventNullableColumns = []string{DimensionsColumn}

// TotalColumns is the whole totals row, in the order the DDL declares it.
//
// Nothing renders a statement from this list — the statements take
// [TotalProjection] and [TotalInsertColumns] — and it is here for the
// cross-check against the shipped DDL, which is the one place a column added to
// the schema and not to this package stops being invisible.
var TotalColumns = []string{
	SubjectColumn,
	MeterColumn,
	PeriodStartColumn,
	PeriodEndColumn,
	AggregationColumn,
	QuantityColumn,
	LastOccurredAtColumn,
	FlushedQuantityColumn,
	FlushSequenceColumn,
	FlushAttemptsColumn,
	NextFlushColumn,
	ClaimedUntilColumn,
	LastErrorColumn,
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	querygen.ArchivedAtColumn,
}

// TotalProjection is the totals row as a metering.Total sees it: what every
// read projects, in the order it projects them, and the shape list every
// statement over this table is rendered from.
//
// Four columns of the row are missing from it, and each absence is a decision:
//
//   - archived_at is in the schema for the convention's sake and no statement
//     writes it. A total is a billing record; hiding one would hide an invoice
//     line. Leaving the column out of the shape list is how a statement says it
//     renders no archived predicate.
//   - last_updated_at is assigned by the writes that rewrite a row, from the
//     instant the caller supplied rather than from the server's clock — a
//     total's timeline is one clock's, and it is the clock the flusher
//     schedules against. querygen stamps the column from the server for any
//     statement whose shape list names it, so the shape list leaves it out and
//     the SET lists name it instead.
//   - created_at is written once, by the seed, and read by nothing.
//   - claimed_until is the lease. It is written by the claim and the two
//     settles and read only by the claim predicate, which compares it rather
//     than projecting it; a flusher holding a lease has no use for its own
//     expiry, since the lease it must respect is the one it took.
var TotalProjection = []string{
	SubjectColumn,
	MeterColumn,
	PeriodStartColumn,
	PeriodEndColumn,
	AggregationColumn,
	QuantityColumn,
	LastOccurredAtColumn,
	FlushedQuantityColumn,
	FlushSequenceColumn,
	FlushAttemptsColumn,
	NextFlushColumn,
	LastErrorColumn,
}

// TotalInsertColumns is what opens a total: the key, the window, how it folds,
// and the four columns a period that has just begun starts at.
//
// The insert is a seed rather than a fold. It writes a zero quantity and a
// last_occurred_at below the window's start, and every write that follows folds
// into what it finds — which is what makes the fold's arithmetic the server's
// rather than a read-modify-write's, and what gives Consume a row to lock. See
// [Render].
//
// last_occurred_at is seeded a whole second before the window's start, and the
// two halves of that are separate decisions. It is near the window rather than
// at the zero time so the column never carries a year-one timestamp for the
// fold to compare against. It is *before* the window rather than at it because
// every fold's guard is strict, so the seeded value has to be strictly earlier
// than any record the window can hold — and on the dialect that stores a bound
// time truncated to the whole second, a record arriving in the window's first
// second is stored at the window's start exactly. A whole second is that
// dialect's resolution and therefore the smallest step still strictly earlier
// on all three. It is a floor rather than a reading, which is what a period
// nothing has been recorded into holds.
//
// last_error is supplied rather than defaulted, and that is one dialect's doing:
// MySQL takes no literal default on a TEXT column, so the column is NOT NULL
// with no default there and the seed is what puts the empty string in it. The
// alternative was a column nullable on one server and not on the other two,
// which converges to a pointer in the generated row and makes every read of a
// last error ask whether this deployment is the one where it can be absent.
var TotalInsertColumns = []string{
	SubjectColumn,
	MeterColumn,
	PeriodStartColumn,
	PeriodEndColumn,
	AggregationColumn,
	QuantityColumn,
	LastOccurredAtColumn,
	NextFlushColumn,
	LastErrorColumn,
	querygen.CreatedAtColumn,
}

// ReleaseColumns is what a failed post assigns, in the order the SET list
// renders them: when to try again, why the last attempt failed, the lease
// handed back, and the stamp.
//
// flushed_quantity is deliberately absent. The post may have reached the
// provider and failed on the way back, so the next attempt has to carry the
// same delta under the same sequence — which is the whole reason the sequence
// is the provider key's varying component rather than a timestamp.
var ReleaseColumns = []string{
	NextFlushColumn,
	LastErrorColumn,
	ClaimedUntilColumn,
	querygen.LastUpdatedAtColumn,
}

// ConsumeColumns is what a decided consume assigns to the row it holds the lock
// on: the projected quantity, the event time it was projected from, and the
// stamp.
//
// The quantity is assigned rather than folded, and the event time is assigned
// rather than maximized, because this write runs against a row the caller
// locked and read. The arithmetic was done against that committed value; doing
// it again in the statement would be a second opinion nobody asked for. Every
// other write to this table takes the other reading, because none of them holds
// a lock — see [Render].
var ConsumeColumns = []string{
	QuantityColumn,
	LastOccurredAtColumn,
	querygen.LastUpdatedAtColumn,
}

// TotalNullableColumns names the totals columns a write may set to NULL.
//
// Two, and each records something that has not happened: claimed_until is NULL
// whenever nobody holds the row, which is the condition the claim predicate
// reads; last_updated_at is NULL until the first write that rewrites the row.
var TotalNullableColumns = []string{ClaimedUntilColumn, querygen.LastUpdatedAtColumn}

// The statements this package renders, named once so that the corpus, the
// tests, and the store all say the same thing.
const (
	// InsertEventQuery is the ingest ledger write, and the dedupe.
	InsertEventQuery = "InsertMeteringEvent"
	// EventExistsQuery is the read-only dedupe probe.
	EventExistsQuery = "MeteringEventExists"
	// PruneEventsQuery is the retention pass.
	PruneEventsQuery = "PruneMeteringEvents"

	// InsertTotalQuery opens a period's total.
	InsertTotalQuery = "InsertMeteringTotal"
	// GetTotalQuery reads one total.
	GetTotalQuery = "GetMeteringTotal"
	// GetTotalForUpdateQuery reads one total and holds it.
	GetTotalForUpdateQuery = "GetMeteringTotalForUpdate"

	// FoldSumTotalQuery, FoldMaxTotalQuery and FoldLastTotalQuery are the three
	// aggregations, one statement each.
	FoldSumTotalQuery  = "FoldMeteringTotalSum"
	FoldMaxTotalQuery  = "FoldMeteringTotalMax"
	FoldLastTotalQuery = "FoldMeteringTotalLast"

	// ApplyConsumeQuery writes the decision Consume made under the lock.
	ApplyConsumeQuery = "ApplyMeteringConsume"

	// SelectFlushableQuery is the claim's read, ClaimTotalQuery its lease.
	SelectFlushableQuery = "SelectFlushableMeteringTotals"
	ClaimTotalQuery      = "ClaimMeteringTotal"

	// MarkFlushedQuery settles a successful post, ReleaseFlushQuery a failed
	// one.
	MarkFlushedQuery  = "MarkMeteringTotalFlushed"
	ReleaseFlushQuery = "ReleaseMeteringFlush"
)

// eventKeyMatches is the ledger's natural key as predicates: the pair that
// addresses exactly one row, and the conflict target the insert skips a
// collision on.
//
// The meter leads, and that ordering is the DDL's. The primary key is (meter,
// idempotency_key) rather than the key alone because callers are told to use a
// request ID, and one request routinely feeds several meters — keyed on the key
// alone the second meter's insert is silently deduped against the first, and
// that customer is under-billed forever.
//
// It is a function rather than a package-level slice because every caller
// appends to what it returns, and a shared slice appended to is a slice whose
// backing array two statements can come to share.
func eventKeyMatches() []querygen.Match {
	return []querygen.Match{
		{Column: MeterColumn},
		{Column: IdempotencyKeyColumn},
	}
}

// totalKeyMatches is the totals table's natural key as predicates: the three
// columns that address exactly one row.
//
// (subject, meter, period_start) is the primary key, and it is what every
// statement over this table keys on — the conflict target of the seed, the
// predicate of each fold, of the consume, of the claim and of both settles.
// There is no id to key on and no second way to say it, which is what
// [querygen.Match] has always been for.
func totalKeyMatches() []querygen.Match {
	return []querygen.Match{
		{Column: SubjectColumn},
		{Column: MeterColumn},
		{Column: PeriodStartColumn},
	}
}

// Render returns the canonical sqlc input for d: the fourteen statements this
// package's store executes, in one file's worth of text.
//
// It is what metering/internal/queriesgen writes to the .sql beside this file
// and what CI regenerates to check the committed copy still matches. That .sql
// is sqlc-gen-unison's input, so what the store executes is this text exactly:
// the generated meteringdb package carries it per dialect, with the consumer's
// table prefix substituted once at construction.
//
// # Seed, then fold
//
// Every write to a total is a seed followed by a fold, and never an upsert
// whose conflict branch does the folding. Two writers folding into the same
// period at the same instant must not both read the total, both add their own
// quantity to it, and between them lose one — silently, and in the direction
// that under-bills. So the arithmetic is in the statement: the seed writes a
// zero row and skips one that is already there, and the fold that follows is an
// UPDATE the server evaluates against whatever the row holds when it gets
// there.
//
// The upsert this replaced said the same thing in one statement, and said it
// twice: once in the VALUES list for the row that was not there and once in the
// conflict branch for the row that was, in two spellings the three dialects
// spell six ways. Seeding first says it once. It also unifies the two paths
// that write a total — the fold and Consume's apply — which previously opened
// their rows differently and stamped last_updated_at differently for it.
//
// # last_occurred_at only ever moves forward
//
// It is what a last-aggregation meter orders by, so a record arriving late — a
// queue redelivering an hour behind — must not drag the row's notion of
// "latest" backwards and let the next out-of-order record win. Every fold
// therefore maximizes the column rather than assigning it, and the two-argument
// maximum is the one expression in this corpus the three servers do not spell
// alike — see [keepLarger].
//
// The last fold's quantity is guarded on that same movement rather than on a
// comparison of its own, so the row is always one record's reading under that
// record's time — see [movesForward]. Both are strict, and the seed is a second
// below the window for it: see [TotalInsertColumns].
//
// Consume's apply is the exception and it is not one: it runs against a row the
// caller locked and read, so the maximum was already taken in Go against the
// committed value.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	querygen.RegisterTable(TableNames...)

	rendered := []*querygen.Query{
		insertEventQuery(g),
		eventExistsQuery(g),
		pruneEventsQuery(g),
		insertTotalQuery(g),
		getTotalQuery(g),
		getTotalForUpdateQuery(g),
	}

	rendered = append(rendered, foldQueries()...)
	rendered = append(rendered,
		applyConsumeQuery(g),
		selectFlushableQuery(g),
		claimTotalQuery(),
		markFlushedQuery(),
		releaseFlushQuery(g),
	)

	return querygen.RenderFile(rendered)
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

// insertEventQuery is the ingest write, and it is the whole of this package's
// dedupe.
//
// A key already in the table takes no row and reports zero rows affected, which
// is how the caller learns the usage was already counted — decided by the
// database, in one round trip, durably, and for as long as the row is retained.
// A cache-backed dedupe would be correct only for as long as the cache held,
// and a billing period is longer than any cache TTL anybody sets.
//
// One row per statement rather than a multi-row insert, deliberately. A
// multi-row insert with conflict-ignore reports how many rows it took but not
// which ones, and folding a batch into its totals requires knowing exactly
// which records were new. Guessing is how usage gets counted twice, so a batch
// pays a statement per record and the fold that follows is grouped.
func insertEventQuery(g *querygen.Generator) *querygen.Query {
	return g.InsertIgnoreQuery(InsertEventQuery, EventsTable,
		EventColumns, EventNullableColumns, eventKeyMatches()...)
}

// eventExistsQuery is the read-only dedupe probe: has this (meter,
// idempotency_key) already been counted?
//
// It exists so that a consume about to be refused can find out whether it is a
// retry of one that already succeeded. The insert-based probe cannot answer
// that on the refusal path, because the refusal path deliberately writes
// nothing — burning the key on a consume that recorded no usage would make the
// caller's next retry look like a duplicate and be answered with a total that
// never included their usage.
func eventExistsQuery(g *querygen.Generator) *querygen.Query {
	return g.ExistsQuery(EventExistsQuery, EventsTable, EventColumns, eventKeyMatches()...)
}

// pruneEventsQuery is the retention pass: event rows past their horizon,
// removed a bounded batch at a time.
//
// The NOT EXISTS is what keeps retention from destroying evidence somebody is
// still going to need. An event row whose period still owes the provider usage
// is the only record of what that unposted total is made of — and, worse,
// deleting it re-opens the idempotency key it held, so a redelivery of that
// same event would be counted a second time into a total nobody has invoiced
// yet.
//
// That guard is not a comparison of a column against a value, so it is not a
// [querygen.Match]. It is a [querygen.Prune] condition instead, which is the
// field that exists so a statement with one authored predicate is still the
// prune rather than a statement its consumer writes out — same cap, same
// ordering, same per-dialect arm, and the count the pass loops on. The
// qualifier is asked for rather than assumed because the two arms name the
// pruned table differently.
//
// The rows are addressed by the events table's own natural key, which is the
// row-value comparison [querygen.Prune].Key exists for, and the pass drains
// oldest first so a backlog's age is a number somebody can watch.
func pruneEventsQuery(g *querygen.Generator) *querygen.Query {
	doomed := g.PruneQualifier(EventsTable)

	// The totals table is aliased because the doomed row's own qualifier is
	// already the longer of the two names, and the three key comparisons read
	// as a key when both sides are short enough to sit on one line.
	const totals = "t"

	unflushed := fmt.Sprintf(
		"NOT EXISTS (SELECT 1 FROM %[1]s %[2]s WHERE %[2]s.%[3]s = %[4]s.%[3]s"+
			" AND %[2]s.%[5]s = %[4]s.%[5]s AND %[2]s.%[6]s = %[4]s.%[6]s"+
			" AND %[2]s.%[7]s > %[2]s.%[8]s)",
		TotalsTable, totals, SubjectColumn, doomed, MeterColumn, PeriodStartColumn,
		QuantityColumn, FlushedQuantityColumn,
	)

	return g.PruneQuery(PruneEventsQuery, EventsTable,
		querygen.Prune{
			Key:        []string{MeterColumn, IdempotencyKeyColumn},
			Order:      []querygen.Order{{Column: RecordedAtColumn}},
			Conditions: []string{unflushed},
		},
		querygen.Match{Column: RecordedAtColumn, Arg: HorizonArg, Against: querygen.AtMostArgument},
	)
}

// insertTotalQuery opens a period's total, and skips one that is already open.
//
// Locking a row requires a row. A subject's first consume in a period has none,
// and two concurrent first consumes would both find nothing to lock, both
// decide against a total of zero, and both take the last unit under the limit.
// Seeding first — conflict-ignored, so the loser of that race simply proceeds —
// gives the SELECT that follows something to serialize on.
//
// It is the same statement Record's fold opens with, because opening a total is
// one thing however the write that needed it arrived. See [Render].
func insertTotalQuery(g *querygen.Generator) *querygen.Query {
	return g.InsertIgnoreQuery(InsertTotalQuery, TotalsTable,
		TotalInsertColumns, nil, totalKeyMatches()...)
}

// getTotalQuery reads one subject's total for a period.
func getTotalQuery(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery(GetTotalQuery, TotalsTable, TotalProjection, querygen.Read{}, totalKeyMatches()...)
}

// getTotalForUpdateQuery is that same read, holding the row.
//
// It is the read with a lock rather than a second rendering of it: the
// projection and the predicates come from [getTotalQuery]'s own statement, and
// the only thing this adds is the suffix. A locked read that had drifted from
// the unlocked one would be two statements claiming to read one row.
//
// The lock is what serializes two transactions consuming from the same total.
// The row is held for the remainder of the caller's transaction, so the second
// consumer blocks here and then reads the total the first one committed rather
// than deciding against a stale one.
func getTotalForUpdateQuery(g *querygen.Generator) *querygen.Query {
	query := getTotalQuery(g)
	query.Annotation.Name = GetTotalForUpdateQuery
	query.Content = strings.TrimSuffix(query.Content, ";") + rowLock(g) + ";"

	return query
}

// rowLock renders the clause that holds a read row for the rest of the
// transaction, where the dialect has one.
//
// Postgres and MySQL take FOR UPDATE. SQLite has none and needs none: it admits
// one writer at a time by construction, and the transaction this read runs in
// has already written the seed above it, so the row cannot move under it.
//
// It happens to select the same two dialects as
// [dialect.Dialect.SupportsSkipLocked], and is written out separately anyway:
// that method answers whether competing workers can skip past locked rows,
// which is a different question that only coincidentally has the same answer.
func rowLock(g *querygen.Generator) string {
	if g.Dialect() == dialect.SQLite {
		return ""
	}

	return "\nFOR UPDATE"
}

// foldQueries renders the three aggregations, one named statement each.
//
// Enumerated rather than parameterized. The aggregation decides one line of the
// SET list, and a builder taking it as an argument would be one statement whose
// text depends on a value — which is the thing this whole tier exists to
// remove. Three names means sqlc checks three statements and the store chooses
// between three generated methods, so an aggregation this package cannot fold
// is a missing case at the call site rather than a write that quietly leaves
// the total where it found it.
//
// AggregationUniqueCount is the aggregation with no statement here, and that is
// the whole of how it is refused at this layer: registration declines it above,
// and a fold for it would have to be "assign the quantity to itself", which
// reads on a dashboard as a meter that stopped counting.
func foldQueries() []*querygen.Query {
	return []*querygen.Query{
		// The additive fold. The addition is the server's, which is what makes
		// two concurrent recorders safe without either of them locking.
		foldQuery(FoldSumTotalQuery,
			fmt.Sprintf("%[1]s = %[1]s + %[2]s", QuantityColumn, argument(QuantityColumn))),

		// The peak. Same reasoning, and the same reason the maximum is taken by
		// the server rather than by whoever read the row last.
		foldQuery(FoldMaxTotalQuery, keepLarger(QuantityColumn)),

		// The most recent reading, guarded on the event time rather than
		// applied unconditionally — an out-of-order arrival leaves the newer
		// reading standing. The comparison is against the column's committed
		// value rather than against the maximum the same statement is about to
		// write, because a record that lost the race must not then be treated
		// as the one that won it.
		//
		// It is [movesForward] rather than a comparison of its own, so the
		// quantity moves exactly when last_occurred_at moves and the row is
		// always one record's reading under that record's time. The guard used
		// to admit the equal case, and on the one dialect that stores a bound
		// time truncated to the whole second that is not the corner it reads
		// as: every record inside one second compares equal to the column
		// there, so a redelivery stamped an hour late but inside the second a
		// reading was already folded from would overwrite it, and the gauge
		// would go backwards. What the strict reading costs is that same
		// resolution — inside one second on that dialect, the first record
		// folded in is the one that stands. See [TotalInsertColumns] for what
		// it makes of the seed.
		foldQuery(FoldLastTotalQuery,
			fmt.Sprintf("%[1]s = CASE WHEN %[2]s THEN %[3]s ELSE %[1]s END",
				QuantityColumn, movesForward(LastOccurredAtColumn),
				argument(QuantityColumn))),
	}
}

// foldQuery renders one aggregation's write: its own assignment to the
// quantity, then the two assignments every fold makes.
//
// The quantity comes first so the last_occurred_at the CASE above compares
// against is still the committed one. That is a property of the statement
// rather than of any server's evaluation order — SQL assignments in a SET list
// all read the row as it was before the statement — and the ordering is here so
// a reader does not have to know that to read the CASE.
func foldQuery(name, quantity string) *querygen.Query {
	assignments := []string{
		quantity,
		keepLarger(LastOccurredAtColumn),
		nullableAssign(querygen.LastUpdatedAtColumn),
	}

	return updateQuery(name, assignments, totalKeyPredicates())
}

// applyConsumeQuery writes the decision Consume made, against a row it already
// holds the lock on.
//
// A plain assignment rather than a fold, because the decision has already been
// made against the locked value: the projection was computed in Go from the
// committed quantity and this write records it. Re-deriving it in the statement
// would be a second opinion nobody asked for, and the two could disagree about
// which reading a last-aggregation meter kept.
//
// It is the one write to this table querygen renders whole, and it is that
// because holding the lock is what removes every expression from it.
func applyConsumeQuery(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(ApplyConsumeQuery, TotalsTable, TotalProjection,
		ConsumeColumns, TotalNullableColumns, totalKeyMatches()...)
}

// selectFlushableQuery picks the next batch of totals to post: those that owe
// the provider something, whose retry time has come, which nobody currently
// holds, and which have not exhausted their attempts.
//
// The quantity > flushed_quantity comparison is between two columns, which no
// index can serve — which is why the Postgres and SQLite schemas make it the
// flush index's partial predicate instead, so that index contains only the rows
// this statement wants.
//
// It projects the whole row rather than the key, and the claim that follows is
// keyed per row. The batch-shaped alternative — select the keys, lease them in
// one UPDATE, read them back — cannot be written here at all: a row-value IN
// list over a three-column key has no static arity for sqlc to check, and the
// re-read it needed had no way to drop the rows the lease had not in fact
// taken. Per row, the count of each lease says which totals this flusher holds.
//
// The lock is what makes a fleet of flushers take disjoint batches; a row
// another flusher holds is one this pass skips rather than queues behind.
// SQLite has neither the clause nor the concurrency it exists for.
func selectFlushableQuery(g *querygen.Generator) *querygen.Query {
	// A lease that was never taken is NULL, and one whose holder ran out of
	// time has lapsed. Both are claimable, and a predicate naming only the
	// second would leave every never-claimed total permanently invisible.
	claimable := fmt.Sprintf("(%[1]s IS NULL OR %[1]s <= %[2]s)",
		querygen.Qualify(TotalsTable, ClaimedUntilColumn), argument(LeaseExpiredByArg))

	predicates := []string{
		fmt.Sprintf("%s > %s",
			querygen.Qualify(TotalsTable, QuantityColumn),
			querygen.Qualify(TotalsTable, FlushedQuantityColumn)),
		fmt.Sprintf("%s <= %s", querygen.Qualify(TotalsTable, NextFlushColumn), argument(DueAtArg)),
		fmt.Sprintf("%s < %s", querygen.Qualify(TotalsTable, FlushAttemptsColumn), argument(MaxAttemptsArg)),
		claimable,
	}

	ordered := querygen.QualifyAll(TotalsTable, []string{NextFlushColumn, SubjectColumn, MeterColumn})

	content := fmt.Sprintf("SELECT\n\t%s\nFROM %s\nWHERE %s\nORDER BY %s\n%s%s;",
		strings.Join(querygen.QualifyAll(TotalsTable, TotalProjection), ",\n\t"),
		TotalsTable,
		strings.Join(predicates, "\n\tAND "),
		strings.Join(ordered, ", "),
		g.LimitClause(),
		skipLocked(g),
	)

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: SelectFlushableQuery, Type: querygen.ManyType},
		Content:    content,
	}
}

// skipLocked renders the lock a claim's read takes where the server has one. It
// follows the page size, because MySQL's bound is a bare marker that has to be
// the statement's last argument.
func skipLocked(g *querygen.Generator) string {
	if !g.Dialect().SupportsSkipLocked() {
		return ""
	}

	return "\nFOR UPDATE SKIP LOCKED"
}

// claimTotalQuery leases one selected total.
//
// The attempt count is incremented here rather than on failure: a flusher that
// crashes mid-post has still consumed an attempt, so a total whose provider
// call reliably kills the process eventually gives up rather than being
// reclaimed forever. That increment is server-side, which is why this statement
// is written out rather than rendered.
//
// The flushable guard is repeated even though the row was just selected,
// because between the select and this update another flusher's settle may have
// left the total owing nothing. The count is the answer: a lease that matched
// no row is a total this flusher does not hold, and it drops out of the batch
// rather than being posted a second time.
func claimTotalQuery() *querygen.Query {
	assignments := []string{
		nullableAssign(ClaimedUntilColumn),
		fmt.Sprintf("%[1]s = %[1]s + 1", FlushAttemptsColumn),
	}

	return updateQuery(ClaimTotalQuery, assignments,
		append(totalKeyPredicates(), stillOwing()))
}

// markFlushedQuery settles a successful post.
//
// The sequence guard is what stops a flusher whose lease lapsed mid-post from
// advancing a sequence a second flusher has already moved. That race is the one
// failure this package cannot repair after the fact: two posts of the same
// delta under two different keys are two charges, and no idempotency key undoes
// the second one. The count is how the loser learns it lost.
//
// Three of its assignments are the statement's own rather than a caller's, and
// each says something no argument should be able to say otherwise: the sequence
// advances by exactly one, the attempt budget is spent and refilled, and the
// last error is cleared because there no longer is one.
func markFlushedQuery() *querygen.Query {
	assignments := []string{
		assign(FlushedQuantityColumn),
		fmt.Sprintf("%[1]s = %[1]s + 1", FlushSequenceColumn),
		FlushAttemptsColumn + " = 0",
		assign(NextFlushColumn),
		ClaimedUntilColumn + " = NULL",
		LastErrorColumn + " = ''",
		nullableAssign(querygen.LastUpdatedAtColumn),
	}

	return updateQuery(MarkFlushedQuery, assignments,
		append(totalKeyPredicates(), atSequence()))
}

// releaseFlushQuery returns a total to the flushable set after a failed post:
// drop the lease, record why, and schedule the retry.
//
// Every column it assigns takes a bound value and its guard is the sequence, so
// it is querygen's own update rather than one written out here. The lease is
// handed back by binding NULL rather than by writing one, which is the one
// thing the rendered statement says less forcefully than the text it replaced —
// and nothing else calls it.
func releaseFlushQuery(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(ReleaseFlushQuery, TotalsTable, TotalProjection,
		ReleaseColumns, TotalNullableColumns,
		append(totalKeyMatches(), querygen.Match{Column: FlushSequenceColumn})...)
}

// updateQuery renders one guarded write over the totals table. Every one of
// them reports its row count, because in this package a write that matched
// nothing is a fact the caller acts on rather than an error the driver raises.
func updateQuery(name string, assignments, predicates []string) *querygen.Query {
	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: name, Type: querygen.ExecRowsType},
		Content: fmt.Sprintf("UPDATE %s SET\n\t%s\nWHERE %s;",
			TotalsTable,
			strings.Join(assignments, ",\n\t"),
			strings.Join(predicates, "\n\tAND "),
		),
	}
}

// totalKeyPredicates is the natural key as an authored statement writes it:
// unqualified, as the predicates of every write verb here are, because an
// UPDATE's SET clause cannot carry a table qualifier and its WHERE therefore
// does not either.
func totalKeyPredicates() []string {
	matches := totalKeyMatches()

	predicates := make([]string, 0, len(matches))
	for i := range matches {
		predicates = append(predicates, assign(matches[i].Column))
	}

	return predicates
}

// stillOwing is the guard a lease carries: the total has usage the provider has
// not been told about.
func stillOwing() string {
	return fmt.Sprintf("%s > %s", QuantityColumn, FlushedQuantityColumn)
}

// atSequence is the guard both settles carry: the row is still at the sequence
// the flusher read.
func atSequence() string {
	return assign(FlushSequenceColumn)
}

// assign renders a column taking a bound value, which is also the equality a
// predicate makes of one — the two are the same text, and a statement that
// assigned a column it was also guarding on would need two names for it. None
// here does.
func assign(column string) string {
	return fmt.Sprintf("%s = %s", column, argument(column))
}

// nullableAssign renders a column a write may set to NULL taking a bound value.
// It is spelled narg rather than arg because the column admits NULL, which is
// the fact the generated parameter's type comes from — the two spellings reach
// the same *time.Time here, and the one that says what the schema says is the
// one that goes on saying it when a column's nullability changes.
func nullableAssign(column string) string {
	return fmt.Sprintf("%s = sqlc.narg(%s)", column, column)
}

// argument renders a reference to a bound argument by name.
func argument(name string) string {
	return fmt.Sprintf("sqlc.arg(%s)", name)
}

// movesForward renders the condition a column moves on: the bound value is
// strictly past what the row holds.
//
// It is named rather than written inline because two assignments are guarded on
// it — [keepLarger]'s own, and the last fold's quantity — and the two must not
// drift. A quantity that moved on one comparison while the column it is stamped
// with moved on another would leave the row holding one record's reading under
// another record's time. See [foldQueries].
func movesForward(column string) string {
	return fmt.Sprintf("%s < %s", column, argument(column))
}

// keepLarger renders the assignment that moves a column forward and never
// back: the bound value where it is larger than what the row holds, and what
// the row holds otherwise.
//
// It is a CASE rather than a scalar maximum, and that is the portable half of a
// choice with two sides. GREATEST is the spelling Postgres and MySQL have and
// SQLite calls MAX — one dialect fact, which this package would have had to
// hold, because querygen renders no statement containing a scalar maximum and
// so has no home for one. And MySQL's analyzer resolves no type for an argument
// that appears only inside a function call, so that spelling would additionally
// have needed a type_override per column, naming back the type the other two
// engines infer.
//
// The CASE has neither problem. Its WHEN compares the argument against the
// column directly, which is the shape every analyzer types a parameter from,
// and the three servers spell it identically — so this file names no dialect
// and the generated signature is inferred rather than declared.
func keepLarger(column string) string {
	return fmt.Sprintf("%[1]s = CASE WHEN %[2]s THEN %[3]s ELSE %[1]s END",
		column, movesForward(column), argument(column))
}

package queries

import (
	"fmt"
	"strings"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// ItemsTable is the one table this package owns, at its canonical unprefixed
// spelling — what the emitted .sql names, and what a consumer's own prefix is
// rendered onto.
const ItemsTable = "work_queue_items"

// TableNames is every table workqueue owns, in the order the DDL creates them.
//
// One, and it is still a list. The registry a consumer reads back to truncate a
// database has to be fed by the table existing rather than by something
// choosing to emit its queries — see querygen's own comment on the trap — and a
// list of one is the shape that survives a second table arriving.
var TableNames = []string{ItemsTable}

// The columns this package's statements name, spelled here so the corpus and
// the queue cannot come to disagree about them.
const (
	// QueueColumn is the logical queue a row belongs to, and the leading column
	// of the primary key. Every statement below binds it, because one table
	// holds every queue in the database and a statement that omitted it would
	// act on somebody else's work.
	QueueColumn = "queue_name"
	// KeyColumn names the unit of work within its queue, and is the second half
	// of the primary key. It is the encoded rendering of the caller's key type;
	// this package never parses it.
	KeyColumn = "item_key"
	// PriorityColumn orders the queue ahead of waiting time. It only ever rises
	// on a re-enqueue, which is the whole of what a demand signal is here.
	PriorityColumn = "priority"
	// AttemptsColumn counts how many times the item has been claimed. The claim
	// increments it server-side, so there is one attempt counter and it is the
	// one the statement that handed the lease out wrote.
	AttemptsColumn = "attempts"
	// EnqueuedAtColumn is when the item first joined the queue. A re-enqueue of
	// an outstanding item leaves it alone — the item has been waiting since it
	// was first asked for — and a restart of a completed one takes the new
	// stamp, because that is a new unit of work wearing an old key.
	EnqueuedAtColumn = "enqueued_at"
	// AvailableAtColumn is the earliest instant the item may be claimed, which
	// is what Entry.Delay moves. It is also what the oldest-ready age in
	// [readQueueStats] is measured from.
	AvailableAtColumn = "available_at"
	// LeaseColumn is when the current lease lapses, and the epoch when there is
	// none. It is NOT NULL and starts at the epoch rather than being nullable,
	// so the claimable predicate is one comparison instead of a comparison plus
	// a NULL branch every future writer would have to remember.
	LeaseColumn = "lease_until"
	// CompletedAtColumn is when the item was finished, and NULL while it has
	// not been. It is the column every read excludes on and the column the
	// retirement assigns, which makes it the one place in this schema a guard
	// and an assignment meet.
	CompletedAtColumn = "completed_at"
	// LastErrorColumn holds why the last attempt handed the item back. It is
	// the last error rather than a final one: a completion clears it, and a
	// restart of a completed item clears it too.
	LastErrorColumn = "last_error"
)

// The sqlc arguments the statements bind, spelled once because the queue binds
// them and the statements read them, and a name spelled in two places is a name
// that can differ in one.
const (
	// QueueArg is the logical queue every statement is scoped to.
	QueueArg = "queue_name"
	// KeysArg is the batch of encoded keys a write addresses its rows through,
	// bound as one array.
	KeysArg = "item_keys"
	// PrioritiesArg is the batch of priorities an enqueue writes, bound as one
	// array and read positionally against [KeysArg]. See [Render] on the three
	// arrays.
	PrioritiesArg = "priorities"
	// CeilingArg is the attempt ceiling the claim and the health read measure
	// against. Non-positive means unlimited, which is why it is compared rather
	// than merely subtracted from.
	CeilingArg = "attempt_ceiling"
	// ClaimLimitArg caps one claim.
	ClaimLimitArg = "claim_limit"
	// LeaseArg is how long a claim's lease runs, as microseconds.
	LeaseArg = "lease_microseconds"
	// DelayArg is how long an item is held back before it may be claimed, as
	// microseconds.
	//
	// One name for two bindings, because it is one fact: an enqueue's delay and
	// a release's delay both land on available_at as an offset from the
	// server's own clock. The enqueue binds it as an array, one element per
	// item in the batch and read positionally against [KeysArg]; the release
	// binds a single value for the whole batch, because a hand-back is one
	// decision about a set of items rather than a decision apiece.
	DelayArg = "delay_microseconds"
	// LastErrorArg is the cause a release records, and it binds nullably: a
	// plain hand-back has no error to give.
	LastErrorArg = "last_error"
	// RetentionArg is how long a completed item is kept, as microseconds.
	RetentionArg = "retention_microseconds"
	// ReapLimitArg caps one reaping pass.
	ReapLimitArg = "reap_limit"
)

// ordinal is what the parallel arrays are joined on.
//
// A batch reaches this schema as one array per column rather than as a list of
// tuples, because a tuple list is a statement whose text grows with the batch
// and this tier has no such statement. WITH ORDINALITY is what puts the columns
// back together: each unnest yields its element and its position, and the join
// on the position is what makes the nth key, the nth priority and the nth delay
// one row again.
const ordinal = "ordinal"

// Columns is every column the table has, in the order the DDL declares it.
//
// Nothing renders a statement from this list. It is here for the cross-check
// against the shipped DDL, which is the one place a column added to the schema
// and not to this package stops being invisible.
//
// There is no convention triple to account for, and that is the schema's own
// decision rather than an omission here: completed items are swept, so
// archived_at would either do nothing or keep the table growing forever, and
// enqueued_at and available_at are the schedule a claim reads rather than a
// creation stamp and a last mutation.
var Columns = []string{
	QueueColumn,
	KeyColumn,
	PriorityColumn,
	AttemptsColumn,
	EnqueuedAtColumn,
	AvailableAtColumn,
	LeaseColumn,
	CompletedAtColumn,
	LastErrorColumn,
}

// InsertColumns is what an enqueue supplies values for.
//
// Every timestamp among them is written as a server-side expression rather than
// bound, for the reason every clock note in this file gives: the row's stamps
// and the comparisons against them have to come from one clock. attempts and
// lease_until are written as the literals that mean "fresh", and completed_at
// and last_error are left to the schema's NULL — a newly enqueued item has
// neither finished nor failed.
func InsertColumns() []string {
	return []string{
		QueueColumn,
		KeyColumn,
		PriorityColumn,
		AttemptsColumn,
		EnqueuedAtColumn,
		AvailableAtColumn,
		LeaseColumn,
	}
}

// Render returns the canonical sqlc input for one dialect.
//
// It takes the dialect and serves one, which is not a contradiction: the roster
// is a property of unison.yaml and of the schema workqueue/migrations ships,
// and this signature is what would make a second dialect a schema question
// rather than a rewrite. What it will not do is answer for a dialect this
// package has no schema for — every statement below is written in Postgres and
// would be handed back unchanged, which is the one failure a generator can have
// that produces a plausible file.
//
// It panics rather than returning an error, in the manner of the generator it
// renders through: the argument is a constant in a generator binary. The panic
// value is an error wrapping dialect.ErrUnsupported.
func Render(d dialect.Dialect) string {
	if err := dialect.RequirePostgres("work queue queries", d); err != nil {
		panic(err)
	}

	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		{Annotation: querygen.QueryAnnotation{Name: "EnqueueItems", Type: querygen.ExecType},
			Content: enqueueItems},
		{Annotation: querygen.QueryAnnotation{Name: "ClaimDueItems", Type: querygen.ManyType},
			Content: claimDueItems},
		{Annotation: querygen.QueryAnnotation{Name: "CompleteItems", Type: querygen.ExecRowsType},
			Content: completeItems},
		{Annotation: querygen.QueryAnnotation{Name: "ReleaseItems", Type: querygen.ExecRowsType},
			Content: releaseItems},
		{Annotation: querygen.QueryAnnotation{Name: "RemoveItems", Type: querygen.ExecRowsType},
			Content: removeItems},
		{Annotation: querygen.QueryAnnotation{Name: "ReapCompletedItems", Type: querygen.ExecRowsType},
			Content: reapCompletedItems},
		{Annotation: querygen.QueryAnnotation{Name: "ReadQueueStats", Type: querygen.OneType},
			Content: readQueueStats},
	})
}

// enqueueItems writes items, and merges the ones whose keys are already taken.
//
// The batch arrives as three parallel arrays rather than as a tuple list, which
// is what makes this one statement of fixed text instead of one assembled per
// call. The ORDER BY is the lock-ordering discipline this package's
// documentation opens with: ON CONFLICT DO UPDATE locks each conflicting row as
// the source reaches it, so two overlapping batches arriving in different
// orders deadlock (SQLSTATE 40P01), and one total order over the primary key
// turns that cycle into a queue. The queue sorts before it binds and the
// statement orders again, because the ordering is a property of the write
// rather than a habit of its caller.
//
// Deduplication stays the caller's, and is not merely an optimization: ON
// CONFLICT DO UPDATE refuses to touch the same row twice in one statement
// (SQLSTATE 21000), so a caller who names a key twice would otherwise lose the
// whole batch alongside it.
//
// The conflict clause encodes what re-enqueueing means: at least this urgent,
// at least this soon. Priority only rises and availability only moves earlier,
// because an enqueue is a claim on attention and the loudest caller should win.
// A completed item is the exception — it is being restarted, so it takes the
// new schedule outright and its attempt count and last error reset with it.
//
// lease_until is deliberately untouched, and its absence from the clause is the
// assertion: enqueueing an item somebody is working on right now must not
// revoke their lease.
var enqueueItems = fmt.Sprintf(`INSERT INTO %[1]s (
	%[2]s
)
SELECT
	%[3]s
FROM %[4]s
ORDER BY keys.%[5]s
ON CONFLICT (%[6]s, %[5]s) DO UPDATE SET
	%[7]s = GREATEST(%[1]s.%[7]s, excluded.%[7]s),
	%[8]s = CASE
		WHEN %[9]s THEN LEAST(%[1]s.%[8]s, excluded.%[8]s)
		ELSE excluded.%[8]s
	END,
	%[10]s = CASE
		WHEN %[9]s THEN %[1]s.%[10]s
		ELSE excluded.%[10]s
	END,
	%[11]s = CASE
		WHEN %[9]s THEN %[1]s.%[11]s
		ELSE 0
	END,
	%[12]s = CASE
		WHEN %[9]s THEN %[1]s.%[12]s
		ELSE NULL
	END,
	%[13]s = NULL`,
	ItemsTable,
	strings.Join(InsertColumns(), ",\n\t"),
	strings.Join([]string{
		"sqlc.arg(" + QueueArg + ")",
		"keys." + KeyColumn,
		"priorities." + PriorityColumn,
		"0",
		querygen.NowExpression,
		querygen.NowExpression + " + " + perRowMicroseconds("delays."+DelayArg),
		epoch,
	}, ",\n\t"),
	enqueuedBatch(),
	KeyColumn,
	QueueColumn,
	PriorityColumn,
	AvailableAtColumn,
	outstanding(),
	EnqueuedAtColumn,
	AttemptsColumn,
	LastErrorColumn,
	CompletedAtColumn,
)

// enqueuedBatch renders the three arrays an enqueue binds, joined back into
// rows on their shared position. See [ordinal].
func enqueuedBatch() string {
	return strings.Join([]string{
		unnested(KeysArg, "text", "keys", KeyColumn),
		"\tJOIN " + unnested(PrioritiesArg, "int", "priorities", PriorityColumn) + " USING (" + ordinal + ")",
		"\tJOIN " + unnested(DelayArg, "bigint", "delays", DelayArg) + " USING (" + ordinal + ")",
	}, "\n")
}

// unnested renders one bound array as a table of its elements and their
// positions.
//
// The cast is what gives sqlc the array's element type, and it is also what
// makes the argument a single bound value rather than a placeholder per
// element — which is the whole reason a batch here does not change the text of
// the statement it is bound to.
func unnested(argument, sqlType, alias, column string) string {
	return fmt.Sprintf("unnest(sqlc.arg(%s)::%s[]) WITH ORDINALITY AS %s(%s, %s)",
		argument, sqlType, alias, column, ordinal)
}

// outstanding is "this row has not been completed", which is the condition the
// conflict clause branches every one of its assignments on. A re-enqueue of an
// outstanding item merges with what is there; a re-enqueue of a completed one
// restarts it.
func outstanding() string {
	return querygen.Qualify(ItemsTable, CompletedAtColumn) + " IS NULL"
}

// claimDueItems is the lease handout: pick the due items, lock the ones nobody
// else is looking at, stamp a lease on them, and return them — one statement,
// so there is no window in which an item is selected but not yet leased.
//
// SKIP LOCKED is what lets a whole fleet claim from one table without any of
// them blocking, and it is also why the claim is exempt from the lock-ordering
// discipline the writers follow: a statement that never waits for a lock cannot
// be half of a deadlock.
//
// The LIMIT sits above the lock rather than below it, which is what makes the
// batch size mean something: Postgres skips a locked row and pulls the next one
// instead of counting it against the limit, so a claimer gets a full batch
// whenever that many items are due, however many competitors are running. A
// LIMIT pushed into a subquery beneath the lock would still be correct and
// would quietly halve throughput under contention, so this is pinned by a test
// against a real server rather than left to the plan.
//
// Ordering is priority first and waiting time second, so the loudest and then
// the oldest work goes first, with the key breaking the remaining ties so that
// two claimers walking the same backlog walk it in the same order.
//
// The returned flag is the lease's history: the prior lease is above the epoch
// sentinel only when this item was leased before and that lease lapsed rather
// than being released. That is the package's whole failure-recovery mechanism
// firing, and this statement is the only place it is observable — the fact
// stops existing the moment the new lease overwrites it.
var claimDueItems = fmt.Sprintf(`WITH due AS (
	SELECT
		%[1]s.%[2]s,
		%[1]s.%[3]s,
		%[1]s.%[4]s AS prior_lease
	FROM %[1]s
	WHERE %[5]s
	ORDER BY %[1]s.%[6]s DESC, %[1]s.%[7]s, %[1]s.%[3]s
	LIMIT sqlc.arg(%[8]s)::int
	FOR UPDATE SKIP LOCKED
)
UPDATE %[1]s SET
	%[4]s = %[9]s + %[10]s,
	%[11]s = %[1]s.%[11]s + 1
FROM due
WHERE %[1]s.%[2]s = due.%[2]s
	AND %[1]s.%[3]s = due.%[3]s
RETURNING
	%[1]s.%[3]s,
	%[1]s.%[6]s,
	%[1]s.%[11]s,
	(due.prior_lease > %[12]s) AS reclaimed`,
	ItemsTable,
	QueueColumn,
	KeyColumn,
	LeaseColumn,
	claimablePredicate(),
	PriorityColumn,
	AvailableAtColumn,
	ClaimLimitArg,
	querygen.NowExpression,
	microseconds(LeaseArg),
	AttemptsColumn,
	epoch,
)

// completeItems retires finished items. Rows are marked rather than deleted, so
// a duplicate or a gap can be investigated after the fact; the reaper removes
// them once they age past the retention window.
//
// Keys the queue has never heard of are simply not matched. That is deliberate:
// a straggler whose lease lapsed and whose item was since removed still gets to
// report success without an error nobody could act on.
var completeItems = lockedTargets("") + fmt.Sprintf(`UPDATE %[1]s SET
	%[2]s = %[3]s,
	%[4]s = %[5]s,
	%[6]s = NULL
FROM target
WHERE %[7]s`,
	ItemsTable,
	CompletedAtColumn,
	querygen.NowExpression,
	LeaseColumn,
	epoch,
	LastErrorColumn,
	targetJoin(),
)

// releaseItems is an early lease hand-back: drop the lease, hold the item until
// the delay elapses, and record why.
//
// Already-completed items are excluded rather than resurrected, and the guard
// is inside the CTE rather than applied afterwards, so a row it excludes is
// never locked at all. A late release arriving after somebody else finished the
// work is the ordinary consequence of a lapsed lease, and undoing their
// completion would turn that waste into a loop.
var releaseItems = lockedTargets(outstanding()) + fmt.Sprintf(`UPDATE %[1]s SET
	%[2]s = %[3]s,
	%[4]s = %[5]s + %[6]s,
	%[7]s = sqlc.narg(%[8]s)
FROM target
WHERE %[9]s`,
	ItemsTable,
	LeaseColumn,
	epoch,
	AvailableAtColumn,
	querygen.NowExpression,
	microseconds(DelayArg),
	LastErrorColumn,
	LastErrorArg,
	targetJoin(),
)

// removeItems deletes named items, whether or not they are leased and whether
// or not they have been completed.
//
// It is how a queue shrinks: a key whose subject no longer exists should stop
// being scheduled rather than be completed as though the work had been done. A
// worker holding a lease on a removed item finds its completion matches
// nothing, which is the same outcome as a lapsed lease and needs no extra
// handling.
var removeItems = lockedTargets("") + fmt.Sprintf(`DELETE FROM %[1]s
USING target
WHERE %[2]s`, ItemsTable, targetJoin())

// reapCompletedItems removes completed items past the retention window,
// bounded so a long-neglected queue is drained over several passes rather than
// one statement that holds locks for minutes.
//
// SKIP LOCKED here, unlike in the other writers, because the reaper is the one
// writer with nothing to prove: an item another statement is holding will still
// be expired on the next pass.
//
// It is written out rather than rendered from querygen's bounded prune, and the
// horizon is why. That shape compares a column against a ceiling the caller
// computed — see querygen.AtMostArgument — which is the right seam for a sweep
// over a column the application stamped. completed_at is stamped by the server,
// in [completeItems], so a horizon subtracted from a caller's clock would put
// two clocks either side of one comparison, in the one package in this module
// that has no clock at all. The window is therefore subtracted server-side,
// from the same expression that wrote the column, and the duration is what
// crosses the seam.
var reapCompletedItems = fmt.Sprintf(`WITH doomed AS (
	SELECT %[1]s.%[2]s, %[1]s.%[3]s
	FROM %[1]s
	WHERE %[1]s.%[2]s = sqlc.arg(%[4]s)
		AND %[1]s.%[5]s IS NOT NULL
		AND %[1]s.%[5]s < %[6]s - %[7]s
	ORDER BY %[1]s.%[2]s, %[1]s.%[3]s
	LIMIT sqlc.arg(%[8]s)::int
	FOR UPDATE SKIP LOCKED
)
DELETE FROM %[1]s
USING doomed
WHERE %[1]s.%[2]s = doomed.%[2]s
	AND %[1]s.%[3]s = doomed.%[3]s`,
	ItemsTable,
	QueueColumn,
	KeyColumn,
	QueueArg,
	CompletedAtColumn,
	querygen.NowExpression,
	microseconds(RetentionArg),
	ReapLimitArg,
)

// readQueueStats is the health read: the queue's shape, plus how long the
// oldest claimable item has been waiting.
//
// All of it in one round trip because no part of it is useful alone. A depth of
// forty thousand is unremarkable if the oldest ready item is four seconds old
// and an incident if it is four hours old, and only the age separates a queue
// that is deep and draining from one that is deep and stuck.
//
// The ready count is rendered from [claimablePredicate], the same text the
// claim is rendered from, because a ready reading that disagreed with what the
// claim will actually hand out is worse than no reading at all.
//
// The age is computed server-side, in microseconds, against the same expression
// the counts are measured against — the alternative would be returning a
// timestamp for the caller to subtract from a clock this package has spent its
// whole design avoiding.
var readQueueStats = fmt.Sprintf(`SELECT
	COALESCE(SUM(CASE WHEN %[1]s THEN 1 ELSE 0 END), 0)::bigint AS pending,
	COALESCE(SUM(CASE WHEN %[2]s THEN 1 ELSE 0 END), 0)::bigint AS ready,
	COALESCE(SUM(CASE WHEN %[1]s AND %[3]s.%[4]s > %[5]s THEN 1 ELSE 0 END), 0)::bigint AS leased,
	COALESCE(SUM(CASE WHEN %[1]s AND sqlc.arg(%[6]s)::int > 0
		AND %[3]s.%[7]s >= sqlc.arg(%[6]s)::int THEN 1 ELSE 0 END), 0)::bigint AS stalled,
	COALESCE(SUM(CASE WHEN %[3]s.%[8]s IS NOT NULL THEN 1 ELSE 0 END), 0)::bigint AS completed,
	COALESCE(%[9]s, 0)::bigint AS oldest_ready_microseconds
FROM %[3]s
WHERE %[3]s.%[10]s = sqlc.arg(%[11]s)`,
	outstanding(),
	claimablePredicate(),
	ItemsTable,
	LeaseColumn,
	querygen.NowExpression,
	CeilingArg,
	AttemptsColumn,
	CompletedAtColumn,
	microsecondsSince(fmt.Sprintf("MIN(CASE WHEN %s THEN %s END)",
		claimablePredicate(), querygen.Qualify(ItemsTable, AvailableAtColumn))),
	QueueColumn,
	QueueArg,
)

// claimablePredicate is what makes an item due: in this queue, not finished,
// not leased, not held back, and not out of attempts.
//
// Rendered once and shared by the claim and the health read, because those two
// disagreeing about what "ready" means is the failure mode a health read exists
// to rule out. A non-positive ceiling means unlimited, which is why it is
// compared rather than merely subtracted from — a queue that has asked for no
// ceiling binds a value rather than reaching a different statement.
func claimablePredicate() string {
	return strings.Join([]string{
		querygen.Qualify(ItemsTable, QueueColumn) + " = sqlc.arg(" + QueueArg + ")",
		outstanding(),
		querygen.Qualify(ItemsTable, LeaseColumn) + " <= " + querygen.NowExpression,
		querygen.Qualify(ItemsTable, AvailableAtColumn) + " <= " + querygen.NowExpression,
		"(sqlc.arg(" + CeilingArg + ")::int <= 0 OR " +
			querygen.Qualify(ItemsTable, AttemptsColumn) + " < sqlc.arg(" + CeilingArg + ")::int)",
	}, " AND ")
}

// lockedTargets renders the CTE every keyed writer reaches its rows through,
// under an optional extra guard.
//
// The ORDER BY is the point. `UPDATE … WHERE item_key IN (…)` gives Postgres no
// obligation to take row locks in any particular order, so two writers whose
// key sets overlap can deadlock; an explicitly ordered SELECT … FOR UPDATE
// makes the acquisition order the primary key's, for every writer, always. No
// SKIP LOCKED: unlike the reaper, these writers are recording a decision
// somebody already acted on, and skipping a locked row would report it as
// unmatched.
//
// The keys arrive as one bound array, so an empty batch is a statement that
// matches nothing rather than a syntax error; the queue answers that case
// without a round trip anyway.
func lockedTargets(guard string) string {
	predicates := []string{querygen.Qualify(ItemsTable, QueueColumn) + " = sqlc.arg(" + QueueArg + ")"}
	if guard != "" {
		predicates = append(predicates, guard)
	}

	predicates = append(predicates,
		querygen.Qualify(ItemsTable, KeyColumn)+" = ANY(sqlc.arg("+KeysArg+")::text[])")

	return fmt.Sprintf(`WITH target AS (
	SELECT %[1]s.%[2]s, %[1]s.%[3]s
	FROM %[1]s
	WHERE %[4]s
	ORDER BY %[1]s.%[2]s, %[1]s.%[3]s
	FOR UPDATE
)
`, ItemsTable, QueueColumn, KeyColumn, strings.Join(predicates, "\n\t\tAND "))
}

// targetJoin renders how a keyed write reaches the rows its CTE locked.
func targetJoin() string {
	return fmt.Sprintf("%[1]s.%[2]s = target.%[2]s\n\tAND %[1]s.%[3]s = target.%[3]s",
		ItemsTable, QueueColumn, KeyColumn)
}

// epoch is the never-leased sentinel.
//
// lease_until is NOT NULL and starts here rather than being nullable, so the
// claim predicate is a single comparison — a lease at or before now covers both
// "never leased" and "lease lapsed" — instead of a comparison plus a NULL
// branch that every future writer would have to remember. It is also what makes
// a reclaim answerable: a prior lease past the epoch is a lease somebody else
// held.
const epoch = "TIMESTAMPTZ 'epoch'"

// microseconds renders a bound microsecond count as an interval.
//
// A lease, a retry delay and a retention window are offsets from the server's
// own clock, so they cross the seam as durations and never as instants — which
// is the whole of this package's clock discipline, stated in one function. The
// cast is what gives sqlc a type for an argument whose only other use is
// multiplication by an interval.
func microseconds(argument string) string {
	return "(sqlc.arg(" + argument + ")::bigint * INTERVAL '1 microsecond')"
}

// perRowMicroseconds renders an unnested microsecond count as an interval.
//
// It is [microseconds] for a value that arrived in an array rather than as a
// bound scalar: the batch's delays are one argument, so the element is read off
// the unnested alias and the cast belongs to the array rather than to each of
// its elements.
func perRowMicroseconds(expression string) string {
	return "(" + expression + " * INTERVAL '1 microsecond')"
}

// microsecondsSince renders "how many microseconds have passed since expr",
// server side, as a bigint.
func microsecondsSince(expression string) string {
	return "(EXTRACT(EPOCH FROM (" + querygen.NowExpression + " - " + expression + ")) * 1000000)::bigint"
}

// FileName is the canonical .sql file a dialect's queries are written to,
// beside this file.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}

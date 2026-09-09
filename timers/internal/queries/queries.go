package queries

import (
	"fmt"
	"strings"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// TimersTable is the one table this package owns, at its canonical unprefixed
// spelling — what the emitted .sql names, and what a consumer's own prefix is
// rendered onto.
const TimersTable = "scheduled_timers"

// TableNames is every table timers owns, in the order the DDL creates them.
//
// One, and it is still a list. The registry a consumer reads back to truncate a
// database has to be fed by the table existing rather than by something
// choosing to emit its queries — see querygen's own comment on the trap — and a
// list of one is the shape that survives a second table arriving.
var TableNames = []string{TimersTable}

// The columns this package's statements name, spelled here so the corpus and
// the store cannot come to disagree about them.
const (
	// SetColumn is the logical timer set a row belongs to, and the leading
	// column of the primary key. Every statement below binds it, because one
	// table holds every set in the database and a statement that omitted it
	// would act on somebody else's timers.
	SetColumn = "timer_set"
	// KeyColumn identifies the timer within its set, and is the second half of
	// the primary key. It is the encoded rendering of the caller's key type;
	// this package never parses it.
	KeyColumn = "timer_key"
	// RunAtColumn is the instant the timer is meant to fire at, and the whole
	// reason this table is not a work queue. It is also half of what addresses
	// a firing — see [Render] on the fence.
	RunAtColumn = "run_at"
	// PayloadColumn is the opaque context a firing carries. It is the one
	// nullable member of the insert: "no payload" and "an empty payload" are
	// different statements about the timer, and a handler that branches on one
	// should not be handed the other.
	PayloadColumn = "payload"
	// AttemptsColumn counts how many times the timer has been claimed. The
	// claim increments it server-side, so there is one attempt counter and it
	// is the one the statement that handed the lease out wrote.
	AttemptsColumn = "attempts"
	// LeaseColumn is when the current lease lapses, and the epoch when there is
	// none. It is NOT NULL and starts at the epoch rather than being nullable,
	// so the due predicate is one comparison instead of a comparison plus a
	// NULL branch every future writer would have to remember.
	LeaseColumn = "lease_until"
	// FiredAtColumn is when the timer fired, and NULL while it has not. It is
	// the column every read excludes on and the column the retirement assigns,
	// which makes it the one place in this schema a guard and an assignment
	// meet.
	FiredAtColumn = "fired_at"
	// LastErrorColumn holds why the last attempt handed the timer back. It is
	// the last error rather than a final one: a reschedule clears it, and a
	// retirement clears it too, because a timer that fired has nothing left to
	// explain.
	LastErrorColumn = "last_error"
)

// The sqlc arguments the statements bind, spelled once because the store binds
// them and the statements read them, and a name spelled in two places is a name
// that can differ in one.
const (
	// SetArg is the logical set every statement is scoped to.
	SetArg = "timer_set"
	// KeysArg is the batch of encoded keys a keyed write addresses its rows
	// through, bound as one array.
	KeysArg = "timer_keys"
	// RunAtsArg is the batch of instants that fences those keys, bound as one
	// array and read positionally against [KeysArg]. See [Render] on the two
	// arrays.
	RunAtsArg = "run_ats"
	// PayloadsArg is the batch of payloads a schedule writes, bound as one
	// array and read positionally against [KeysArg].
	PayloadsArg = "payloads"
	// CeilingArg is the attempt ceiling a claim and the two aggregate reads
	// measure against. Non-positive means unlimited, which is why it is
	// compared rather than merely subtracted from.
	CeilingArg = "attempt_ceiling"
	// ClaimLimitArg caps one claim.
	ClaimLimitArg = "claim_limit"
	// LeaseArg is how long a claim's lease runs, as microseconds.
	LeaseArg = "lease_microseconds"
	// DelayArg is how far a release pushes a timer out, as microseconds.
	DelayArg = "delay_microseconds"
	// LastErrorArg is the cause a release records, and it binds nullably: a
	// plain hand-back has no error to give.
	LastErrorArg = "last_error"
	// RetentionArg is how long a fired timer is kept, as microseconds.
	RetentionArg = "retention_microseconds"
	// ReapLimitArg caps one reaping pass.
	ReapLimitArg = "reap_limit"
)

// ordinal is what the two parallel arrays are joined on.
//
// A batch reaches this schema as one array per column rather than as a list of
// tuples, because a tuple list is a statement whose text grows with the batch
// and this tier has no such statement. WITH ORDINALITY is what puts the columns
// back together: each unnest yields its element and its position, and the join
// on the position is what makes the nth key and the nth instant one row again.
const ordinal = "ordinal"

// Columns is every column the table has, in the order the DDL declares it.
//
// Nothing renders a statement from this list. It is here for the cross-check
// against the shipped DDL, which is the one place a column added to the schema
// and not to this package stops being invisible — and for the two absences
// below, each of which is a decision rather than an oversight.
//
// created_at is the schema's DEFAULT and no statement here writes it.
// last_updated_at is written by exactly one statement, the reschedule, because
// a reschedule is the only update this table takes that changes what the timer
// says; a claim, a retirement and a hand-back move this package's own
// bookkeeping and leave the row's last mutation alone. archived_at is in the
// schema for the convention's sake and no statement writes it at all: a fired
// timer is reaped rather than hidden.
var Columns = []string{
	SetColumn,
	KeyColumn,
	RunAtColumn,
	PayloadColumn,
	AttemptsColumn,
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	querygen.ArchivedAtColumn,
	LeaseColumn,
	FiredAtColumn,
	LastErrorColumn,
}

// InsertColumns is what a schedule supplies values for.
//
// created_at is the schema's DEFAULT rather than a value this process sends,
// for the reason every clock note in this file gives: the row's timestamps and
// the comparisons against them have to come from one clock. attempts and
// lease_until are written as the literals that mean "fresh" — a schedule is a
// new timer even when it lands on a key that already had one.
func InsertColumns() []string {
	return []string{
		SetColumn,
		KeyColumn,
		RunAtColumn,
		PayloadColumn,
		AttemptsColumn,
		LeaseColumn,
	}
}

// Render returns the canonical sqlc input for one dialect.
//
// It takes the dialect and serves one, which is not a contradiction: the roster
// is a property of unison.yaml and of the schema timers/migrations ships, and
// this signature is what would make a second dialect a schema question rather
// than a rewrite. What it will not do is answer for a dialect this package has
// no schema for — every statement below is written in Postgres and would be
// handed back unchanged, which is the one failure a generator can have that
// produces a plausible file.
//
// It panics rather than returning an error, in the manner of the generator it
// renders through: the argument is a constant in a generator binary. The panic
// value is an error wrapping dialect.ErrUnsupported.
func Render(d dialect.Dialect) string {
	if err := dialect.RequirePostgres("timers queries", d); err != nil {
		panic(err)
	}

	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		{Annotation: querygen.QueryAnnotation{Name: "ScheduleTimers", Type: querygen.ExecType},
			Content: scheduleTimers},
		{Annotation: querygen.QueryAnnotation{Name: "ClaimDueTimers", Type: querygen.ManyType},
			Content: claimDueTimers},
		{Annotation: querygen.QueryAnnotation{Name: "ReadNextDueTimer", Type: querygen.OneType},
			Content: readNextDueTimer},
		{Annotation: querygen.QueryAnnotation{Name: "CompleteTimers", Type: querygen.ExecRowsType},
			Content: completeTimers},
		{Annotation: querygen.QueryAnnotation{Name: "ReleaseTimers", Type: querygen.ExecRowsType},
			Content: releaseTimers},
		{Annotation: querygen.QueryAnnotation{Name: "CancelTimers", Type: querygen.ExecRowsType},
			Content: cancelTimers},
		{Annotation: querygen.QueryAnnotation{Name: "ReapFiredTimers", Type: querygen.ExecRowsType},
			Content: reapFiredTimers},
		{Annotation: querygen.QueryAnnotation{Name: "ReadTimerStats", Type: querygen.OneType},
			Content: readTimerStats},
	})
}

// scheduleTimers writes timers, and moves the ones whose keys are already
// taken.
//
// The batch arrives as three parallel arrays rather than as a tuple list, which
// is what makes this one statement of fixed text instead of one assembled per
// call. The ORDER BY is the lock-ordering discipline this package's
// documentation opens with: ON CONFLICT DO UPDATE locks each conflicting row as
// the source reaches it, so two overlapping batches arriving in different orders
// deadlock (SQLSTATE 40P01), and one total order over the primary key turns that
// cycle into a queue. The store sorts before it binds and the statement orders
// again, because the ordering is a property of the write rather than a habit of
// its caller.
//
// Deduplication stays the caller's, and is not merely an optimization: ON
// CONFLICT DO UPDATE refuses to touch the same row twice in one statement
// (SQLSTATE 21000), so a caller who names a key twice would otherwise lose the
// whole batch alongside it. Last-wins is the rule, and it is the only one
// consistent with what a second schedule of a key means everywhere else.
//
// The conflict clause is where this parts company with a work queue's. Enqueuing
// the same key twice means "at least this urgent, at least this soon", so
// availability only ever moves earlier. Scheduling the same key twice means
// "actually, then" — a trial extended by a week has to be able to move later —
// so the new instant wins outright, and the attempt count and last error reset
// with it because this is a fresh schedule rather than a retry of the old one.
//
// The lease is revoked if and only if the instant actually moved, which is the
// one place those two cases have to be told apart. A move frees the row
// immediately: the worker holding the lease is firing a schedule that no longer
// exists — the run_at fence already stops their retirement landing, see
// [completeTimers] — so leaving the lease in place would only make the new
// schedule wait out a lease nothing can still discharge. A reschedule to the
// same instant is not a move, and revoking there would be actively harmful: an
// at-least-once upstream redelivering "start trial" would free a row somebody is
// firing right now, and a second worker would claim and fire it while the first
// was still going.
var scheduleTimers = fmt.Sprintf(`INSERT INTO %[1]s (
	%[2]s
)
SELECT
	%[3]s
FROM %[4]s
ORDER BY keys.%[5]s
ON CONFLICT (%[6]s, %[5]s) DO UPDATE SET
	%[7]s = excluded.%[7]s,
	%[8]s = excluded.%[8]s,
	%[9]s = 0,
	%[10]s = NULL,
	%[11]s = NULL,
	%[12]s = %[13]s,
	%[14]s = CASE
		WHEN %[1]s.%[7]s IS DISTINCT FROM excluded.%[7]s THEN %[15]s
		ELSE %[1]s.%[14]s
	END`,
	TimersTable,
	strings.Join(InsertColumns(), ",\n\t"),
	strings.Join([]string{
		"sqlc.arg(" + SetArg + ")",
		"keys." + KeyColumn,
		"instants." + RunAtColumn,
		"payloads." + PayloadColumn,
		"0",
		epoch,
	}, ",\n\t"),
	scheduledBatch(),
	KeyColumn,
	SetColumn,
	RunAtColumn,
	PayloadColumn,
	AttemptsColumn,
	LastErrorColumn,
	FiredAtColumn,
	querygen.LastUpdatedAtColumn,
	querygen.NowExpression,
	LeaseColumn,
	epoch,
)

// scheduledBatch renders the three arrays a schedule binds, joined back into
// rows on their shared position. See [ordinal].
func scheduledBatch() string {
	return strings.Join([]string{
		unnested(KeysArg, "text", "keys", KeyColumn),
		"\tJOIN " + unnested(RunAtsArg, "timestamptz", "instants", RunAtColumn) + " USING (" + ordinal + ")",
		"\tJOIN " + unnested(PayloadsArg, "bytea", "payloads", PayloadColumn) + " USING (" + ordinal + ")",
	}, "\n")
}

// firedBatch renders the two arrays a keyed write binds — the keys and the
// instants that fence them — joined back into the pairs that address a firing.
func firedBatch() string {
	return "SELECT keys." + KeyColumn + ", instants." + RunAtColumn + "\n\t\t\tFROM " +
		unnested(KeysArg, "text", "keys", KeyColumn) + "\n\t\t\t\tJOIN " +
		unnested(RunAtsArg, "timestamptz", "instants", RunAtColumn) + " USING (" + ordinal + ")"
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

// claimDueTimers is the lease handout: pick the timers that have come due, lock
// the ones nobody else is looking at, stamp a lease on them, and return them —
// one statement, so there is no window in which a timer is selected but not yet
// leased.
//
// SKIP LOCKED is what lets a whole fleet fire from one table without any of them
// blocking, and it is also why the claim is exempt from the lock-ordering
// discipline the writers follow: a statement that never waits for a lock cannot
// be half of a deadlock.
//
// The LIMIT sits above the lock rather than below it, which is what makes the
// batch size mean something: Postgres skips a locked row and pulls the next one
// instead of counting it against the limit, so a claimer gets a full batch
// whenever that many timers are due, however many competitors are running. A
// LIMIT pushed into a subquery beneath the lock would still be correct and would
// quietly halve throughput under contention, so this is pinned by a test against
// a real server rather than left to the plan.
//
// Ordering is by run_at alone — the oldest debt first. There is no priority
// column and there should not be one: a timer already said what it wanted by
// naming an instant, and a second ordering key would only let a caller jump a
// queue of firings that are, by construction, already late.
//
// Lateness is computed server-side and returned as microseconds, because it is
// the number that says whether the fleet is keeping up and it must not be a
// subtraction against the reader's clock. So is the answer to whether this
// claim is a reclaim, which is the prior lease read before the new one
// overwrites it — a fact that exists only inside this statement.
var claimDueTimers = fmt.Sprintf(`WITH due AS (
	SELECT
		%[1]s.%[2]s,
		%[1]s.%[3]s,
		%[1]s.%[4]s AS prior_lease
	FROM %[1]s
	WHERE %[5]s
	ORDER BY %[1]s.%[6]s, %[1]s.%[3]s
	LIMIT sqlc.arg(%[7]s)::int
	FOR UPDATE SKIP LOCKED
)
UPDATE %[1]s SET
	%[4]s = %[8]s + %[9]s,
	%[10]s = %[1]s.%[10]s + 1
FROM due
WHERE %[1]s.%[2]s = due.%[2]s
	AND %[1]s.%[3]s = due.%[3]s
RETURNING
	%[1]s.%[3]s,
	%[1]s.%[11]s,
	%[1]s.%[6]s,
	%[12]s AS late_microseconds,
	%[1]s.%[10]s,
	(due.prior_lease > %[13]s) AS reclaimed`,
	TimersTable,
	SetColumn,
	KeyColumn,
	LeaseColumn,
	duePredicate(),
	RunAtColumn,
	ClaimLimitArg,
	querygen.NowExpression,
	microseconds(LeaseArg),
	AttemptsColumn,
	PayloadColumn,
	microsecondsSince(querygen.Qualify(TimersTable, RunAtColumn)),
	epoch,
)

// readNextDueTimer is the sleep hint: how long until the nearest outstanding
// timer can be claimed, and whether there is one at all.
//
// It is the read that makes a timer poller cheap, so it measures to when the row
// next becomes claimable rather than to when it was meant to run: GREATEST of
// the instant and the lease. For an unleased row the two are the same, because
// lease_until is the epoch; for a leased one it is the lease's expiry, so a
// poller whose fleet-mate has died sleeps until the lease lapses instead of
// through it. That is the difference between a stalled firing recovering in one
// lease and recovering on the poll backstop.
//
// It is a whole-set aggregate rather than an indexed lookup: the partial index
// on (timer_set, run_at) cannot serve a MIN over a GREATEST. The set it scans is
// the outstanding backlog, which is the small one — everything fired is excluded
// by the same partial predicate the index is built on — so this is a read over
// the work, not over the history.
var readNextDueTimer = fmt.Sprintf(`SELECT
	COUNT(*) AS outstanding,
	COALESCE(-%[1]s, 0)::bigint AS next_due_microseconds
FROM %[2]s
WHERE %[3]s`,
	microsecondsSince(fmt.Sprintf("MIN(GREATEST(%s, %s))",
		querygen.Qualify(TimersTable, RunAtColumn),
		querygen.Qualify(TimersTable, LeaseColumn))),
	TimersTable,
	outstandingPredicate(),
)

// completeTimers retires firings that have been handled. Rows are marked rather
// than deleted, so "did the expiry run, and when" stays answerable after the
// fact; the reaper removes them once they age past the retention window.
//
// A firing is addressed by its key and its instant together, and that pair is
// the whole of this package's answer to the reschedule race. A retirement
// carrying a stale run_at matches nothing, so a timer moved while it was being
// fired keeps its new schedule instead of being marked fired against the old
// one. That is the same "matches nothing" outcome a lapsed lease already
// produces, so it needs no new handling anywhere — and firings the set does not
// recognize are simply not matched, because a straggler whose lease lapsed,
// whose timer was cancelled, or whose schedule moved underneath it has nothing
// useful to do with an error.
//
// last_updated_at is deliberately not stamped. It records that the timer's own
// schedule changed, which a retirement does not do; the reschedule is the one
// statement here that writes it.
var completeTimers = lockedTargets("", firedPairs()) + fmt.Sprintf(`UPDATE %[1]s SET
	%[2]s = %[3]s,
	%[4]s = %[5]s,
	%[6]s = NULL
FROM target
WHERE %[7]s`,
	TimersTable,
	FiredAtColumn,
	querygen.NowExpression,
	LeaseColumn,
	epoch,
	LastErrorColumn,
	targetJoin(),
)

// releaseTimers is an early lease hand-back: drop the lease, push the timer out
// by the delay, and record why.
//
// Pushing run_at forward rather than holding it behind a separate availability
// column is deliberate — this table has one instant per row, and a retried timer
// genuinely is now scheduled for later. The cost is that a firing's lateness is
// measured against the retry's instant rather than the original, so a timer that
// has been retried five times does not look five delays late; the stalled count
// in [readTimerStats] is what surfaces that instead.
//
// Already-fired rows are excluded rather than resurrected, and the run_at fence
// applies here exactly as it does to a retirement: a release against a schedule
// that has since moved must not drag the new one backwards.
var releaseTimers = lockedTargets(
	querygen.Qualify(TimersTable, FiredAtColumn)+" IS NULL", firedPairs()) + fmt.Sprintf(`UPDATE %[1]s SET
	%[2]s = %[3]s,
	%[4]s = %[5]s + %[6]s,
	%[7]s = sqlc.narg(%[8]s)
FROM target
WHERE %[9]s`,
	TimersTable,
	LeaseColumn,
	epoch,
	RunAtColumn,
	querygen.NowExpression,
	microseconds(DelayArg),
	LastErrorColumn,
	LastErrorArg,
	targetJoin(),
)

// cancelTimers deletes named timers, whatever their schedule and whether or not
// they have fired.
//
// It deletes rather than marking, because a cancelled timer has no history worth
// keeping: nobody asks when a reminder that was called off would have gone out.
// The row count it reports is the useful part — it is how a caller learns
// whether the cancel beat the firing.
//
// The keys arrive as one bound array, so an empty batch is a statement that
// matches nothing rather than a syntax error; the store answers that case
// without a round trip anyway.
var cancelTimers = lockedTargets("",
	querygen.Qualify(TimersTable, KeyColumn)+" = ANY(sqlc.arg("+KeysArg+")::text[])") +
	fmt.Sprintf(`DELETE FROM %[1]s
USING target
WHERE %[2]s`, TimersTable, targetJoin())

// reapFiredTimers removes fired timers past the retention window, bounded so a
// long-neglected set is drained over several passes rather than one statement
// that holds locks for minutes.
//
// SKIP LOCKED here, unlike in the other writers, because the reaper is the one
// writer with nothing to prove: a row another statement is holding will still be
// expired on the next pass.
//
// It is written out rather than rendered from querygen's bounded prune, and the
// horizon is why. That shape compares a column against a ceiling the caller
// computed — see querygen.AtMostArgument — which is the right seam for a sweep
// over a column the application stamped. fired_at is stamped by the server, in
// [completeTimers], so a horizon subtracted from a caller's clock would put two
// clocks either side of one comparison. Under the injected clock this package
// takes, those two are not merely skewed but arbitrarily far apart. The window
// is therefore subtracted server-side, from the same now() that wrote the
// column, and the duration is what crosses the seam.
var reapFiredTimers = fmt.Sprintf(`WITH doomed AS (
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
	TimersTable,
	SetColumn,
	KeyColumn,
	SetArg,
	FiredAtColumn,
	querygen.NowExpression,
	microseconds(RetentionArg),
	ReapLimitArg,
)

// readTimerStats is the health read: the set's shape, plus how late the oldest
// unfired due timer already is.
//
// All of it in one round trip because no part of it is useful alone. Ten
// thousand outstanding timers is unremarkable if none of them is due yet and an
// incident if the oldest came due four hours ago, and only the lateness
// separates a set that is large from one that has stopped firing.
//
// The due count is rendered from [duePredicate], the same text the claim is
// rendered from, because a due reading that disagreed with what the claim will
// actually hand out is worse than no reading at all.
//
// The lateness is computed server-side, in microseconds, against the same clock
// the counts are measured against — the alternative would be returning a
// timestamp for the caller to subtract from a clock that has no say in when a
// timer is due.
var readTimerStats = fmt.Sprintf(`SELECT
	COALESCE(SUM(CASE WHEN %[1]s.%[2]s IS NULL THEN 1 ELSE 0 END), 0)::bigint AS outstanding,
	COALESCE(SUM(CASE WHEN %[3]s THEN 1 ELSE 0 END), 0)::bigint AS due,
	COALESCE(SUM(CASE WHEN %[1]s.%[2]s IS NULL AND %[1]s.%[4]s > %[5]s THEN 1 ELSE 0 END), 0)::bigint AS leased,
	COALESCE(SUM(CASE WHEN %[1]s.%[2]s IS NULL AND sqlc.arg(%[6]s)::int > 0
		AND %[1]s.%[7]s >= sqlc.arg(%[6]s)::int THEN 1 ELSE 0 END), 0)::bigint AS stalled,
	COALESCE(SUM(CASE WHEN %[1]s.%[2]s IS NOT NULL THEN 1 ELSE 0 END), 0)::bigint AS fired,
	COALESCE(%[8]s, 0)::bigint AS oldest_due_microseconds
FROM %[1]s
WHERE %[1]s.%[9]s = sqlc.arg(%[10]s)`,
	TimersTable,
	FiredAtColumn,
	duePredicate(),
	LeaseColumn,
	querygen.NowExpression,
	CeilingArg,
	AttemptsColumn,
	microsecondsSince(fmt.Sprintf("MIN(CASE WHEN %s THEN %s END)",
		duePredicate(), querygen.Qualify(TimersTable, RunAtColumn))),
	SetColumn,
	SetArg,
)

// duePredicate is what makes a timer ready to fire: in this set, not yet fired,
// not leased, its instant reached, and not out of attempts.
//
// Rendered once and shared by the claim and the health read, because those two
// disagreeing about what "due" means is the failure mode a health read exists to
// rule out. A non-positive ceiling means unlimited, which is why it is compared
// rather than merely subtracted from — a set that has asked for no ceiling binds
// a value rather than reaching a different statement.
func duePredicate() string {
	return strings.Join([]string{
		querygen.Qualify(TimersTable, SetColumn) + " = sqlc.arg(" + SetArg + ")",
		querygen.Qualify(TimersTable, FiredAtColumn) + " IS NULL",
		querygen.Qualify(TimersTable, LeaseColumn) + " <= " + querygen.NowExpression,
		querygen.Qualify(TimersTable, RunAtColumn) + " <= " + querygen.NowExpression,
		"(sqlc.arg(" + CeilingArg + ")::int <= 0 OR " +
			querygen.Qualify(TimersTable, AttemptsColumn) + " < sqlc.arg(" + CeilingArg + ")::int)",
	}, " AND ")
}

// outstandingPredicate is [duePredicate] with the two time comparisons dropped:
// a timer that has not fired and has attempts left, whenever it is meant to run.
//
// It is the set the next-due read measures over, because the answer that read
// wants is "how long until one of these becomes due", and a row excluded for not
// being due yet is the entire question.
func outstandingPredicate() string {
	return strings.Join([]string{
		querygen.Qualify(TimersTable, SetColumn) + " = sqlc.arg(" + SetArg + ")",
		querygen.Qualify(TimersTable, FiredAtColumn) + " IS NULL",
		"(sqlc.arg(" + CeilingArg + ")::int <= 0 OR " +
			querygen.Qualify(TimersTable, AttemptsColumn) + " < sqlc.arg(" + CeilingArg + ")::int)",
	}, " AND ")
}

// lockedTargets renders the CTE every keyed writer reaches its rows through,
// under an optional extra guard.
//
// The ORDER BY is the point. `UPDATE … WHERE timer_key IN (…)` gives Postgres no
// obligation to take row locks in any particular order, so two writers whose key
// sets overlap can deadlock; an explicitly ordered SELECT … FOR UPDATE makes the
// acquisition order the primary key's, for every writer, always. No SKIP LOCKED:
// unlike the reaper, these writers are recording a decision somebody already
// acted on, and skipping a locked row would report it as unmatched.
func lockedTargets(guard, match string) string {
	predicates := []string{querygen.Qualify(TimersTable, SetColumn) + " = sqlc.arg(" + SetArg + ")"}
	if guard != "" {
		predicates = append(predicates, guard)
	}

	predicates = append(predicates, match)

	return fmt.Sprintf(`WITH target AS (
	SELECT %[1]s.%[2]s, %[1]s.%[3]s
	FROM %[1]s
	WHERE %[4]s
	ORDER BY %[1]s.%[2]s, %[1]s.%[3]s
	FOR UPDATE
)
`, TimersTable, SetColumn, KeyColumn, strings.Join(predicates, "\n\t\tAND "))
}

// firedPairs renders the membership test a keyed write addresses its firings
// with: the key and the instant together, matched against the two parallel
// arrays the caller bound.
func firedPairs() string {
	return fmt.Sprintf("(%s, %s) IN (\n\t\t\t%s\n\t\t)",
		querygen.Qualify(TimersTable, KeyColumn),
		querygen.Qualify(TimersTable, RunAtColumn),
		firedBatch())
}

// targetJoin renders how a keyed write reaches the rows its CTE locked.
func targetJoin() string {
	return fmt.Sprintf("%[1]s.%[2]s = target.%[2]s\n\tAND %[1]s.%[3]s = target.%[3]s",
		TimersTable, SetColumn, KeyColumn)
}

// epoch is the never-leased sentinel.
//
// lease_until is NOT NULL and starts here rather than being nullable, so the due
// predicate is a single comparison — a lease at or before now covers both "never
// leased" and "lease lapsed" — instead of a comparison plus a NULL branch that
// every future writer would have to remember. It is also what makes a reclaim
// answerable: a prior lease past the epoch is a lease somebody else held.
const epoch = "TIMESTAMPTZ 'epoch'"

// microseconds renders a bound microsecond count as an interval.
//
// A lease and a retry delay and a retention window are offsets from the server's
// own clock, so they cross the seam as durations; run_at is the one timestamp
// bound absolutely, because it is the thing the caller actually meant. The cast
// is what gives sqlc a type for an argument whose only other use is
// multiplication by an interval.
func microseconds(argument string) string {
	return "(sqlc.arg(" + argument + ")::bigint * INTERVAL '1 microsecond')"
}

// microsecondsSince renders "how many microseconds have passed since expr",
// server side, as a bigint. Negative when expr is in the future, which is what
// makes it a sleep hint as readily as a lateness.
func microsecondsSince(expression string) string {
	return "(EXTRACT(EPOCH FROM (" + querygen.NowExpression + " - " + expression + ")) * 1000000)::bigint"
}

// FileName is the canonical .sql file a dialect's queries are written to, beside
// this file.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}

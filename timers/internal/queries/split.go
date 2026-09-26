package queries

import (
	"fmt"
	"strings"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// The arguments only the split corpus binds. The Postgres corpus binds a batch
// as parallel arrays; these two engines have no array type, so a row is bound
// one value per column and a batch is a run of statements.
const (
	// KeyArg is the one key a schedule writes.
	KeyArg = "timer_key"
	// RunAtArg is the one instant a schedule writes, as microseconds since the
	// Unix epoch.
	//
	// An integer rather than a timestamp, because the two engines cannot agree
	// on a timestamp's binding and the generated querier needs them to: a bound
	// time reaches SQLite as whole-second text, which would store a timer for
	// 12:00:00.700 as 12:00:00 and fire it seven tenths of a second early. A
	// count crosses the seam exactly on both, and each engine turns it into its
	// own stored shape server-side — see instant.
	RunAtArg = "run_at_microseconds"
	// PayloadArg is the one payload a schedule writes.
	PayloadArg = "payload"
	// HolderArg names the claim a statement is written for: the name a lease
	// stamps, the name a read-back finds its rows by, and the name every outcome
	// write is fenced on.
	//
	// One name per statement rather than one per firing, which is what the two
	// engines' IN lists make of the Postgres corpus's (key, run_at, holder)
	// triples: a batch naming several claims is a statement per claim, and it
	// is almost always one. See heldBy for why the instant is not bound beside
	// it.
	HolderArg = "leased_by"
	// LimitArg caps the two candidate reads, the claim's and the reaper's.
	//
	// One name for both, because MySQL leaves no choice: it accepts only a bare
	// placeholder in LIMIT and names that parameter `limit` whatever the
	// statement meant by it, and unison.split.yaml renames it once for the
	// whole corpus. SQLite spells the same name out, so the two converge.
	LimitArg = "result_limit"
	// OffsetArg is how far into its candidates a claim's read resumes, for the
	// reason OffsetArg's MySQL spelling shares with LimitArg's: a bare
	// placeholder, named `offset`, renamed once in unison.split.yaml.
	OffsetArg = "result_offset"
)

// renderSplit is the corpus for the two engines with no RETURNING and no
// arrays.
//
// It is a second statement set rather than a second spelling of the first,
// because the difference is shape rather than syntax. The Postgres claim is one
// statement that leases rows and hands them back; here it is four — a read of
// the candidates, a locking read of those by key, the lease, and a read-back by
// the name the lease stamped — held in one transaction by the set. A Postgres
// batch is a bound array per column; here it is a bound IN list, which carries
// one column, so a schedule is a statement per timer and an outcome write is a
// statement per claim. unison refuses a query whose shape differs across a
// roster, and rightly, so the two sets are two rosters: this one is generated
// into timers/internal/timerssplitdb, and Postgres keeps timersdb and its single
// statements.
//
// What does not change is anything a caller can observe. The reschedule rule,
// the due predicate, the fences, the lock order and the clock are the same
// decisions as the Postgres corpus, spelled in the engine at hand — see each
// statement.
func renderSplit(d dialect.Dialect) string {
	s := newSplit(d)

	return querygen.RenderFile([]*querygen.Query{
		{Annotation: querygen.QueryAnnotation{Name: "ScheduleTimer", Type: querygen.ExecType},
			Content: s.scheduleTimer()},
		{Annotation: querygen.QueryAnnotation{Name: "SelectDueTimers", Type: querygen.ManyType},
			Content: s.selectDueTimers()},
		{Annotation: querygen.QueryAnnotation{Name: "LockDueTimers", Type: querygen.ManyType},
			Content: s.lockDueTimers()},
		{Annotation: querygen.QueryAnnotation{Name: "LeaseTimers", Type: querygen.ExecRowsType},
			Content: s.leaseTimers()},
		{Annotation: querygen.QueryAnnotation{Name: "FetchLeasedTimers", Type: querygen.ManyType},
			Content: s.fetchLeasedTimers()},
		{Annotation: querygen.QueryAnnotation{Name: "ReadNextDueTimer", Type: querygen.OneType},
			Content: s.readNextDueTimer()},
		{Annotation: querygen.QueryAnnotation{Name: "CompleteTimers", Type: querygen.ExecRowsType},
			Content: s.completeTimers()},
		{Annotation: querygen.QueryAnnotation{Name: "ReleaseTimers", Type: querygen.ExecRowsType},
			Content: s.releaseTimers()},
		{Annotation: querygen.QueryAnnotation{Name: "CancelTimers", Type: querygen.ExecRowsType},
			Content: s.cancelTimers()},
		{Annotation: querygen.QueryAnnotation{Name: "SelectReapableTimers", Type: querygen.ManyType},
			Content: s.selectReapableTimers()},
		{Annotation: querygen.QueryAnnotation{Name: "DeleteReapedTimers", Type: querygen.ExecRowsType},
			Content: s.deleteReapedTimers()},
		{Annotation: querygen.QueryAnnotation{Name: "ReadTimerStats", Type: querygen.OneType},
			Content: s.readTimerStats()},
	})
}

// split renders the statements for one of the two engines.
type split struct {
	g *querygen.Generator
	d dialect.Dialect
}

func newSplit(d dialect.Dialect) split {
	return split{d: d, g: querygen.For(d)}
}

// scheduleTimer writes one timer, and moves the one its key already has.
//
// One row per statement, which is what an IN list's single column leaves:
// there is no spelling of three parallel batches that both engines take. The
// set runs a batch as a run of these inside one transaction, in key order,
// which is the lock-ordering discipline the Postgres statement's ORDER BY
// applies within itself.
//
// The conflict rule is the Postgres statement's, clause for clause: the new
// instant and payload win outright, the attempt count, the last error and the
// firing reset, and the lease and the name on it are revoked if and only if
// the instant actually moved. run_at is NOT NULL, so `<>` is the whole of
// IS DISTINCT FROM here.
//
// The assignments are in the order MySQL needs. Postgres and SQLite evaluate
// every SET expression against the row as it was found; MySQL evaluates them
// left to right and lets a later one see an earlier one's result. The two
// revocations compare run_at, so they are assigned before run_at is, and read
// the instant the row had rather than the one it is being given.
func (s split) scheduleTimer() string {
	existing := func(column string) string { return querygen.Qualify(TimersTable, column) }

	incoming := func(column string) string {
		if s.d == dialect.MySQL {
			return "VALUES(" + column + ")"
		}

		return "excluded." + column
	}

	moved := existing(RunAtColumn) + " <> " + incoming(RunAtColumn)

	columns := InsertColumns()
	values := []string{
		"sqlc.arg(" + SetArg + ")",
		"sqlc.arg(" + KeyArg + ")",
		s.instant(RunAtArg),
		"sqlc.narg(" + PayloadArg + ")",
		"0",
		s.epoch(),
	}

	// MySQL's created_at DEFAULT reads the session's clock, which is not the
	// one now reads; the insert writes the right one itself. See the schema.
	if s.d == dialect.MySQL {
		columns = append(columns, querygen.CreatedAtColumn)
		values = append(values, s.now())
	}

	return fmt.Sprintf(`INSERT INTO %[1]s (
	%[2]s
) VALUES (
	%[3]s
)
%[4]s
	%[5]s = CASE
		WHEN %[6]s THEN %[7]s
		ELSE %[8]s
	END,
	%[9]s = CASE
		WHEN %[6]s THEN NULL
		ELSE %[10]s
	END,
	%[11]s = %[12]s,
	%[13]s = %[14]s,
	%[15]s = 0,
	%[16]s = NULL,
	%[17]s = NULL,
	%[18]s = %[19]s`,
		TimersTable,
		strings.Join(columns, ",\n\t"),
		strings.Join(values, ",\n\t"),
		s.conflictHeader(),
		LeaseColumn, moved, s.epoch(), existing(LeaseColumn),
		HolderColumn, existing(HolderColumn),
		RunAtColumn, incoming(RunAtColumn),
		PayloadColumn, incoming(PayloadColumn),
		AttemptsColumn,
		LastErrorColumn,
		FiredAtColumn,
		querygen.LastUpdatedAtColumn, s.now(),
	)
}

// conflictHeader opens the reschedule: a conflict target where the engine takes
// one, and MySQL's clause where it does not. The primary key is the only unique
// key the table has, so MySQL's firing on any of them is firing on this one.
func (s split) conflictHeader() string {
	if s.d == dialect.MySQL {
		return "ON DUPLICATE KEY UPDATE"
	}

	return fmt.Sprintf("ON CONFLICT (%s, %s) DO UPDATE SET", SetColumn, KeyColumn)
}

// selectDueTimers is the first of the claim's statements: the due timers, the
// oldest debt first, as candidates. It takes no lock.
//
// The lock is the next statement's, by key, and the reason is the one thing
// MySQL does differently from Postgres here. A locking read over an index range
// under InnoDB's default isolation also locks the first record past the range,
// so that nothing can be inserted into it — and past the end of one set's due
// timers is, as often as not, the first due timer of the next set. A claim that
// locked its candidates by range would hide another set's earliest timer from
// that set's own claimants for as long as the claim's transaction ran, and SKIP
// LOCKED would skip it rather than wait. Locked by primary key instead, every
// lock is on a row this claim asked for and on nothing beside it.
//
// The offset is how a claim fills its batch when some of these candidates turn
// out to be held: it reads on from where the last read stopped, against the
// same snapshot, rather than from the top again. See lockDueTimers.
func (s split) selectDueTimers() string {
	return fmt.Sprintf(`SELECT %[1]s
FROM %[2]s
WHERE %[3]s
ORDER BY %[4]s, %[1]s
%[5]s`,
		querygen.Qualify(TimersTable, KeyColumn),
		TimersTable,
		s.due(),
		querygen.Qualify(TimersTable, RunAtColumn),
		s.page(),
	)
}

// lockDueTimers is the second: lock the candidates nobody else is looking at,
// and read, while holding them, whether each is still due.
//
// SKIP LOCKED is what lets a fleet claim from one table without blocking, and
// the set is what makes a batch full: a candidate another claimant holds is
// skipped here, and the set reads on — the next candidates after these — until
// it has as many as it asked for or the set has no more due. That is the
// property the Postgres claim gets by putting its LIMIT above the lock.
//
// The due predicate is repeated because the candidates were read from a
// snapshot and this reads the rows as they are now: one another claimant
// leased and committed in between is locked here and is no longer due. The keys
// name every column of the primary key, which is what keeps the lock to the
// row — a unique lookup locks the record it finds and no gap beside it — and
// byKey is what keeps MySQL on that key.
//
// SQLite has no row lock to skip and needs none: it has one writer, and the set
// holds these statements in a transaction on it.
//
// The reclaim flag is read here because this is the last moment it exists. The
// lease written in the next statement overwrites the horizon the flag is
// computed from.
func (s split) lockDueTimers() string {
	return fmt.Sprintf(`SELECT
	%[1]s,
	(%[2]s > %[3]s) AS reclaimed
FROM %[4]s%[8]s
WHERE %[5]s
	AND %[6]s%[7]s`,
		querygen.Qualify(TimersTable, KeyColumn),
		querygen.Qualify(TimersTable, LeaseColumn),
		s.epoch(),
		TimersTable,
		s.due(),
		s.keys(),
		s.skipLocked(),
		s.byKey(),
	)
}

// leaseTimers is the third: stamp the lease and the claim's name on the rows
// the locking read took, and count the attempt.
//
// It repeats the due predicate the read made rather than trusting the keys
// alone, because a write has to hold every row-state test its select held or
// it leases whatever the rows became in between. On MySQL nothing can have
// changed — the read locked them — and on SQLite the transaction is the only
// writer; the predicate is what makes that the engines' guarantee rather than
// this statement's assumption.
//
// The lease is rounded up and padded on SQLite; see after. MySQL assigns left
// to right, and nothing here reads a column an earlier assignment wrote.
func (s split) leaseTimers() string {
	return fmt.Sprintf(`UPDATE %[1]s%[11]s SET
	%[2]s = %[3]s,
	%[4]s = sqlc.arg(%[5]s),
	%[6]s = %[7]s + 1
WHERE %[8]s
	AND %[9]s%[10]s`,
		TimersTable,
		LeaseColumn, s.after(LeaseArg, true),
		HolderColumn, HolderArg,
		AttemptsColumn, querygen.Qualify(TimersTable, AttemptsColumn),
		s.due(),
		s.keys(),
		s.keyOrder(),
		s.byKey(),
	)
}

// fetchLeasedTimers is the fourth: read back what the lease took, by the name it
// stamped, with the lateness measured on the server's clock.
//
// The name is the fence, because the name is the fact: a key the read selected
// and the lease did not take is not this claim's, and the name is minted per
// claim, so nothing else answers to it. The keys narrow the read to the rows the
// lease could have taken, and are there for the path rather than the answer: no
// index carries the name, so a read by set and name alone walks every row the
// set holds — fired rows included, for as long as retention keeps them — while
// the claim's locks are held. The keys name the whole primary key beside the
// set, and byKey keeps MySQL on it.
//
// Whether the payload is NULL is read beside it, because SQLite's driver cannot
// say: it hands a zero-length blob back as a nil slice, which is the one value
// "no payload" and "an empty payload" must not share. The column keeps them
// apart; this is how the set hears which one it holds.
func (s split) fetchLeasedTimers() string {
	return fmt.Sprintf(`SELECT
	%[1]s,
	%[2]s,
	(%[2]s IS NOT NULL) AS has_payload,
	%[3]s,
	%[4]s AS late_microseconds,
	%[5]s
FROM %[6]s%[11]s
WHERE %[7]s = sqlc.arg(%[8]s)
	AND %[9]s = sqlc.arg(%[10]s)
	AND %[12]s
ORDER BY %[3]s, %[1]s`,
		querygen.Qualify(TimersTable, KeyColumn),
		querygen.Qualify(TimersTable, PayloadColumn),
		querygen.Qualify(TimersTable, RunAtColumn),
		s.integer(s.microsecondsSince(querygen.Qualify(TimersTable, RunAtColumn))),
		querygen.Qualify(TimersTable, AttemptsColumn),
		TimersTable,
		querygen.Qualify(TimersTable, SetColumn), SetArg,
		querygen.Qualify(TimersTable, HolderColumn), HolderArg,
		s.byKey(),
		s.keys(),
	)
}

// readNextDueTimer is the sleep hint, the Postgres statement's decision in this
// engine's spelling: how long until the nearest outstanding timer can be
// claimed, measured to the later of its instant and its lease.
func (s split) readNextDueTimer() string {
	return fmt.Sprintf(`SELECT
	COUNT(*) AS outstanding,
	%[1]s AS next_due_microseconds
FROM %[2]s
WHERE %[3]s`,
		s.integer("COALESCE(-"+s.microsecondsSince("MIN("+s.greatest(
			querygen.Qualify(TimersTable, RunAtColumn),
			querygen.Qualify(TimersTable, LeaseColumn))+")")+", 0)"),
		TimersTable,
		s.outstanding(),
	)
}

// completeTimers retires a claim's handled firings, and releases the name with
// the lease, for the Postgres statement's reasons: a retired firing that still
// answered to the claim that fired it would answer to it again once a
// reschedule restarts the row.
//
// Every row it matches changes — leased_by goes from a name to NULL — so MySQL's
// changed-row count is the matched count here.
func (s split) completeTimers() string {
	return fmt.Sprintf(`UPDATE %[1]s%[10]s SET
	%[2]s = %[3]s,
	%[4]s = %[5]s,
	%[6]s = NULL,
	%[7]s = NULL
WHERE %[8]s%[9]s`,
		TimersTable,
		FiredAtColumn, s.now(),
		LeaseColumn, s.epoch(),
		HolderColumn,
		LastErrorColumn,
		s.heldBy(false),
		s.keyOrder(),
		s.byKey(),
	)
}

// releaseTimers hands a claim's firings back early, pushed out by a delay, with
// a reason. Fired timers are excluded, as in the Postgres statement, so a late
// hand-back cannot resurrect a finished firing; and like completeTimers every
// matched row changes, because the name is cleared.
//
// The instant is pushed server-side, from the same clock the due predicate
// reads, and the name goes with the lease — which is what keeps the name alone
// a fence for the instant too; see heldBy.
func (s split) releaseTimers() string {
	return fmt.Sprintf(`UPDATE %[1]s%[11]s SET
	%[2]s = %[3]s,
	%[4]s = NULL,
	%[5]s = %[6]s,
	%[7]s = sqlc.narg(%[8]s)
WHERE %[9]s%[10]s`,
		TimersTable,
		LeaseColumn, s.epoch(),
		HolderColumn,
		RunAtColumn, s.after(DelayArg, false),
		LastErrorColumn, LastErrorArg,
		s.heldBy(true),
		s.keyOrder(),
		s.byKey(),
	)
}

// cancelTimers deletes named timers whatever their state; the one keyed write
// fenced on nothing but the set.
func (s split) cancelTimers() string {
	return fmt.Sprintf(`DELETE %[6]sFROM %[1]s
WHERE %[2]s = sqlc.arg(%[3]s)
	AND %[4]s%[5]s`,
		TimersTable,
		querygen.Qualify(TimersTable, SetColumn), SetArg,
		s.keys(),
		s.keyOrder(),
		s.deleteByKey(),
	)
}

// selectReapableTimers is the reaper's read: fired timers past the retention
// window, oldest first, and bounded. It takes no lock, for the reason
// selectDueTimers takes none — a locking range here would reach past this
// set's fired timers into the next set's due ones.
//
// Oldest first rather than in key order, which is where it parts from the
// Postgres statement, and the index is why: on MySQL the due index orders
// fired rows by firing under each set, and a read ordered any other way would
// sort every row past the window rather than stop at the batch it keeps.
func (s split) selectReapableTimers() string {
	return fmt.Sprintf(`SELECT %[1]s
FROM %[2]s
WHERE %[3]s
ORDER BY %[4]s
LIMIT %[5]s`,
		querygen.Qualify(TimersTable, KeyColumn),
		TimersTable,
		s.reapable(),
		querygen.Qualify(TimersTable, FiredAtColumn),
		s.limit(),
	)
}

// deleteReapedTimers removes what the read selected, by key, repeating its test
// for the reason leaseTimers repeats the claim's.
//
// It waits for a row somebody holds rather than skipping it, where the
// Postgres reaper skips. The rows are fired, so what holds one is a reschedule
// restarting it or a cancel removing it — each a single short write, which the
// delete then sees the result of and, for a restarted timer, no longer matches.
// The locks are taken in key order, as every other keyed writer takes them, so
// the wait is a queue rather than half of a deadlock.
func (s split) deleteReapedTimers() string {
	return fmt.Sprintf(`DELETE %[5]sFROM %[1]s
WHERE %[2]s
	AND %[3]s%[4]s`,
		TimersTable,
		s.reapable(),
		s.keys(),
		s.keyOrder(),
		s.deleteByKey(),
	)
}

// readTimerStats is the health read, one round trip, with the due count
// rendered from the claim's own predicate so the two cannot disagree about
// what due means.
//
// Every count is cast to a 64-bit integer, which is not decoration: MySQL's SUM
// is a DECIMAL and SQLite's is whatever the column affinity makes of it, and
// neither is the int64 the set reads.
func (s split) readTimerStats() string {
	unfired := querygen.Qualify(TimersTable, FiredAtColumn) + " IS NULL"

	count := func(predicate string) string {
		return s.integer(fmt.Sprintf("COALESCE(SUM(CASE WHEN %s THEN 1 ELSE 0 END), 0)", predicate))
	}

	return fmt.Sprintf(`SELECT
	%[1]s AS outstanding,
	%[2]s AS due,
	%[3]s AS leased,
	%[4]s AS stalled,
	%[5]s AS fired,
	%[6]s AS oldest_due_microseconds
FROM %[7]s
WHERE %[8]s = sqlc.arg(%[9]s)`,
		count(unfired),
		count(s.due()),
		count(unfired+" AND "+querygen.Qualify(TimersTable, LeaseColumn)+" > "+s.now()),
		count(unfired+" AND sqlc.arg("+CeilingArg+") > 0\n\t\tAND "+
			querygen.Qualify(TimersTable, AttemptsColumn)+" >= sqlc.arg("+CeilingArg+")"),
		count(querygen.Qualify(TimersTable, FiredAtColumn)+" IS NOT NULL"),
		s.integer("COALESCE("+s.microsecondsSince(fmt.Sprintf("MIN(CASE WHEN %s THEN %s END)",
			s.due(), querygen.Qualify(TimersTable, RunAtColumn)))+", 0)"),
		TimersTable,
		querygen.Qualify(TimersTable, SetColumn), SetArg,
	)
}

// due is the Postgres corpus's duePredicate in this engine's spelling: in this
// set, not yet fired, not leased, its instant reached, and not out of attempts.
func (s split) due() string {
	return strings.Join([]string{
		querygen.Qualify(TimersTable, SetColumn) + " = sqlc.arg(" + SetArg + ")",
		querygen.Qualify(TimersTable, FiredAtColumn) + " IS NULL",
		querygen.Qualify(TimersTable, LeaseColumn) + " <= " + s.now(),
		querygen.Qualify(TimersTable, RunAtColumn) + " <= " + s.now(),
		s.withinCeiling(),
	}, "\n\tAND ")
}

// outstanding is due with the two time comparisons dropped, as the Postgres
// corpus's outstandingPredicate is.
func (s split) outstanding() string {
	return strings.Join([]string{
		querygen.Qualify(TimersTable, SetColumn) + " = sqlc.arg(" + SetArg + ")",
		querygen.Qualify(TimersTable, FiredAtColumn) + " IS NULL",
		s.withinCeiling(),
	}, "\n\tAND ")
}

// withinCeiling is the attempt ceiling, where non-positive means unlimited.
func (s split) withinCeiling() string {
	return "(sqlc.arg(" + CeilingArg + ") <= 0 OR " +
		querygen.Qualify(TimersTable, AttemptsColumn) + " < sqlc.arg(" + CeilingArg + "))"
}

// heldBy is the fence every outcome write is addressed through: this set, this
// claim's name, these keys — and, for the write that must not touch a fired
// timer, not fired.
//
// The instant the Postgres triples also carry is not bound here, and nothing is
// lost by it, because on this table the name already fences the instant. Every
// statement that moves run_at takes the name with it: a reschedule to a new
// instant revokes the name in the same CASE that revokes the lease, a release
// clears it, and a claim — the one statement that stamps a name — leaves run_at
// where it was. So while a row answers to a claim's name, its instant is the
// one that claim was handed, and a firing whose schedule has since moved
// matches nothing here exactly as it matches nothing there. The container
// suites pin that on all three engines, and the statements that keep it true
// are pinned in this package's tests.
//
// The keys bind last, because on both engines they expand to a placeholder per
// element and an argument after the expansion would be numbered into it.
func (s split) heldBy(unfired bool) string {
	predicates := []string{
		querygen.Qualify(TimersTable, SetColumn) + " = sqlc.arg(" + SetArg + ")",
	}

	if unfired {
		predicates = append(predicates, querygen.Qualify(TimersTable, FiredAtColumn)+" IS NULL")
	}

	predicates = append(predicates,
		querygen.Qualify(TimersTable, HolderColumn)+" = sqlc.arg("+HolderArg+")",
		s.keys(),
	)

	return strings.Join(predicates, "\n\tAND ")
}

// reapable is what makes a fired timer the reaper's: in this set, fired, and
// fired longer ago than the retention window.
func (s split) reapable() string {
	return strings.Join([]string{
		querygen.Qualify(TimersTable, SetColumn) + " = sqlc.arg(" + SetArg + ")",
		querygen.Qualify(TimersTable, FiredAtColumn) + " IS NOT NULL",
		querygen.Qualify(TimersTable, FiredAtColumn) + " < " + s.before(RetentionArg),
	}, "\n\tAND ")
}

// keys is the batch a keyed write addresses, as the engine spells a bound set.
func (s split) keys() string {
	return s.g.SetCondition(querygen.Qualify(TimersTable, KeyColumn), KeysArg)
}

// keyOrder is the lock-ordering discipline for a keyed write, where the engine
// can say it.
//
// MySQL takes an ORDER BY on a single-table UPDATE or DELETE and acquires its
// row locks in that order, which is what the Postgres statements' ordered CTE is
// for: two writers whose key sets overlap then queue rather than deadlock.
// SQLite has one writer, so there is no second party to a lock cycle, and it
// parses the clause only in builds that few are compiled as.
func (s split) keyOrder() string {
	if s.d != dialect.MySQL {
		return ""
	}

	return "\nORDER BY " + querygen.Qualify(TimersTable, KeyColumn)
}

// byKey keeps a keyed statement on the primary key, on MySQL.
//
// Every statement that binds the key set also binds the set, so it names the
// whole primary key, and that is what keeps its locks to the rows it names: a
// unique lookup locks the record it finds and no gap beside it. But naming the
// key is not choosing it. The claim's statements and the reaper's also carry a
// due or reapable test, the hand-back carries `fired_at IS NULL`, and each of
// those is a range over scheduled_timers_due_idx that an optimizer may cost as
// the cheaper path — a small set, drifted statistics. A statement that took
// that path would lock by range, and the first record past a set's range is,
// as often as not, the next set's earliest due timer: the lock
// selectDueTimers exists to avoid, back by another door. The hint takes the
// choice away.
//
// FORCE INDEX on a SELECT and an UPDATE. MySQL takes no index hint on a
// single-table DELETE, so the deletes say it with deleteByKey instead.
func (s split) byKey() string {
	if s.d != dialect.MySQL {
		return ""
	}

	return " FORCE INDEX (PRIMARY)"
}

// deleteByKey is byKey for a single-table DELETE, which refuses FORCE INDEX
// and accepts the optimizer hint INDEX — the same instruction, placed where a
// DELETE can carry it. The hint names the table, and the generated querier
// prefixes that mention with every other, so it names the table the statement
// deletes from under any prefix.
func (s split) deleteByKey() string {
	if s.d != dialect.MySQL {
		return ""
	}

	return "/*+ INDEX(" + TimersTable + " PRIMARY) */ "
}

// skipLocked is the lock a claim's locking read takes, where the engine has
// one.
func (s split) skipLocked() string {
	if !s.d.SupportsSkipLocked() {
		return ""
	}

	return "\nFOR UPDATE SKIP LOCKED"
}

// limit is a candidate read's bound. MySQL accepts only a bare placeholder
// there; see LimitArg for how the two converge on one name.
func (s split) limit() string {
	if s.d == dialect.MySQL {
		return "?"
	}

	return "sqlc.arg(" + LimitArg + ")"
}

// page is a claim's candidate read's bound and where it resumes.
//
// MySQL's spelling is the two-argument LIMIT, offset first, rather than LIMIT
// and OFFSET, and not for style: sqlc hands SQLite's two arguments to the
// generated querier offset first, and unison converges a query only when its
// arguments come in one order on both engines. The two-argument form is the
// one that puts MySQL's in the same order.
func (s split) page() string {
	if s.d == dialect.MySQL {
		return "LIMIT ?, ?"
	}

	return "LIMIT sqlc.arg(" + LimitArg + ") OFFSET sqlc.arg(" + OffsetArg + ")"
}

// sqliteInstant is the one fixed-width shape every instant in SQLite's schema
// is stored in: milliseconds, as text. See timers/migrations' sqlite.sql.
const sqliteInstant = "'%Y-%m-%d %H:%M:%f'"

// now is the server's clock as a statement should store it and compare against
// it.
//
// On MySQL that is the UTC wall clock, at the microseconds the columns hold.
// run_at is the UTC wall clock whatever the session says (see instant), so the
// clock it is compared with has to be too: querygen's stored spelling,
// CURRENT_TIMESTAMP(6), reads the session's time zone, and on a connection
// whose zone is not UTC every timer would fire early or late by the offset.
//
// UTC_TIMESTAMP(6) is the obvious spelling and sqlc's catalog knows only the
// argumentless form, which is second-granular. So the whole seconds come from
// that, and the fraction from CURRENT_TIMESTAMP(6): MySQL reads every clock in
// a statement once, at the statement's start, so the two are the same instant,
// and no time zone offset has a fraction of a second in it to change the
// fraction by. On SQLite it is strftime's %f, the only clock SQLite has that
// reads finer than a second, and UTC already.
func (s split) now() string {
	if s.d == dialect.SQLite {
		return "strftime(" + sqliteInstant + ", 'now')"
	}

	return "(UTC_TIMESTAMP() + INTERVAL MICROSECOND(CURRENT_TIMESTAMP(6)) MICROSECOND)"
}

// instant renders a bound count of microseconds since the Unix epoch as the
// engine's stored instant, rounded so that a timer never fires before the
// instant it was scheduled for.
//
// MySQL stores microseconds and so keeps the count exactly; the set has
// already rounded the caller's nanoseconds up to it. SQLite stores
// milliseconds, so the count is rounded up to the next whole one: a timer for
// 12:00:00.000700 is stored as 12:00:00.001 and fires a fraction of a
// millisecond late, where rounding down would fire it early, and early is the
// one direction a timer must never move in.
//
// The arithmetic is on DATETIME rather than through FROM_UNIXTIME on MySQL,
// which would read the count in the session's time zone; an epoch plus an
// interval is the UTC wall clock the column holds, whatever the session says,
// and now reads the same clock to compare it against.
func (s split) instant(argument string) string {
	if s.d == dialect.MySQL {
		return fmt.Sprintf("(CAST('1970-01-01 00:00:00' AS DATETIME(6)) + INTERVAL sqlc.arg(%s) MICROSECOND)", argument)
	}

	return fmt.Sprintf("strftime(%s, ((sqlc.arg(%s) + 999) / 1000) / 1000.0, 'unixepoch')", sqliteInstant, argument)
}

// after renders "the server's now, plus a bound microsecond count": a lease
// horizon, or a released timer's new instant.
//
// MySQL keeps the microseconds. SQLite's clock is millisecond text, so the
// count is rounded up to whole milliseconds — a lease or a delay is never made
// shorter than it was asked for — and a lease is then padded by one more. The
// pad is for the clock rather than the count: SQLite truncates its own now to
// the millisecond, so the instant a lease is measured from can already be up to
// a millisecond behind the instant it was taken, and a lease that ended a
// millisecond before it was asked to is a lease another claimant takes over
// while this one still holds it. A release's delay needs no pad, because a
// backed-off timer that comes due a millisecond early is merely early, and a
// pad would make a zero-delay hand-back miss the claim right after it.
func (s split) after(argument string, pad bool) string {
	if s.d == dialect.MySQL {
		return fmt.Sprintf("(%s + INTERVAL sqlc.arg(%s) MICROSECOND)", s.now(), argument)
	}

	milliseconds := "(sqlc.arg(" + argument + ") + 999) / 1000"
	if pad {
		milliseconds += " + 1"
	}

	return fmt.Sprintf("strftime(%s, 'now', printf('%%+.3f seconds', (%s) / 1000.0))",
		sqliteInstant, milliseconds)
}

// before renders "the server's now, minus a bound microsecond count": the
// retention horizon. Rounded up on SQLite for after's reason, so a fired timer
// is never reaped before its window has run.
func (s split) before(argument string) string {
	if s.d == dialect.MySQL {
		return fmt.Sprintf("(%s - INTERVAL sqlc.arg(%s) MICROSECOND)", s.now(), argument)
	}

	return fmt.Sprintf("strftime(%s, 'now', printf('-%%.3f seconds', "+
		"((sqlc.arg(%s) + 999) / 1000) / 1000.0))", sqliteInstant, argument)
}

// microsecondsSince renders how long ago an instant was, server side, in
// microseconds. Negative when the instant is in the future, which is what
// makes it a sleep hint as readily as a lateness.
func (s split) microsecondsSince(expression string) string {
	if s.d == dialect.MySQL {
		return fmt.Sprintf("TIMESTAMPDIFF(MICROSECOND, %s, %s)", expression, s.now())
	}

	return fmt.Sprintf("ROUND((julianday('now') - julianday(%s)) * 86400000000)", expression)
}

// epoch is the never-leased sentinel, in the shape the engine stores instants
// in. See the Postgres corpus's epoch for why lease_until has one.
func (s split) epoch() string {
	if s.d == dialect.MySQL {
		return "'1970-01-01 00:00:00'"
	}

	return "'1970-01-01 00:00:00.000'"
}

// greatest is the two-argument maximum. MySQL spells it as Postgres does;
// SQLite's max is the scalar form when given two arguments, and compares the
// fixed-width instants as the text they are.
func (s split) greatest(a, b string) string {
	if s.d == dialect.MySQL {
		return "GREATEST(" + a + ", " + b + ")"
	}

	return "max(" + a + ", " + b + ")"
}

// integer casts an expression to the 64-bit integer the set reads.
func (s split) integer(expression string) string {
	if s.d == dialect.MySQL {
		return "CAST(" + expression + " AS SIGNED)"
	}

	return "CAST(" + expression + " AS INTEGER)"
}

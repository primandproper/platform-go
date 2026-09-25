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
	// ItemKeyArg is the one key an enqueue writes.
	ItemKeyArg = "item_key"
	// PriorityArg is the one priority an enqueue writes.
	PriorityArg = "priority"
	// HolderArg names the claim a statement is written for: the name a lease
	// stamps, the name a read-back finds its rows by, and the name every outcome
	// write is fenced on.
	//
	// One name per statement rather than one per item, which is what the two
	// engines' IN lists make of the Postgres corpus's (key, holder) pairs: a
	// batch naming several claims is a statement per claim, and it is almost
	// always one.
	HolderArg = "leased_by"
	// LimitArg caps the two locking reads, the claim's and the reaper's.
	//
	// One name for both, because MySQL leaves no choice: it accepts only a bare
	// placeholder in LIMIT and names that parameter `limit` whatever the
	// statement meant by it, and unison.split.yaml renames it once for the
	// whole corpus. SQLite spells the same name out, so the two converge.
	LimitArg = "result_limit"
)

// renderSplit is the corpus for the two engines with no RETURNING and no
// arrays.
//
// It is a second statement set rather than a second spelling of the first,
// because the difference is shape rather than syntax. The Postgres claim is one
// statement that leases rows and hands them back; here it is three — a locking
// read, the lease, and a read-back by the name the lease stamped — held in one
// transaction by the queue. A Postgres batch is a bound array per column; here
// it is a bound IN list, which carries one column, so an enqueue is a statement
// per row and an outcome write is a statement per claim. unison refuses a query
// whose shape differs across a roster, and rightly, so the two sets are two
// rosters: this one is generated into workqueue/internal/workqueuesplitdb, and
// Postgres keeps workqueuedb and its single statements.
//
// What does not change is anything a caller can observe. The merge rule, the
// claimable predicate, the fence on the claim's name, the forward-only extension
// and the clock are the same decisions as the Postgres corpus, spelled in the
// engine at hand — see each statement.
func renderSplit(d dialect.Dialect) string {
	s := newSplit(d)

	return querygen.RenderFile([]*querygen.Query{
		{Annotation: querygen.QueryAnnotation{Name: "EnqueueItem", Type: querygen.ExecType},
			Content: s.enqueueItem()},
		{Annotation: querygen.QueryAnnotation{Name: "SelectDueItems", Type: querygen.ManyType},
			Content: s.selectDueItems()},
		{Annotation: querygen.QueryAnnotation{Name: "LeaseItems", Type: querygen.ExecRowsType},
			Content: s.leaseItems()},
		{Annotation: querygen.QueryAnnotation{Name: "FetchLeasedItems", Type: querygen.ManyType},
			Content: s.fetchLeasedItems()},
		{Annotation: querygen.QueryAnnotation{Name: "ExtendItems", Type: querygen.ExecRowsType},
			Content: s.extendItems()},
		{Annotation: querygen.QueryAnnotation{Name: "CountHeldItems", Type: querygen.OneType},
			Content: s.countHeldItems()},
		{Annotation: querygen.QueryAnnotation{Name: "CompleteItems", Type: querygen.ExecRowsType},
			Content: s.completeItems()},
		{Annotation: querygen.QueryAnnotation{Name: "ReleaseItems", Type: querygen.ExecRowsType},
			Content: s.releaseItems()},
		{Annotation: querygen.QueryAnnotation{Name: "RemoveItems", Type: querygen.ExecRowsType},
			Content: s.removeItems()},
		{Annotation: querygen.QueryAnnotation{Name: "RequeueItems", Type: querygen.ExecRowsType},
			Content: s.requeueItems()},
		{Annotation: querygen.QueryAnnotation{Name: "SelectReapableItems", Type: querygen.ManyType},
			Content: s.selectReapableItems()},
		{Annotation: querygen.QueryAnnotation{Name: "DeleteReapedItems", Type: querygen.ExecRowsType},
			Content: s.deleteReapedItems()},
		{Annotation: querygen.QueryAnnotation{Name: "ReadQueueStats", Type: querygen.OneType},
			Content: s.readQueueStats()},
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

// enqueueItem writes one item, and merges it into the row its key already has.
//
// One row per statement, which is what an IN list's single column leaves:
// there is no spelling of three parallel batches that both engines take. The
// queue runs a merged batch as a run of these inside one transaction, in key
// order, which is the lock-ordering discipline the Postgres statement's ORDER BY
// applies within itself.
//
// The merge is the Postgres statement's, clause for clause — at least this
// urgent, at least this soon, and a completed item restarted outright — and
// lease_until and leased_by are absent for the same reason: enqueueing an item
// somebody is working on must not revoke their lease.
//
// The assignments are in the order MySQL needs. Postgres and SQLite evaluate
// every SET expression against the row as it was found; MySQL evaluates them
// left to right and lets a later one see an earlier one's result. Every branch
// below tests completed_at, so completed_at is assigned last, and the branches
// all read the row before the restart cleared it.
func (s split) enqueueItem() string {
	existing := func(column string) string { return querygen.Qualify(ItemsTable, column) }

	incoming := func(column string) string {
		if s.d == dialect.MySQL {
			return "VALUES(" + column + ")"
		}

		return "excluded." + column
	}

	return fmt.Sprintf(`INSERT INTO %[1]s (
	%[2]s
) VALUES (
	%[3]s
)
%[4]s
	%[5]s = %[6]s,
	%[7]s = CASE
		WHEN %[8]s THEN %[9]s
		ELSE %[10]s
	END,
	%[11]s = CASE
		WHEN %[8]s THEN %[12]s
		ELSE %[13]s
	END,
	%[14]s = CASE
		WHEN %[8]s THEN %[15]s
		ELSE 0
	END,
	%[16]s = CASE
		WHEN %[8]s THEN %[17]s
		ELSE NULL
	END,
	%[18]s = NULL`,
		ItemsTable,
		strings.Join(InsertColumns(), ",\n\t"),
		strings.Join([]string{
			"sqlc.arg(" + QueueArg + ")",
			"sqlc.arg(" + ItemKeyArg + ")",
			"sqlc.arg(" + PriorityArg + ")",
			"0",
			s.now(),
			s.after(DelayArg, false),
			s.epoch(),
		}, ",\n\t"),
		s.conflictHeader(),
		PriorityColumn, s.greatest(existing(PriorityColumn), incoming(PriorityColumn)),
		AvailableAtColumn,
		outstanding(),
		s.least(existing(AvailableAtColumn), incoming(AvailableAtColumn)),
		incoming(AvailableAtColumn),
		EnqueuedAtColumn,
		existing(EnqueuedAtColumn),
		incoming(EnqueuedAtColumn),
		AttemptsColumn,
		existing(AttemptsColumn),
		LastErrorColumn,
		existing(LastErrorColumn),
		CompletedAtColumn,
	)
}

// conflictHeader opens the merge: a conflict target where the engine takes one,
// and MySQL's clause where it does not. The primary key is the only unique key
// the table has, so MySQL's firing on any of them is firing on this one.
func (s split) conflictHeader() string {
	if s.d == dialect.MySQL {
		return "ON DUPLICATE KEY UPDATE"
	}

	return fmt.Sprintf("ON CONFLICT (%s, %s) DO UPDATE SET", QueueColumn, KeyColumn)
}

// selectDueItems is the first of the claim's three statements: pick the due
// items and lock the ones nobody else is looking at.
//
// It is the Postgres claim's CTE with the lease taken out of it, and it keeps
// the property that CTE is pinned for: on MySQL the LIMIT counts the rows the
// read returned rather than the rows it looked at, so a row another claimer
// holds is skipped and replaced, and a claimer gets a full batch while that
// many items are due. The claim index is what keeps the lock to the batch — a
// locking read that had to sort would lock every candidate it read.
//
// SQLite has no row lock to skip and needs none: it has one writer, and the
// queue holds these statements in a transaction on it.
//
// The reclaim flag is read here because this is the last moment it exists. The
// lease written in the next statement overwrites the horizon the flag is
// computed from.
func (s split) selectDueItems() string {
	return fmt.Sprintf(`SELECT
	%[1]s,
	(%[2]s > %[3]s) AS reclaimed
FROM %[4]s
WHERE %[5]s
ORDER BY %[6]s DESC, %[7]s, %[1]s
LIMIT %[8]s%[9]s`,
		querygen.Qualify(ItemsTable, KeyColumn),
		querygen.Qualify(ItemsTable, LeaseColumn),
		s.epoch(),
		ItemsTable,
		s.claimable(),
		querygen.Qualify(ItemsTable, PriorityColumn),
		querygen.Qualify(ItemsTable, AvailableAtColumn),
		s.limit(),
		s.skipLocked(),
	)
}

// leaseItems is the second: stamp the lease and the claim's name on the rows
// the read selected, and count the attempt.
//
// It repeats the claimable predicate the read made rather than trusting the
// keys alone, because a write has to hold every row-state test its select held
// or it leases whatever the rows became in between. On MySQL nothing can have
// changed — the read locked them — and on SQLite the transaction is the only
// writer; the predicate is what makes that the engines' guarantee rather than
// this statement's assumption.
//
// The lease is rounded up and padded on SQLite; see after. MySQL assigns left
// to right, and nothing here reads a column an earlier assignment wrote.
func (s split) leaseItems() string {
	return fmt.Sprintf(`UPDATE %[1]s SET
	%[2]s = %[3]s,
	%[4]s = sqlc.arg(%[5]s),
	%[6]s = %[7]s + 1
WHERE %[8]s
	AND %[9]s`,
		ItemsTable,
		LeaseColumn, s.after(LeaseArg, true),
		HolderColumn, HolderArg,
		AttemptsColumn, querygen.Qualify(ItemsTable, AttemptsColumn),
		s.claimable(),
		s.keys(),
	)
}

// fetchLeasedItems is the third: read back what the lease took, by the name it
// stamped.
//
// The name rather than the keys, because the name is the fact: a key the read
// selected and the lease did not take is not this claim's, and the name is
// minted per claim, so nothing else answers to it.
func (s split) fetchLeasedItems() string {
	return fmt.Sprintf(`SELECT
	%[1]s,
	%[2]s,
	%[3]s
FROM %[4]s
WHERE %[5]s = sqlc.arg(%[6]s)
	AND %[7]s = sqlc.arg(%[8]s)
ORDER BY %[2]s DESC, %[9]s, %[1]s`,
		querygen.Qualify(ItemsTable, KeyColumn),
		querygen.Qualify(ItemsTable, PriorityColumn),
		querygen.Qualify(ItemsTable, AttemptsColumn),
		ItemsTable,
		querygen.Qualify(ItemsTable, QueueColumn), QueueArg,
		querygen.Qualify(ItemsTable, HolderColumn), HolderArg,
		querygen.Qualify(ItemsTable, AvailableAtColumn),
	)
}

// extendItems pushes a claim's leases out, never in. The Postgres statement's
// decision in both respects — fenced on the claim, and GREATEST so that an
// extension arriving under a longer lease leaves it alone.
//
// Its row count is not the answer to how many items the claim still holds, and
// the reason is MySQL's: it reports rows changed rather than rows matched, and
// an extension shorter than the lease it lands on changes nothing. That answer
// is countHeldItems', read in the same transaction.
func (s split) extendItems() string {
	return fmt.Sprintf(`UPDATE %[1]s SET
	%[2]s = %[3]s
WHERE %[4]s%[5]s`,
		ItemsTable,
		LeaseColumn, s.greatest(querygen.Qualify(ItemsTable, LeaseColumn), s.after(LeaseArg, true)),
		s.heldBy(true),
		s.keyOrder(),
	)
}

// countHeldItems is how many of the named items the claim still holds: the rows
// extendItems matched, whether or not it moved them.
//
// Read in the transaction the extension ran in, after it, so the count is of
// the same rows — the extension neither completes an item nor changes its
// holder, and the rows it matched are locked until the transaction ends.
func (s split) countHeldItems() string {
	return fmt.Sprintf(`SELECT COUNT(*) AS held
FROM %[1]s
WHERE %[2]s`,
		ItemsTable,
		s.heldBy(true),
	)
}

// completeItems retires a claim's finished items, and releases the name with
// the lease, for the Postgres statement's reasons: a retired item that still
// answered to the claim that finished it would answer to it again after a
// restart.
//
// Every row it matches changes — leased_by goes from a name to NULL — so MySQL's
// changed-row count is the matched count here.
func (s split) completeItems() string {
	return fmt.Sprintf(`UPDATE %[1]s SET
	%[2]s = %[3]s,
	%[4]s = %[5]s,
	%[6]s = NULL,
	%[7]s = NULL
WHERE %[8]s%[9]s`,
		ItemsTable,
		CompletedAtColumn, s.now(),
		LeaseColumn, s.epoch(),
		HolderColumn,
		LastErrorColumn,
		s.heldBy(false),
		s.keyOrder(),
	)
}

// releaseItems hands a claim's items back early, held for a delay, with a
// reason. Completed items are excluded, as in the Postgres statement, so a late
// hand-back cannot resurrect finished work; and like completeItems every
// matched row changes, because the name is cleared.
func (s split) releaseItems() string {
	return fmt.Sprintf(`UPDATE %[1]s SET
	%[2]s = %[3]s,
	%[4]s = NULL,
	%[5]s = %[6]s,
	%[7]s = sqlc.narg(%[8]s)
WHERE %[9]s%[10]s`,
		ItemsTable,
		LeaseColumn, s.epoch(),
		HolderColumn,
		AvailableAtColumn, s.after(DelayArg, false),
		LastErrorColumn, LastErrorArg,
		s.heldBy(true),
		s.keyOrder(),
	)
}

// removeItems deletes named items whatever their state; the operator's write,
// fenced on nothing but the queue.
func (s split) removeItems() string {
	return fmt.Sprintf(`DELETE FROM %[1]s
WHERE %[2]s = sqlc.arg(%[3]s)
	AND %[4]s%[5]s`,
		ItemsTable,
		querygen.Qualify(ItemsTable, QueueColumn), QueueArg,
		s.keys(),
		s.keyOrder(),
	)
}

// requeueItems revives stalled items: the attempt counter to zero, claimable
// now, and nothing else on the row touched — the Postgres statement's decision,
// including leaving a live lease where it is.
func (s split) requeueItems() string {
	return fmt.Sprintf(`UPDATE %[1]s SET
	%[2]s = 0,
	%[3]s = %[4]s
WHERE %[5]s = sqlc.arg(%[6]s)
	AND %[7]s
	AND %[8]s%[9]s`,
		ItemsTable,
		AttemptsColumn,
		AvailableAtColumn, s.now(),
		querygen.Qualify(ItemsTable, QueueColumn), QueueArg,
		outstanding(),
		s.keys(),
		s.keyOrder(),
	)
}

// selectReapableItems is the reaper's locking read: completed items past the
// retention window, oldest first, bounded, and skipping whatever another
// statement holds — the reaper has nothing to prove, and an item held now is
// reaped on the next pass.
//
// Oldest first rather than in key order, which is where it parts from the
// Postgres statement, and the index is why: on MySQL the claim index orders
// completed rows by completion under each queue, and a locking read ordered
// any other way would sort — and lock — every row past the window rather than
// the batch it keeps.
func (s split) selectReapableItems() string {
	return fmt.Sprintf(`SELECT %[1]s
FROM %[2]s
WHERE %[3]s
ORDER BY %[4]s
LIMIT %[5]s%[6]s`,
		querygen.Qualify(ItemsTable, KeyColumn),
		ItemsTable,
		s.reapable(),
		querygen.Qualify(ItemsTable, CompletedAtColumn),
		s.limit(),
		s.skipLocked(),
	)
}

// deleteReapedItems removes what the read selected, repeating its test for the
// reason leaseItems repeats the claim's.
func (s split) deleteReapedItems() string {
	return fmt.Sprintf(`DELETE FROM %[1]s
WHERE %[2]s
	AND %[3]s%[4]s`,
		ItemsTable,
		s.reapable(),
		s.keys(),
		s.keyOrder(),
	)
}

// readQueueStats is the health read, one round trip, with the ready count
// rendered from the claim's own predicate so the two cannot disagree about what
// ready means.
//
// Every count is cast to a 64-bit integer, which is not decoration: MySQL's SUM
// is a DECIMAL and SQLite's is whatever the column affinity makes of it, and
// neither is the int64 the queue reads.
func (s split) readQueueStats() string {
	count := func(predicate string) string {
		return s.integer(fmt.Sprintf("COALESCE(SUM(CASE WHEN %s THEN 1 ELSE 0 END), 0)", predicate))
	}

	return fmt.Sprintf(`SELECT
	%[1]s AS pending,
	%[2]s AS ready,
	%[3]s AS leased,
	%[4]s AS stalled,
	%[5]s AS completed,
	%[6]s AS oldest_ready_microseconds
FROM %[7]s
WHERE %[8]s = sqlc.arg(%[9]s)`,
		count(outstanding()),
		count(s.claimable()),
		count(outstanding()+" AND "+querygen.Qualify(ItemsTable, LeaseColumn)+" > "+s.now()),
		count(outstanding()+" AND sqlc.arg("+CeilingArg+") > 0\n\t\tAND "+
			querygen.Qualify(ItemsTable, AttemptsColumn)+" >= sqlc.arg("+CeilingArg+")"),
		count(querygen.Qualify(ItemsTable, CompletedAtColumn)+" IS NOT NULL"),
		s.integer("COALESCE("+s.microsecondsSince(fmt.Sprintf("MIN(CASE WHEN %s THEN %s END)",
			s.claimable(), querygen.Qualify(ItemsTable, AvailableAtColumn)))+", 0)"),
		ItemsTable,
		querygen.Qualify(ItemsTable, QueueColumn), QueueArg,
	)
}

// claimable is the Postgres corpus's claimablePredicate in this engine's
// spelling: in this queue, not finished, not leased, not held back, and not out
// of attempts.
func (s split) claimable() string {
	return strings.Join([]string{
		querygen.Qualify(ItemsTable, QueueColumn) + " = sqlc.arg(" + QueueArg + ")",
		outstanding(),
		querygen.Qualify(ItemsTable, LeaseColumn) + " <= " + s.now(),
		querygen.Qualify(ItemsTable, AvailableAtColumn) + " <= " + s.now(),
		"(sqlc.arg(" + CeilingArg + ") <= 0 OR " +
			querygen.Qualify(ItemsTable, AttemptsColumn) + " < sqlc.arg(" + CeilingArg + "))",
	}, "\n\tAND ")
}

// heldBy is the fence every outcome write is addressed through: this queue, this
// claim's name, these keys — and, for the writes that must not touch a finished
// item, not completed.
//
// The keys bind last, because on both engines they expand to a placeholder per
// element and an argument after the expansion would be numbered into it.
func (s split) heldBy(unfinished bool) string {
	predicates := []string{
		querygen.Qualify(ItemsTable, QueueColumn) + " = sqlc.arg(" + QueueArg + ")",
	}

	if unfinished {
		predicates = append(predicates, outstanding())
	}

	predicates = append(predicates,
		querygen.Qualify(ItemsTable, HolderColumn)+" = sqlc.arg("+HolderArg+")",
		s.keys(),
	)

	return strings.Join(predicates, "\n\tAND ")
}

// reapable is what makes a completed item the reaper's: in this queue, finished,
// and finished longer ago than the retention window.
func (s split) reapable() string {
	return strings.Join([]string{
		querygen.Qualify(ItemsTable, QueueColumn) + " = sqlc.arg(" + QueueArg + ")",
		querygen.Qualify(ItemsTable, CompletedAtColumn) + " IS NOT NULL",
		querygen.Qualify(ItemsTable, CompletedAtColumn) + " < " + s.before(RetentionArg),
	}, "\n\tAND ")
}

// keys is the batch a keyed write addresses, as the engine spells a bound set.
func (s split) keys() string {
	return s.g.SetCondition(querygen.Qualify(ItemsTable, KeyColumn), KeysArg)
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

	return "\nORDER BY " + querygen.Qualify(ItemsTable, KeyColumn)
}

// skipLocked is the lock a claim or reap read takes, where the engine has one.
func (s split) skipLocked() string {
	if !s.d.SupportsSkipLocked() {
		return ""
	}

	return "\nFOR UPDATE SKIP LOCKED"
}

// limit is a locking read's bound. MySQL accepts only a bare placeholder there;
// see LimitArg for how the two converge on one name.
func (s split) limit() string {
	if s.d == dialect.MySQL {
		return "?"
	}

	return "sqlc.arg(" + LimitArg + ")"
}

// sqliteNow is SQLite's clock at the finest grain it has: milliseconds, as text
// in the one fixed-width shape every instant in its schema is stored in. See
// workqueue/migrations' sqlite.sql.
const sqliteNow = "strftime('%Y-%m-%d %H:%M:%f', 'now')"

// now is the server's clock as a statement should store it and compare against
// it.
//
// On MySQL that is querygen's stored spelling rather than the bare
// CURRENT_TIMESTAMP, which is second-granular whatever the column holds.
func (s split) now() string {
	if s.d == dialect.SQLite {
		return sqliteNow
	}

	return s.g.StoredNow()
}

// after renders "the server's now, plus a bound microsecond count": a lease
// horizon or an availability.
//
// MySQL keeps the microseconds. SQLite's clock is millisecond text, so the
// count is rounded up to whole milliseconds — a lease or a delay is never made
// shorter than it was asked for — and a lease is then padded by one more. The
// pad is for the clock rather than the count: SQLite truncates its own now to
// the millisecond, so the instant a lease is measured from can already be up to
// a millisecond behind the instant it was taken, and a lease that ended a
// millisecond before it was asked to is a lease another worker takes over while
// this one still holds it. A delay needs no pad, because an item that comes due
// a millisecond early is merely early.
func (s split) after(argument string, pad bool) string {
	if s.d == dialect.MySQL {
		return fmt.Sprintf("(%s + INTERVAL sqlc.arg(%s) MICROSECOND)", s.now(), argument)
	}

	milliseconds := "(sqlc.arg(" + argument + ") + 999) / 1000"
	if pad {
		milliseconds += " + 1"
	}

	return fmt.Sprintf("strftime('%%Y-%%m-%%d %%H:%%M:%%f', 'now', printf('%%+.3f seconds', (%s) / 1000.0))",
		milliseconds)
}

// before renders "the server's now, minus a bound microsecond count": the
// retention horizon. Rounded up on SQLite for after's reason, so an item is
// never reaped before its window has run.
func (s split) before(argument string) string {
	if s.d == dialect.MySQL {
		return fmt.Sprintf("(%s - INTERVAL sqlc.arg(%s) MICROSECOND)", s.now(), argument)
	}

	return fmt.Sprintf("strftime('%%Y-%%m-%%d %%H:%%M:%%f', 'now', printf('-%%.3f seconds', "+
		"((sqlc.arg(%s) + 999) / 1000) / 1000.0))", argument)
}

// microsecondsSince renders how long ago an instant was, server side, in
// microseconds.
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

// greatest and least are the two-argument maximum and minimum. MySQL spells
// them as Postgres does; SQLite's max and min are the scalar forms when given
// two arguments, and compare the fixed-width instants as the text they are.
func (s split) greatest(a, b string) string {
	if s.d == dialect.MySQL {
		return "GREATEST(" + a + ", " + b + ")"
	}

	return "max(" + a + ", " + b + ")"
}

func (s split) least(a, b string) string {
	if s.d == dialect.MySQL {
		return "LEAST(" + a + ", " + b + ")"
	}

	return "min(" + a + ", " + b + ")"
}

// integer casts an aggregate to the 64-bit integer the queue reads.
func (s split) integer(expression string) string {
	if s.d == dialect.MySQL {
		return "CAST(" + expression + " AS SIGNED)"
	}

	return "CAST(" + expression + " AS INTEGER)"
}

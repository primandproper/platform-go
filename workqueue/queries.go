package workqueue

import (
	"fmt"
	"strings"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
)

// serviceName names the loggers, spans, and metrics this package emits.
const serviceName = "workqueue"

// tableFor renders the queue table name under a namespace. It is the one place
// the component segment is spelled, so the queries and the DDL cannot disagree
// about it.
func tableFor(prefix string) string {
	return ddl.Qualify(prefix) + "work_queue_items"
}

// epoch is the never-leased sentinel.
//
// lease_until is NOT NULL and starts here rather than being nullable, so the
// claim predicate is a single comparison — `lease_until <= now()` covers both
// "never leased" and "lease lapsed" — instead of a comparison plus a NULL
// branch that every future writer would have to remember.
const epoch = "TIMESTAMPTZ 'epoch'"

// micros renders "the bound microsecond count, as an interval". Durations,
// never timestamps, cross the seam into these statements: the offset is applied
// to the server's now(), so a caller's clock never enters the schedule.
func micros(marker string) string {
	return "(" + marker + "::bigint * INTERVAL '1 microsecond')"
}

// claimablePredicate is what makes an item due: not finished, not leased, not
// held back, and not out of attempts. Rendered once and shared by the claim and
// the health read, because a Stats.Ready that disagreed with what Claim will
// actually hand out is worse than no reading at all.
//
// queueMarker binds the queue name and attemptsMarker the attempt ceiling; a
// non-positive ceiling means unlimited, which is the default.
func claimablePredicate(alias, queueMarker, attemptsMarker string) string {
	return fmt.Sprintf(
		"%[1]s.queue_name = %[2]s "+
			"AND %[1]s.completed_at IS NULL "+
			"AND %[1]s.lease_until <= now() "+
			"AND %[1]s.available_at <= now() "+
			"AND (%[3]s::int <= 0 OR %[1]s.attempts < %[3]s::int)",
		alias, queueMarker, attemptsMarker,
	)
}

// encodedEntry is one row's worth of bound parameters, with the key already
// rendered to its stored form.
type encodedEntry struct {
	key         string
	priority    int
	delayMicros int64
}

// buildUpsert renders the enqueue statement.
//
// rows must arrive sorted by key and free of duplicates; sortAndMergeEntries is
// what guarantees both. The sort is the lock-ordering discipline this package's
// documentation opens with — ON CONFLICT DO UPDATE locks each conflicting row as
// the source reaches it, so two overlapping batches arriving in different orders
// deadlock (SQLSTATE 40P01). One total order over the primary key turns that
// cycle into a queue.
//
// Deduplication is not merely an optimization either: ON CONFLICT DO UPDATE
// refuses to touch the same row twice in one statement (SQLSTATE 21000), so a
// caller who names a key twice would otherwise lose the whole batch alongside
// it.
//
// The conflict clause encodes what re-enqueueing means: at least this urgent, at
// least this soon. Priority only rises and availability only moves earlier,
// because an enqueue is a claim on attention and the loudest caller should win.
// A completed item is the exception — it is being restarted, so it takes the new
// schedule outright and its attempt count and last error reset.
//
// lease_until is deliberately untouched. Enqueueing an item somebody is working
// on right now must not revoke their lease.
func buildUpsert(table, queueName string, rows []encodedEntry) (query string, args []any) {
	const columnsPerRow = 4

	args = make([]any, 0, len(rows)*columnsPerRow)
	tuples := make([]string, 0, len(rows))

	for i := range rows {
		p := func(offset int) string { return dialect.Postgres.Placeholder(len(args) + offset) }

		tuples = append(tuples, fmt.Sprintf(
			"(%s, %s, %s, 0, now(), now() + %s, %s)",
			p(1), p(2), p(3), micros(p(4)), epoch,
		))
		args = append(args, queueName, rows[i].key, rows[i].priority, rows[i].delayMicros)
	}

	query = fmt.Sprintf(
		"INSERT INTO %s AS q "+
			"(queue_name, item_key, priority, attempts, enqueued_at, available_at, lease_until) "+
			"VALUES %s "+
			"ON CONFLICT (queue_name, item_key) DO UPDATE SET "+
			"priority = GREATEST(q.priority, excluded.priority), "+
			"available_at = CASE WHEN q.completed_at IS NULL "+
			"THEN LEAST(q.available_at, excluded.available_at) ELSE excluded.available_at END, "+
			"enqueued_at = CASE WHEN q.completed_at IS NULL THEN q.enqueued_at ELSE excluded.enqueued_at END, "+
			"attempts = CASE WHEN q.completed_at IS NULL THEN q.attempts ELSE 0 END, "+
			"last_error = CASE WHEN q.completed_at IS NULL THEN q.last_error ELSE NULL END, "+
			"completed_at = NULL",
		table, strings.Join(tuples, ", "),
	)

	return query, args
}

// buildClaim renders the lease handout: pick the due items, lock the ones
// nobody else is looking at, stamp a lease on them, and return them — one
// statement, so there is no window in which an item is selected but not yet
// leased.
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
// LIMIT pushed into a subquery beneath the lock would still be correct and would
// quietly halve throughput under contention, so this is pinned by a test against
// a real server rather than left to the plan.
//
// The returned flag is the lease's history: prior_lease is above the epoch
// sentinel only when this item was leased before and that lease lapsed rather
// than being released. That is the package's whole failure-recovery mechanism
// firing, and the only place it is observable.
func buildClaim(table string) string {
	return fmt.Sprintf(
		"WITH due AS ("+
			"SELECT q.queue_name, q.item_key, q.lease_until AS prior_lease "+
			"FROM %[1]s AS q "+
			"WHERE %[2]s "+
			"ORDER BY q.priority DESC, q.available_at, q.item_key "+
			"LIMIT $3::int "+
			"FOR UPDATE SKIP LOCKED"+
			") "+
			"UPDATE %[1]s AS q "+
			"SET lease_until = now() + %[3]s, attempts = q.attempts + 1 "+
			"FROM due "+
			"WHERE q.queue_name = due.queue_name AND q.item_key = due.item_key "+
			"RETURNING q.item_key, q.priority, q.attempts, (due.prior_lease > %[4]s)",
		table, claimablePredicate("q", "$1", "$2"), micros("$4"), epoch,
	)
}

// lockedTargets renders the CTE every keyed writer reaches its rows through.
//
// The ORDER BY is the point. `UPDATE … WHERE item_key IN (…)` gives Postgres no
// obligation to take row locks in any particular order, so two writers whose key
// sets overlap can deadlock; an explicitly ordered SELECT … FOR UPDATE makes the
// acquisition order the primary key's, for every writer, always.
//
// extra narrows the set further — Release wants only items that are still
// outstanding — and is appended to the predicate rather than applied afterwards,
// so a row excluded by it is never locked at all.
func lockedTargets(table, extra, keyMarkers string) string {
	predicate := "q.queue_name = $1 AND q.item_key IN (" + keyMarkers + ")"
	if extra != "" {
		predicate += " AND " + extra
	}

	return fmt.Sprintf(
		"WITH target AS ("+
			"SELECT q.queue_name, q.item_key FROM %s AS q WHERE %s "+
			"ORDER BY q.queue_name, q.item_key FOR UPDATE"+
			") ",
		table, predicate,
	)
}

// buildComplete renders the retirement of finished items. Rows are marked rather
// than deleted, so a duplicate or a gap can be investigated after the fact; the
// reaper removes them once they age past Retention.
//
// Keys the queue has never heard of are simply not matched. That is deliberate:
// a straggler whose lease lapsed and whose item was since removed still gets to
// report success without an error nobody could act on.
func buildComplete(table string, keyCount int) string {
	return lockedTargets(table, "", dialect.Postgres.Placeholders(2, keyCount)) +
		fmt.Sprintf(
			"UPDATE %s AS q "+
				"SET completed_at = now(), lease_until = %s, last_error = NULL "+
				"FROM target WHERE q.queue_name = target.queue_name AND q.item_key = target.item_key",
			table, epoch,
		)
}

// buildRelease renders an early lease hand-back: drop the lease, hold the item
// until the delay elapses, and record why.
//
// Already-completed items are excluded rather than resurrected. A late Release
// arriving after somebody else finished the work is the ordinary consequence of
// a lapsed lease, and undoing their Complete would turn that waste into a loop.
func buildRelease(table string, keyCount int) string {
	return lockedTargets(table, "q.completed_at IS NULL", dialect.Postgres.Placeholders(4, keyCount)) +
		fmt.Sprintf(
			"UPDATE %s AS q "+
				"SET lease_until = %s, available_at = now() + %s, last_error = $3 "+
				"FROM target WHERE q.queue_name = target.queue_name AND q.item_key = target.item_key",
			table, epoch, micros("$2"),
		)
}

// buildRemove renders the deletion of named items, whether or not they are
// leased. A worker holding a lease on a removed item finds its Complete matches
// nothing, which is the same outcome as a lapsed lease and needs no extra
// handling.
func buildRemove(table string, keyCount int) string {
	return lockedTargets(table, "", dialect.Postgres.Placeholders(2, keyCount)) +
		fmt.Sprintf(
			"DELETE FROM %s AS q USING target "+
				"WHERE q.queue_name = target.queue_name AND q.item_key = target.item_key",
			table,
		)
}

// buildReap renders the DELETE that removes completed items past the retention
// window, bounded so a long-neglected queue is drained over several passes
// rather than one statement that holds locks for minutes.
//
// SKIP LOCKED here, unlike in the other writers, because the reaper is the one
// writer with nothing to prove: an item another statement is holding will still
// be expired on the next pass.
func buildReap(table string) string {
	return fmt.Sprintf(
		"WITH doomed AS ("+
			"SELECT q.queue_name, q.item_key FROM %[1]s AS q "+
			"WHERE q.queue_name = $1 AND q.completed_at IS NOT NULL AND q.completed_at < now() - %[2]s "+
			"ORDER BY q.queue_name, q.item_key LIMIT $3::int FOR UPDATE SKIP LOCKED"+
			") "+
			"DELETE FROM %[1]s AS q USING doomed "+
			"WHERE q.queue_name = doomed.queue_name AND q.item_key = doomed.item_key",
		table, micros("$2"),
	)
}

// buildStats renders the health read: the queue's shape, plus how long the
// oldest claimable item has been waiting.
//
// All of it in one round trip because no part of it is useful alone. A depth of
// forty thousand is unremarkable if the oldest ready item is four seconds old
// and an incident if it is four hours old, and only the age separates a queue
// that is deep and draining from one that is deep and stuck.
//
// The age is computed server-side, in microseconds, against the same now() the
// counts are measured against — the alternative would be returning a timestamp
// for the caller to subtract from a clock this package has spent its whole
// design avoiding.
func buildStats(table string) string {
	ready := claimablePredicate("q", "$1", "$2")

	return fmt.Sprintf(
		"SELECT "+
			"COALESCE(SUM(CASE WHEN q.completed_at IS NULL THEN 1 ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN %[2]s THEN 1 ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN q.completed_at IS NULL AND q.lease_until > now() THEN 1 ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN q.completed_at IS NULL AND $2::int > 0 "+
			"AND q.attempts >= $2::int THEN 1 ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN q.completed_at IS NOT NULL THEN 1 ELSE 0 END), 0), "+
			"COALESCE((EXTRACT(EPOCH FROM (now() - MIN(CASE WHEN %[2]s THEN q.available_at END))) "+
			"* 1000000)::bigint, 0) "+
			"FROM %[1]s AS q WHERE q.queue_name = $1",
		table, ready,
	)
}

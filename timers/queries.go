package timers

import (
	"fmt"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
)

// serviceName names the loggers, spans, and metrics this package emits.
const serviceName = "timers"

// tableFor renders the timer table name under a namespace. It is the one place
// the component segment is spelled, so the queries and the DDL cannot disagree
// about it.
func tableFor(prefix string) string {
	return ddl.Qualify(prefix) + "scheduled_timers"
}

// epoch is the never-leased sentinel.
//
// lease_until is NOT NULL and starts here rather than being nullable, so the due
// predicate is a single comparison — `lease_until <= now()` covers both "never
// leased" and "lease lapsed" — instead of a comparison plus a NULL branch that
// every future writer would have to remember.
const epoch = "TIMESTAMPTZ 'epoch'"

// micros renders "the bound microsecond count, as an interval". A lease and a
// retry delay are offsets from the server's now(), so they cross the seam as
// durations; run_at is the one timestamp bound absolutely, because it is the
// thing the caller actually meant.
func micros(marker string) string {
	return "(" + marker + "::bigint * INTERVAL '1 microsecond')"
}

// microsSince renders "how many microseconds have passed since expr", server
// side, as a bigint. Negative when expr is in the future.
func microsSince(expr string) string {
	return "(EXTRACT(EPOCH FROM (now() - " + expr + ")) * 1000000)::bigint"
}

// duePredicate is what makes a timer ready to fire: not yet fired, not leased,
// its instant reached, and not out of attempts. Rendered once and shared by the
// claim, the next-due read, and the health read, because a Stats.Due that
// disagreed with what Claim will actually hand out is worse than no reading at
// all.
//
// setMarker binds the set name and attemptsMarker the attempt ceiling; a
// non-positive ceiling means unlimited.
func duePredicate(alias, setMarker, attemptsMarker string) string {
	return fmt.Sprintf(
		"%[1]s.timer_set = %[2]s "+
			"AND %[1]s.fired_at IS NULL "+
			"AND %[1]s.lease_until <= now() "+
			"AND %[1]s.run_at <= now() "+
			"AND (%[3]s::int <= 0 OR %[1]s.attempts < %[3]s::int)",
		alias, setMarker, attemptsMarker,
	)
}

// outstandingPredicate is duePredicate with the two time comparisons dropped: a
// timer that has not fired and has attempts left, whenever it is meant to run.
// It is the set the next-due read measures over, because the answer it wants is
// "how long until one of these becomes due", and a row excluded for not being
// due yet is the entire question.
func outstandingPredicate(alias, setMarker, attemptsMarker string) string {
	return fmt.Sprintf(
		"%[1]s.timer_set = %[2]s "+
			"AND %[1]s.fired_at IS NULL "+
			"AND (%[3]s::int <= 0 OR %[1]s.attempts < %[3]s::int)",
		alias, setMarker, attemptsMarker,
	)
}

// encodedTimer is one row's worth of bound parameters, with the key already
// rendered to its stored form.
type encodedTimer struct {
	runAt   time.Time
	key     string
	payload []byte
}

// buildSchedule renders the write that puts timers in the table.
//
// rows must arrive sorted by key and free of duplicates; sortAndMergeTimers is
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
// The conflict clause is where this parts company with a work queue's. Enqueuing
// the same key twice means "at least this urgent, at least this soon", so
// availability only ever moves earlier. Scheduling the same key twice means
// "actually, then" — a trial extended by a week has to be able to move later —
// so the new instant wins outright, and the attempt count and last error reset
// with it because this is a fresh schedule rather than a retry of the old one.
//
// The lease is revoked if and only if the instant actually moved, which is the
// one place those two cases have to be told apart.
//
// A move frees the row immediately. The worker holding the lease is firing a
// schedule that no longer exists — the run_at fence already stops their Complete
// from landing, see buildComplete — so leaving the lease in place would only
// make the new schedule wait out a lease nothing can still discharge.
//
// A reschedule to the same instant is not a move, and revoking there would be
// actively harmful: an at-least-once upstream redelivering "start trial" would
// free a row somebody is firing right now, and a second worker would claim and
// fire it while the first was still going. Left alone, the first worker's
// Complete matches and retires it exactly once.
func buildSchedule(table, setName string, rows []encodedTimer) (query string, args []any) {
	const columnsPerRow = 4

	args = make([]any, 0, len(rows)*columnsPerRow)
	tuples := make([]string, 0, len(rows))

	for i := range rows {
		p := func(offset int) string { return dialect.Postgres.Placeholder(len(args) + offset) }

		tuples = append(tuples, fmt.Sprintf(
			"(%s, %s, %s::timestamptz, %s::bytea, 0, now(), %s)",
			p(1), p(2), p(3), p(4), epoch,
		))
		args = append(args, setName, rows[i].key, rows[i].runAt, rows[i].payload)
	}

	query = fmt.Sprintf(
		"INSERT INTO %s AS s "+
			"(timer_set, timer_key, run_at, payload, attempts, scheduled_at, lease_until) "+
			"VALUES %s "+
			"ON CONFLICT (timer_set, timer_key) DO UPDATE SET "+
			"run_at = excluded.run_at, "+
			"payload = excluded.payload, "+
			"scheduled_at = now(), "+
			"attempts = 0, "+
			"last_error = NULL, "+
			"fired_at = NULL, "+
			"lease_until = CASE WHEN s.run_at IS DISTINCT FROM excluded.run_at "+
			"THEN %s ELSE s.lease_until END",
		table, strings.Join(tuples, ", "), epoch,
	)

	return query, args
}

// buildClaim renders the lease handout: pick the timers that have come due, lock
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
// subtraction against the reader's clock.
func buildClaim(table string) string {
	return fmt.Sprintf(
		"WITH due AS ("+
			"SELECT s.timer_set, s.timer_key, s.lease_until AS prior_lease "+
			"FROM %[1]s AS s "+
			"WHERE %[2]s "+
			"ORDER BY s.run_at, s.timer_key "+
			"LIMIT $3::int "+
			"FOR UPDATE SKIP LOCKED"+
			") "+
			"UPDATE %[1]s AS s "+
			"SET lease_until = now() + %[3]s, attempts = s.attempts + 1 "+
			"FROM due "+
			"WHERE s.timer_set = due.timer_set AND s.timer_key = due.timer_key "+
			"RETURNING s.timer_key, s.payload, s.run_at, %[5]s, s.attempts, (due.prior_lease > %[4]s)",
		table, duePredicate("s", "$1", "$2"), micros("$4"), epoch, microsSince("s.run_at"),
	)
}

// buildNextDue renders the sleep hint: how long until the nearest outstanding
// timer can be claimed, and whether there is one at all.
//
// The instant measured is GREATEST(run_at, lease_until), which is when the row
// next becomes claimable rather than when it was meant to run. For an unleased
// row the two are the same, because lease_until is the epoch; for a leased one
// it is the lease's expiry, so a poller whose fleet-mate has died sleeps until
// the lease lapses instead of through it. That is the difference between a
// stalled firing recovering in one lease and recovering on the poll backstop.
//
// It is a whole-set aggregate rather than an indexed lookup: the partial index
// on (timer_set, run_at) cannot serve a MIN over a GREATEST. The set it scans is
// the outstanding backlog, which is the small one — everything fired is excluded
// by the same partial predicate the index is built on — so this is a read over
// the work, not over the history.
func buildNextDue(table string) string {
	return fmt.Sprintf(
		"SELECT COUNT(*), "+
			"COALESCE(-%[3]s, 0) "+
			"FROM %[1]s AS s WHERE %[2]s",
		table, outstandingPredicate("s", "$1", "$2"),
		microsSince("MIN(GREATEST(s.run_at, s.lease_until))"),
	)
}

// lockedTargets renders the CTE every keyed writer reaches its rows through.
//
// The ORDER BY is the point. `UPDATE … WHERE timer_key IN (…)` gives Postgres no
// obligation to take row locks in any particular order, so two writers whose key
// sets overlap can deadlock; an explicitly ordered SELECT … FOR UPDATE makes the
// acquisition order the primary key's, for every writer, always.
func lockedTargets(table, match string) string {
	return fmt.Sprintf(
		"WITH target AS ("+
			"SELECT s.timer_set, s.timer_key FROM %s AS s WHERE s.timer_set = $1 AND %s "+
			"ORDER BY s.timer_set, s.timer_key FOR UPDATE"+
			") ",
		table, match,
	)
}

// firedTuples renders the row-value list that matches a batch of firings — each
// one a key together with the exact instant the claimant was handed.
//
// The instant is what makes a firing addressable rather than just its key, and
// it is the whole of this package's answer to the reschedule race. A Complete
// carrying a stale run_at matches nothing, so a timer moved while it was being
// fired keeps its new schedule instead of being marked fired against the old
// one. That is the same "matches nothing" outcome a lapsed lease already
// produces, so it needs no new handling anywhere.
func firedTuples(start, count int) string {
	tuples := make([]string, 0, count)
	for i := range count {
		tuples = append(tuples, fmt.Sprintf("(%s, %s::timestamptz)",
			dialect.Postgres.Placeholder(start+(2*i)), dialect.Postgres.Placeholder(start+(2*i)+1)))
	}

	return "(s.timer_key, s.run_at) IN (" + strings.Join(tuples, ", ") + ")"
}

// buildComplete renders the retirement of fired timers. Rows are marked rather
// than deleted, so "did the expiry run, and when" stays answerable after the
// fact; the reaper removes them once they age past Retention.
//
// Firings the set does not recognize are simply not matched — a straggler whose
// lease lapsed, whose timer was cancelled, or whose schedule moved underneath it
// has nothing useful to do with an error.
func buildComplete(table string, count int) string {
	return lockedTargets(table, firedTuples(2, count)) +
		fmt.Sprintf(
			"UPDATE %s AS s "+
				"SET fired_at = now(), lease_until = %s, last_error = NULL "+
				"FROM target WHERE s.timer_set = target.timer_set AND s.timer_key = target.timer_key",
			table, epoch,
		)
}

// buildRelease renders an early lease hand-back: drop the lease, push the timer
// out by the delay, and record why.
//
// Pushing run_at forward rather than holding it behind a separate availability
// column is deliberate — this table has one instant per row, and a retried timer
// genuinely is now scheduled for later. The cost is that Due.Late is measured
// against the retry's instant rather than the original, so a timer that has been
// retried five times does not look five delays late. Stats.Stalled is what
// surfaces that instead.
//
// Already-fired rows are excluded rather than resurrected, and the run_at fence
// applies here exactly as it does to Complete: a release against a schedule that
// has since moved must not drag the new one backwards.
func buildRelease(table string, count int) string {
	return lockedTargets(table, "s.fired_at IS NULL AND "+firedTuples(4, count)) +
		fmt.Sprintf(
			"UPDATE %s AS s "+
				"SET lease_until = %s, run_at = now() + %s, last_error = $3 "+
				"FROM target WHERE s.timer_set = target.timer_set AND s.timer_key = target.timer_key",
			table, epoch, micros("$2"),
		)
}

// buildCancel renders the deletion of named timers, whether or not they are
// leased and whether or not they have fired.
//
// It deletes rather than marking, because a cancelled timer has no history worth
// keeping: nobody asks when a reminder that was called off would have gone out.
// The row count it reports is the useful part — it is how a caller learns
// whether the cancel beat the firing.
func buildCancel(table string, keyCount int) string {
	return lockedTargets(table, "s.timer_key IN ("+dialect.Postgres.Placeholders(2, keyCount)+")") +
		fmt.Sprintf(
			"DELETE FROM %s AS s USING target "+
				"WHERE s.timer_set = target.timer_set AND s.timer_key = target.timer_key",
			table,
		)
}

// buildReap renders the DELETE that removes fired timers past the retention
// window, bounded so a long-neglected set is drained over several passes rather
// than one statement that holds locks for minutes.
//
// SKIP LOCKED here, unlike in the other writers, because the reaper is the one
// writer with nothing to prove: a row another statement is holding will still be
// expired on the next pass.
func buildReap(table string) string {
	return fmt.Sprintf(
		"WITH doomed AS ("+
			"SELECT s.timer_set, s.timer_key FROM %[1]s AS s "+
			"WHERE s.timer_set = $1 AND s.fired_at IS NOT NULL AND s.fired_at < now() - %[2]s "+
			"ORDER BY s.timer_set, s.timer_key LIMIT $3::int FOR UPDATE SKIP LOCKED"+
			") "+
			"DELETE FROM %[1]s AS s USING doomed "+
			"WHERE s.timer_set = doomed.timer_set AND s.timer_key = doomed.timer_key",
		table, micros("$2"),
	)
}

// buildStats renders the health read: the set's shape, plus how late the oldest
// unfired due timer already is.
//
// All of it in one round trip because no part of it is useful alone. Ten
// thousand outstanding timers is unremarkable if none of them is due yet and an
// incident if the oldest came due four hours ago, and only the lateness
// separates a set that is large from one that has stopped firing.
//
// The lateness is computed server-side, in microseconds, against the same now()
// the counts are measured against — the alternative would be returning a
// timestamp for the caller to subtract from a clock that has no say in when a
// timer is due.
func buildStats(table string) string {
	due := duePredicate("s", "$1", "$2")

	return fmt.Sprintf(
		"SELECT "+
			"COALESCE(SUM(CASE WHEN s.fired_at IS NULL THEN 1 ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN %[2]s THEN 1 ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN s.fired_at IS NULL AND s.lease_until > now() THEN 1 ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN s.fired_at IS NULL AND $2::int > 0 "+
			"AND s.attempts >= $2::int THEN 1 ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN s.fired_at IS NOT NULL THEN 1 ELSE 0 END), 0), "+
			"COALESCE(%[3]s, 0) "+
			"FROM %[1]s AS s WHERE s.timer_set = $1",
		table, due, microsSince("MIN(CASE WHEN "+due+" THEN s.run_at END)"),
	)
}

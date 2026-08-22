package metering

import (
	"fmt"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
)

// A note on timestamps, because one dialect does something surprising.
//
// Every time this package binds is a UTC time.Time, and every comparison is
// against another such value. Postgres and MySQL store these as real temporal
// types and compare them as such. SQLite does not: modernc's driver stores a
// bound time.Time as Go's own String() rendering — "2026-07-31 12:00:00 +0000
// UTC" — so `next_flush <= ?` there is a string comparison, and so is the
// GREATEST that keeps last_occurred_at monotone.
//
// That is still correct, because the rendering begins with a fixed-width
// "YYYY-MM-DD HH:MM:SS" prefix and everything is UTC, so lexical order is
// chronological order. It stops being correct the moment a value is bound in a
// non-UTC location, so do not remove the .UTC() calls at the binding sites.

// tables holds the rendered table names. Derived from one prefix so that adding
// a third table later cannot introduce an inconsistently named one — see
// metering/migrations.
type tables struct {
	base   string
	events string
	totals string
}

func newTables(prefix string) *tables {
	return &tables{
		base:   prefix,
		events: ddl.Qualify(prefix) + "metering_events",
		totals: ddl.Qualify(prefix) + "metering_totals",
	}
}

// prefix returns the prefix the names were derived from, for the validation that
// has to run against every rendered name rather than against any one.
func (t *tables) prefix() string {
	return t.base
}

// totalColumns is the projection every total read scans. Declared once so the
// SELECTs and the Scan cannot drift apart.
const totalColumns = "subject, meter, period_start, period_end, aggregation, quantity, " +
	"last_occurred_at, flushed_quantity, flush_sequence, flush_attempts, next_flush, last_error"

// totalKeyColumns is the composite primary key of the totals table, in the order
// every predicate and tuple binds it.
const totalKeyColumns = "subject, meter, period_start"

// buildInsertEvent renders the ingest ledger write for one usage record.
//
// The conflict clause is the dedupe, and it is the only one this package has. A
// key that is already in the table takes no row and reports zero rows affected,
// which is how the caller learns the usage was already counted — decided by the
// database, in one round trip, durably, and for as long as the row is retained.
//
// One row per statement rather than a multi-row INSERT, deliberately. A multi-row
// insert with conflict-ignore reports how many rows it took but not which ones,
// and folding a batch into its totals requires knowing exactly which records were
// new. Guessing is how usage gets counted twice, so the batch pays a statement
// per record and the fold that follows is grouped — a thousand records for one
// subject and period cost a thousand inserts and one total update.
func (t *tables) buildInsertEvent(d dialect.Dialect, e *Entry, dimensions []byte, at time.Time) (query string, args []any) {
	args = []any{
		e.IdempotencyKey, e.Subject, e.Meter, e.Quantity,
		e.OccurredAt.UTC(), at.UTC(), e.Bounds.Start.UTC(), database.BlobOrNil(dimensions),
	}

	query = fmt.Sprintf(
		"INSERT %sINTO %s (idempotency_key, subject, meter, quantity, occurred_at, recorded_at, period_start, dimensions) "+
			"VALUES (%s)",
		ignorePrefix(d), t.events, d.Placeholders(1, len(args)),
	)

	if d == dialect.Postgres {
		// The conflict target is the events table's primary key, which is scoped
		// to the meter: one request routinely feeds several meters, and deduping
		// on the key alone silently drops every meter after the first.
		query += " ON CONFLICT (meter, idempotency_key) DO NOTHING"
	}

	return query, args
}

// ignorePrefix renders the dialect's way of spelling "skip a row that is already
// there" in the INSERT verb itself. Postgres spells it as a trailing ON CONFLICT
// clause instead, which the callers append.
func ignorePrefix(d dialect.Dialect) string {
	switch d {
	case dialect.MySQL:
		return "IGNORE "
	case dialect.SQLite:
		return "OR IGNORE "
	case dialect.Postgres:
		return ""
	default:
		return ""
	}
}

// buildUpsertTotal renders the fold of one group of accepted records into its
// period's total.
//
// The arithmetic is in the statement rather than in a read-modify-write, and that
// is what makes concurrent ingest safe. Two recorders folding into the same
// period at the same instant would otherwise both read the same total, both add
// their own quantity to it, and between them lose one of the two — silently, and
// in the direction that under-bills.
//
// quantity is the group's already-folded contribution: summed for a sum meter,
// maxed for a max meter, the latest for a last meter. Folding the group in Go and
// the group into the row in SQL means one statement per group rather than per
// record, and the two foldings are the same function — Aggregation.Fold — so they
// cannot disagree about what the aggregation means.
func (t *tables) buildUpsertTotal(
	d dialect.Dialect,
	subject, meter string,
	aggregation Aggregation,
	bounds Bounds,
	quantity int64,
	lastOccurredAt, at time.Time,
) (query string, args []any) {
	args = []any{
		subject, meter, bounds.Start.UTC(), bounds.End.UTC(), string(aggregation),
		quantity, lastOccurredAt.UTC(), at.UTC(), at.UTC(),
	}

	insert := fmt.Sprintf(
		"INSERT INTO %s (subject, meter, period_start, period_end, aggregation, quantity, "+
			"last_occurred_at, next_flush, updated_at) VALUES (%s)",
		t.totals, d.Placeholders(1, len(args)),
	)

	existing, incoming := conflictRefs(d, t.totals)

	// last_occurred_at only ever moves forward. It is what AggregationLast orders
	// by, so a record that arrives late — a queue redelivering an hour behind —
	// must not drag the row's notion of "latest" backwards and let the next
	// out-of-order record win.
	sets := []string{
		fmt.Sprintf("last_occurred_at = %s(%s, %s)",
			greatestFunc(d), existing("last_occurred_at"), incoming("last_occurred_at")),
		fmt.Sprintf("updated_at = %s", incoming("updated_at")),
	}

	switch aggregation {
	case AggregationSum:
		sets = append(sets, fmt.Sprintf("quantity = %s + %s", existing("quantity"), incoming("quantity")))
	case AggregationMax:
		sets = append(sets, fmt.Sprintf("quantity = %s(%s, %s)",
			greatestFunc(d), existing("quantity"), incoming("quantity")))
	case AggregationLast:
		// Guarded on the event time rather than applied unconditionally, so an
		// out-of-order arrival leaves the newer reading standing.
		sets = append(sets, fmt.Sprintf(
			"quantity = CASE WHEN %s >= %s THEN %s ELSE %s END",
			incoming("last_occurred_at"), existing("last_occurred_at"),
			incoming("quantity"), existing("quantity"),
		))
	case AggregationUniqueCount:
		// Unreachable: registration refuses it. Left as an explicit arm so that
		// adding an aggregation cannot silently fall through to "leave the total
		// alone", which reads on a dashboard as a meter that stopped counting.
		sets = append(sets, fmt.Sprintf("quantity = %s", existing("quantity")))
	default:
		sets = append(sets, fmt.Sprintf("quantity = %s", existing("quantity")))
	}

	return insert + conflictClause(d, totalKeyColumns) + strings.Join(sets, ", "), args
}

// conflictRefs returns the two renderings an upsert's SET clause needs: how the
// dialect names the row already in the table, and how it names the one being
// inserted.
//
// MySQL is the odd one. Postgres and SQLite expose the incoming row under a
// pseudo-table, and the existing row by its real name; MySQL exposes the incoming
// row through VALUES() and the existing row as a bare column. The three are
// spelled differently and mean the same thing, which is exactly the sort of
// difference that belongs in one function rather than in every query.
func conflictRefs(d dialect.Dialect, table string) (existing, incoming func(string) string) {
	if d == dialect.MySQL {
		return func(col string) string { return col },
			func(col string) string { return "VALUES(" + col + ")" }
	}

	return func(col string) string { return table + "." + col },
		func(col string) string { return "excluded." + col }
}

// conflictClause renders the "and if the row is already there" preamble, up to
// and including the SET keyword.
func conflictClause(d dialect.Dialect, keyColumns string) string {
	if d == dialect.MySQL {
		return " ON DUPLICATE KEY UPDATE "
	}

	return " ON CONFLICT (" + keyColumns + ") DO UPDATE SET "
}

// greatestFunc renders the dialect's two-argument maximum. SQLite spells it MAX,
// which in scalar position — two arguments rather than one — is the same function
// the other two call GREATEST.
func greatestFunc(d dialect.Dialect) string {
	if d == dialect.SQLite {
		return "MAX"
	}

	return "GREATEST"
}

// buildInsertZeroTotal renders the placeholder row Consume locks.
//
// Locking a row requires a row. A subject's first consume in a period has none,
// and two concurrent first consumes would both find nothing to lock, both decide
// against a total of zero, and both take the last unit under the limit. Inserting
// a zero row first — conflict-ignored, so the loser of that race simply proceeds —
// gives the subsequent SELECT ... FOR UPDATE something to serialize on.
func (t *tables) buildInsertZeroTotal(
	d dialect.Dialect,
	subject, meter string,
	aggregation Aggregation,
	bounds Bounds,
	at time.Time,
) (query string, args []any) {
	args = []any{
		subject, meter, bounds.Start.UTC(), bounds.End.UTC(), string(aggregation),
		bounds.Start.UTC(), at.UTC(), at.UTC(),
	}

	query = fmt.Sprintf(
		"INSERT %sINTO %s (subject, meter, period_start, period_end, aggregation, quantity, "+
			"last_occurred_at, next_flush, updated_at) VALUES (%s, %s, %s, %s, %s, 0, %s, %s, %s)",
		ignorePrefix(d), t.totals,
		d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), d.Placeholder(4),
		d.Placeholder(5), d.Placeholder(6), d.Placeholder(7), d.Placeholder(8),
	)

	if d == dialect.Postgres {
		query += " ON CONFLICT (" + totalKeyColumns + ") DO NOTHING"
	}

	return query, args
}

// buildSelectTotal renders the read of one subject's total for a period.
//
// forUpdate is what serializes two transactions consuming from the same total.
// The row is locked for the remainder of the caller's transaction, so the second
// consumer blocks here and then reads the total the first one committed rather
// than deciding against a stale one. Postgres and MySQL both take a row lock;
// SQLite has no FOR UPDATE and needs none, since it admits one writer at a time
// by construction.
func (t *tables) buildSelectTotal(
	d dialect.Dialect,
	subject, meter string,
	periodStart time.Time,
	forUpdate bool,
) (query string, args []any) {
	query = fmt.Sprintf(
		"SELECT %s FROM %s WHERE subject = %s AND meter = %s AND period_start = %s",
		totalColumns, t.totals, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3),
	)

	if forUpdate && supportsRowLock(d) {
		query += " FOR UPDATE"
	}

	return query, []any{subject, meter, periodStart.UTC()}
}

// buildEventExists renders the read-only dedupe probe: has this (meter,
// idempotency_key) already been counted?
//
// It exists so that a consume which is about to be refused can find out whether
// it is a retry of one that already succeeded, without writing anything. The
// insert-based probe cannot answer that question on the refusal path, because
// the refusal path deliberately writes nothing.
func (t *tables) buildEventExists(d dialect.Dialect, meter, idempotencyKey string) (query string, args []any) {
	query = fmt.Sprintf(
		"SELECT 1 FROM %s WHERE meter = %s AND idempotency_key = %s",
		t.events, d.Placeholder(1), d.Placeholder(2),
	)

	return query, []any{meter, idempotencyKey}
}

// supportsRowLock reports whether the dialect can take an explicit row lock with
// FOR UPDATE.
//
// It happens to select the same two dialects as Dialect.SupportsSkipLocked, and
// is written out separately anyway: that method answers whether competing workers
// can skip past locked rows, which is a different question that only
// coincidentally has the same answer today.
func supportsRowLock(d dialect.Dialect) bool {
	return d == dialect.Postgres || d == dialect.MySQL
}

// buildApplyConsume renders the total update Consume runs once it has decided,
// against a row it already holds the lock on.
//
// A plain UPDATE rather than an upsert, because the row is guaranteed to exist —
// buildInsertZeroTotal put it there — and because the decision has already been
// made against the locked value. Re-deriving it in the statement would be a
// second opinion nobody asked for.
func (t *tables) buildApplyConsume(
	d dialect.Dialect,
	subject, meter string,
	periodStart time.Time,
	quantity int64,
	lastOccurredAt, at time.Time,
) (query string, args []any) {
	args = []any{quantity, lastOccurredAt.UTC(), at.UTC(), subject, meter, periodStart.UTC()}

	return fmt.Sprintf(
		"UPDATE %s SET quantity = %s, last_occurred_at = %s(last_occurred_at, %s), updated_at = %s "+
			"WHERE subject = %s AND meter = %s AND period_start = %s",
		t.totals, d.Placeholder(1), greatestFunc(d), d.Placeholder(2), d.Placeholder(3),
		d.Placeholder(4), d.Placeholder(5), d.Placeholder(6),
	), args
}

// buildSelectFlushable renders the query picking the next batch of totals to
// post: those that owe the provider something, whose retry time has come, which
// nobody currently holds, and which have not exhausted their attempts.
//
// The quantity > flushed_quantity comparison is between two columns, which no
// index can serve — which is why the Postgres and SQLite schemas make it the
// partial index's predicate instead, so the index contains only the rows this
// query wants.
func (t *tables) buildSelectFlushable(
	d dialect.Dialect,
	now time.Time,
	limit, maxAttempts int,
	skipLocked bool,
) (query string, args []any) {
	query = fmt.Sprintf(
		"SELECT %s FROM %s "+
			"WHERE quantity > flushed_quantity AND next_flush <= %s AND flush_attempts < %s "+
			"AND (claimed_until IS NULL OR claimed_until <= %s) "+
			"ORDER BY next_flush, subject, meter LIMIT %s",
		totalKeyColumns, t.totals,
		d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), d.Placeholder(4),
	)

	if skipLocked && d.SupportsSkipLocked() {
		query += " FOR UPDATE SKIP LOCKED"
	}

	return query, []any{now.UTC(), maxAttempts, now.UTC(), limit}
}

// buildClaimFlushable renders the UPDATE that leases the selected totals.
//
// The attempt count is incremented here rather than on failure: a flusher that
// crashes mid-post has still consumed an attempt, so a total whose provider call
// reliably kills the process eventually gives up rather than being reclaimed
// forever. The flushable guard is repeated even though the rows were just
// selected, because between the SELECT and this UPDATE another flusher's
// MarkFlushed may have settled them.
func (t *tables) buildClaimFlushable(d dialect.Dialect, keys []totalKey, claimedUntil time.Time) (query string, args []any) {
	args = make([]any, 0, len(keys)*3+2)
	args = append(args, claimedUntil.UTC())

	tuples, tupleArgs := keyTuples(d, keys, len(args)+1)
	args = append(args, tupleArgs...)

	return fmt.Sprintf(
		"UPDATE %s SET claimed_until = %s, flush_attempts = flush_attempts + 1 "+
			"WHERE (%s) IN (%s) AND quantity > flushed_quantity",
		t.totals, d.Placeholder(1), totalKeyColumns, tuples,
	), args
}

// buildFetchTotalsByKey renders the projection of a claimed batch.
func (t *tables) buildFetchTotalsByKey(d dialect.Dialect, keys []totalKey) (query string, args []any) {
	tuples, args := keyTuples(d, keys, 1)

	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE (%s) IN (%s) ORDER BY next_flush, subject, meter",
		totalColumns, t.totals, totalKeyColumns, tuples,
	), args
}

// totalKey identifies one row of the totals table.
type totalKey struct {
	periodStart time.Time
	subject     string
	meter       string
}

// keyTuples renders a row-value IN list for a set of composite keys, starting at
// the given placeholder index.
//
// Row values rather than a disjunction of three-way ANDs: the tuple form is one
// clause the planner can match against the composite primary key, where the
// expanded form is a chain of ORs it generally cannot.
func keyTuples(d dialect.Dialect, keys []totalKey, start int) (tuples string, args []any) {
	args = make([]any, 0, len(keys)*3)
	rendered := make([]string, 0, len(keys))

	for i := range keys {
		k := &keys[i]
		rendered = append(rendered, "("+d.Placeholders(start+len(args), 3)+")")
		args = append(args, k.subject, k.meter, k.periodStart.UTC())
	}

	return strings.Join(rendered, ", "), args
}

// buildMarkFlushed renders the settle of a successful post.
//
// The sequence guard is what stops a flusher whose lease lapsed mid-post from
// advancing a sequence a second flusher has already moved. That race is the one
// failure this package cannot repair after the fact: two posts of the same delta
// under two different keys are two charges, and no idempotency key undoes the
// second one.
func (t *tables) buildMarkFlushed(d dialect.Dialect, total *Total, flushed int64, at time.Time) (query string, args []any) {
	args = []any{
		flushed, at.UTC(), at.UTC(),
		total.Subject, total.Meter, total.PeriodStart.UTC(), total.FlushSequence,
	}

	return fmt.Sprintf(
		"UPDATE %s SET flushed_quantity = %s, flush_sequence = flush_sequence + 1, flush_attempts = 0, "+
			"next_flush = %s, claimed_until = NULL, last_error = '', updated_at = %s "+
			"WHERE subject = %s AND meter = %s AND period_start = %s AND flush_sequence = %s",
		t.totals, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3),
		d.Placeholder(4), d.Placeholder(5), d.Placeholder(6), d.Placeholder(7),
	), args
}

// buildReleaseFlush renders the return of a total to the flushable set after a
// failed post: drop the lease, record why, and schedule the retry.
//
// flushed_quantity is deliberately untouched. The post may have reached the
// provider and failed on the way back, so the next attempt has to carry the same
// delta under the same sequence — which is the whole reason the sequence is the
// key's varying component rather than a timestamp.
func (t *tables) buildReleaseFlush(d dialect.Dialect, total *Total, lastErr string, nextFlush, at time.Time) (query string, args []any) {
	args = []any{
		nextFlush.UTC(), lastErr, at.UTC(),
		total.Subject, total.Meter, total.PeriodStart.UTC(), total.FlushSequence,
	}

	return fmt.Sprintf(
		"UPDATE %s SET next_flush = %s, last_error = %s, claimed_until = NULL, updated_at = %s "+
			"WHERE subject = %s AND meter = %s AND period_start = %s AND flush_sequence = %s",
		t.totals, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3),
		d.Placeholder(4), d.Placeholder(5), d.Placeholder(6), d.Placeholder(7),
	), args
}

// buildReapEvents renders the DELETE removing event rows past retention.
//
// The NOT EXISTS is what keeps retention from destroying evidence somebody is
// still going to need. An event row whose period still owes the provider usage is
// the only record of what that unposted total is made of — and, worse, deleting
// it re-opens the idempotency key it held, so a redelivery of that same event
// would be counted a second time into a total that has not yet been invoiced.
func (t *tables) buildReapEvents(d dialect.Dialect, before time.Time, limit int) (query string, args []any) {
	args = []any{before.UTC(), limit}

	inner := fmt.Sprintf(
		"SELECT idempotency_key FROM %s e WHERE e.recorded_at < %s "+
			"AND NOT EXISTS (SELECT 1 FROM %s t WHERE t.subject = e.subject AND t.meter = e.meter "+
			"AND t.period_start = e.period_start AND t.quantity > t.flushed_quantity) LIMIT %s",
		t.events, d.Placeholder(1), t.totals, d.Placeholder(2),
	)

	// MySQL refuses a subquery that reads the table being updated
	// (ER_UPDATE_TABLE_USED), but accepts it once materialized through a derived
	// table.
	if d == dialect.MySQL {
		inner = fmt.Sprintf("SELECT idempotency_key FROM (%s) AS doomed", inner)
	}

	return fmt.Sprintf("DELETE FROM %s WHERE idempotency_key IN (%s)", t.events, inner), args
}

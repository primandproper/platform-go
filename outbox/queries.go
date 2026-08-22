package outbox

import (
	"fmt"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"
)

// serviceName names the loggers, spans, and metrics this package emits.
const serviceName = "outbox"

// messageColumns is the projection the Relay scans. Declared once so the
// SELECT and the Scan cannot drift apart.
const messageColumns = "id, topic, partition_key, payload, attempts"

// A note on timestamps, because one dialect does something surprising.
//
// Every time this package binds is a UTC time.Time, and every comparison is
// against another such value. Postgres and MySQL store these as real temporal
// types and compare them as such. SQLite does not: modernc's driver stores a
// bound time.Time as Go's own String() rendering — "2026-07-27 12:00:00 +0000
// UTC" — so `next_attempt <= ?` there is a string comparison.
//
// That is still correct, but for a reason worth writing down: the rendering
// begins with a fixed-width "YYYY-MM-DD HH:MM:SS" prefix and everything is UTC,
// so lexical order is chronological order. Sub-second values sort correctly too,
// since a fractional part starts with '.' (0x2E) and a whole second continues
// with ' ' (0x20), placing the whole second first.
//
// It stops being correct the moment a value is bound in a non-UTC location, so
// do not remove the .UTC() calls at the binding sites.

// buildInsert renders a multi-row INSERT for the supplied rows. New messages
// are immediately eligible: next_attempt is their creation time.
//
// Every row in one call shares a created_at, so created_at alone does not order
// two messages enqueued together. What separates them is id: identifiers.New is
// xid, whose string form sorts in generation order, so rows come out of one
// Enqueue with ascending ids in the order the caller passed them. Both
// buildSelectClaimable's per-key predicate and the ORDER BY clauses here rely
// on that — see buildSelectClaimable for what breaks without it.
func buildInsert(d dialect.Dialect, table string, rows []enqueueRow) (query string, args []any) {
	// created_at is bound twice: a new message is eligible immediately, so its
	// first next_attempt is its creation time.
	const columnsPerRow = 6

	args = make([]any, 0, len(rows)*columnsPerRow)
	tuples := make([]string, 0, len(rows))

	for i := range rows {
		tuples = append(tuples, "("+d.Placeholders(len(args)+1, columnsPerRow)+")")
		args = append(args,
			rows[i].id, rows[i].topic, rows[i].key, rows[i].payload, rows[i].createdAt, rows[i].createdAt,
		)
	}

	query = fmt.Sprintf(
		"INSERT INTO %s (id, topic, partition_key, payload, created_at, next_attempt) VALUES %s",
		table, strings.Join(tuples, ", "),
	)

	return query, args
}

// buildSelectClaimable renders the query that picks the next batch of message
// IDs to claim.
//
// The ordering guarantee lives in this predicate. A row with a partition key is
// claimable only when no earlier unpublished row shares that key, so at most one
// row per key is ever in flight across every relay in the fleet — keyed messages
// are strictly ordered even under concurrent ClaimSkipLocked relays. Unkeyed
// rows skip the check entirely and claim freely.
//
// "Earlier" is (created_at, id), not created_at alone, and the tuple is what
// makes the guarantee hold. Enqueue stamps every row in one call with a single
// timestamp, so two messages sharing a key and an Enqueue also share a
// created_at; under a bare `<` neither would block the other, both would be
// claimable at once, and a failure on the first would publish the second ahead
// of it. The tiebreak is id because that is what ORDER BY breaks ties on below
// — the predicate and the publish order have to agree on "earlier" or the
// batch can contain a pair it is about to reorder.
func buildSelectClaimable(d dialect.Dialect, table string, now time.Time, limit int, skipLocked bool) (query string, args []any) {
	p := func(n int) string { return d.Placeholder(n) }

	query = fmt.Sprintf(
		"SELECT id FROM %s AS m "+
			"WHERE m.published_at IS NULL AND m.quarantined = FALSE "+
			"AND m.next_attempt <= %s "+
			"AND (m.claimed_until IS NULL OR m.claimed_until <= %s) "+
			"AND (m.partition_key = '' OR NOT EXISTS ("+
			"SELECT 1 FROM %s AS prior "+
			"WHERE prior.partition_key = m.partition_key "+
			"AND prior.published_at IS NULL "+
			"AND prior.quarantined = FALSE "+
			"AND (prior.created_at < m.created_at "+
			"OR (prior.created_at = m.created_at AND prior.id < m.id)))) "+
			"ORDER BY m.created_at, m.id LIMIT %s",
		table, p(1), p(2), table, p(3),
	)

	if skipLocked && d.SupportsSkipLocked() {
		query += " FOR UPDATE SKIP LOCKED"
	}

	return query, []any{now, now, limit}
}

// buildClaim renders the UPDATE that leases the selected rows. The attempt
// count is incremented here rather than on failure: a relay that crashes
// mid-publish has still consumed an attempt, so a message that reliably kills
// its relay eventually quarantines instead of being reclaimed forever.
func buildClaim(d dialect.Dialect, table string, ids []string, claimedUntil time.Time) (query string, args []any) {
	args = make([]any, 0, len(ids)+1)
	args = append(args, claimedUntil)

	for _, id := range ids {
		args = append(args, id)
	}

	query = fmt.Sprintf(
		"UPDATE %s SET claimed_until = %s, attempts = attempts + 1 WHERE id IN (%s)",
		table, d.Placeholder(1), d.Placeholders(2, len(ids)),
	)

	return query, args
}

// buildFetch renders the projection of claimed rows, ordered so that messages
// sharing a partition key are published oldest-first.
func buildFetch(d dialect.Dialect, table string, ids []string) (query string, args []any) {
	args = make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}

	query = fmt.Sprintf(
		"SELECT %s FROM %s WHERE id IN (%s) ORDER BY created_at, id",
		messageColumns, table, d.Placeholders(1, len(ids)),
	)

	return query, args
}

// buildMarkPublished renders the UPDATE that retires successfully published
// rows. The rows are kept, not deleted, so a duplicate or a gap can be
// investigated later; the reaper removes them once they age out.
func buildMarkPublished(d dialect.Dialect, table string, ids []string, at time.Time) (query string, args []any) {
	args = make([]any, 0, len(ids)+1)
	args = append(args, at)

	for _, id := range ids {
		args = append(args, id)
	}

	query = fmt.Sprintf(
		"UPDATE %s SET published_at = %s, claimed_until = NULL, last_error = NULL WHERE id IN (%s)",
		table, d.Placeholder(1), d.Placeholders(2, len(ids)),
	)

	return query, args
}

// buildRecordFailure renders the UPDATE applied to a message whose publish
// failed: release the lease, record why, and schedule the retry. Quarantined
// rows are excluded from every future claim.
func buildRecordFailure(d dialect.Dialect, table, id string, nextAttempt time.Time, lastErr string, quarantine bool) (query string, args []any) {
	query = fmt.Sprintf(
		"UPDATE %s SET claimed_until = NULL, next_attempt = %s, last_error = %s, quarantined = %s WHERE id = %s",
		table, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), d.Placeholder(4),
	)

	return query, []any{nextAttempt, lastErr, quarantine, id}
}

// buildBacklog renders the health query: how many messages are waiting, and
// when the oldest of them was created.
//
// Both come back from one round trip because they answer one question — is the
// relay keeping up — and neither is useful alone. A depth of 40,000 is fine if
// the oldest is four seconds old and an incident if it is four hours old.
// Quarantined rows are excluded: they are never going to be published, so
// counting them would make a permanently broken message look like a permanently
// growing backlog.
// It takes no dialect and binds no parameters — unlike every other builder here
// — because it has nothing to vary: no placeholders, and the aggregate syntax is
// identical on all three.
func buildBacklog(table string) string {
	return fmt.Sprintf(
		"SELECT COUNT(*), MIN(created_at) FROM %s WHERE published_at IS NULL AND quarantined = FALSE",
		table,
	)
}

// buildReap renders the DELETE that removes published rows past the retention
// window.
func buildReap(d dialect.Dialect, table string, before time.Time, limit int) (query string, args []any) {
	inner := fmt.Sprintf(
		"SELECT id FROM %s WHERE published_at IS NOT NULL AND published_at < %s LIMIT %s",
		table, d.Placeholder(1), d.Placeholder(2),
	)

	// MySQL refuses a subquery that reads the table being deleted from
	// (ER_UPDATE_TABLE_USED), but accepts it once materialized through a
	// derived table.
	if d == dialect.MySQL {
		inner = fmt.Sprintf("SELECT id FROM (%s) AS doomed", inner)
	}

	return fmt.Sprintf("DELETE FROM %s WHERE id IN (%s)", table, inner), []any{before, limit}
}

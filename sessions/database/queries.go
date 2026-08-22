package database

import (
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
)

// recordColumns is the projection Load scans. Declared once so the SELECT and
// the Scan cannot drift apart, and ordered to match scanRecord.
//
// expires_at is not in it. Whether a session is live is decided by the store's
// policy from created_at and last_seen_at, so the column that exists for the
// sweeper's benefit is never read back — see the DDL for why that separation is
// deliberate.
const recordColumns = "data, created_at, last_seen_at, version"

// A note on timestamps, because one dialect does something surprising.
//
// Every time this package binds is a UTC time.Time truncated to microseconds by
// the store before it ever gets here. Postgres and MySQL store these as real
// temporal types; SQLite does not — modernc's driver stores a bound time.Time
// as Go's own String() rendering, so the sweeper's `expires_at <= ?` there is a
// string comparison.
//
// That is still correct, because the rendering begins with a fixed-width
// "YYYY-MM-DD HH:MM:SS" prefix and everything is UTC, so lexical order is
// chronological order. It stops being correct the moment a value is bound in a
// non-UTC location, so do not remove the .UTC() calls at the binding sites.

// row is one record's worth of bound parameters, with the payload already
// encoded.
type row struct {
	createdAt  time.Time
	lastSeenAt time.Time
	expiresAt  time.Time
	id         string
	data       []byte
	version    int
}

// tableName renders the session table's name for a namespace. The sessions
// segment is the schema's own, so a table says which package created it even in
// a database shared between applications.
func tableName(prefix string) string {
	return ddl.Qualify(prefix) + "sessions"
}

// buildSelect renders the read of one session record.
func buildSelect(d dialect.Dialect, table, id string) (query string, args []any) {
	return fmt.Sprintf("SELECT %s FROM %s WHERE id = %s", recordColumns, table, d.Placeholder(1)),
		[]any{id}
}

// buildInsert renders the creation of a session row, ignoring a row that is
// already there.
//
// The conflict clause is what makes ErrIDConflict reportable without parsing a
// driver's error: a duplicate primary key leaves zero rows affected instead of
// raising a dialect-specific SQLSTATE. It is also what keeps a conflict inside
// Rename's transaction from aborting it — Postgres marks a transaction failed
// after a constraint violation, so a caller could not distinguish "that
// identifier exists" from "your transaction is now unusable".
func buildInsert(d dialect.Dialect, table string, r *row) (query string, args []any) {
	const columns = 6

	query = fmt.Sprintf(
		"INSERT %sINTO %s (id, data, created_at, last_seen_at, expires_at, version) VALUES (%s)",
		ignorePrefix(d), table, d.Placeholders(1, columns),
	)

	if d == dialect.Postgres {
		query += " ON CONFLICT (id) DO NOTHING"
	}

	return query, []any{r.id, r.data, r.createdAt.UTC(), r.lastSeenAt.UTC(), r.expiresAt.UTC(), r.version}
}

// ignorePrefix renders the dialect's "skip a duplicate row" clause prefix.
// Postgres has none and takes an ON CONFLICT clause instead, which buildInsert
// appends.
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

// buildUpdate renders the overwrite of an existing session row.
//
// created_at is deliberately not in the SET list. It anchors the absolute
// timeout, and an update — a touch, a payload save, the write half of a renewal
// — must never move it, or the timeout it anchors would stop being absolute.
// Leaving it out of the statement makes that structural rather than a rule
// somebody has to remember.
func buildUpdate(d dialect.Dialect, table string, r *row) (query string, args []any) {
	query = fmt.Sprintf(
		"UPDATE %s SET data = %s, last_seen_at = %s, expires_at = %s, version = %s WHERE id = %s",
		table, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), d.Placeholder(4), d.Placeholder(5),
	)

	return query, []any{r.data, r.lastSeenAt.UTC(), r.expiresAt.UTC(), r.version, r.id}
}

// buildExists renders the existence check Update falls back on.
//
// It runs only when an UPDATE reported no rows affected, which MySQL also
// reports for an update that changed nothing — two saves of an identical
// payload within the same microsecond, say. Without this the second would be
// answered "no such session" and sign a user out over a no-op.
func buildExists(d dialect.Dialect, table, id string) (query string, args []any) {
	return fmt.Sprintf("SELECT 1 FROM %s WHERE id = %s", table, d.Placeholder(1)), []any{id}
}

// buildDelete renders the removal of one session row.
func buildDelete(d dialect.Dialect, table, id string) (query string, args []any) {
	return fmt.Sprintf("DELETE FROM %s WHERE id = %s", table, d.Placeholder(1)), []any{id}
}

// buildSweep renders the removal of every row whose deadline has passed.
func buildSweep(d dialect.Dialect, table string, now time.Time) (query string, args []any) {
	return fmt.Sprintf("DELETE FROM %s WHERE expires_at <= %s", table, d.Placeholder(1)),
		[]any{now.UTC()}
}

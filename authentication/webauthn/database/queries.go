package database

import (
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
)

// sessionColumns is the projection Consume scans, ordered to match the Scan.
const sessionColumns = "session_data, expires_at"

// A note on timestamps, because one dialect does something surprising.
//
// Every time this package binds is a UTC time.Time. Postgres and MySQL store
// these as real temporal types; SQLite does not — modernc's driver stores a
// bound time.Time as Go's own String() rendering, so the sweeper's
// `expires_at <= ?` there is a string comparison.
//
// That is still correct, because the rendering begins with a fixed-width
// "YYYY-MM-DD HH:MM:SS" prefix and everything is UTC, so lexical order is
// chronological order. It stops being correct the moment a value is bound in a
// non-UTC location, so do not remove the .UTC() calls at the binding sites.

// tableName renders the ceremony session table's name for a namespace. The
// webauthn segment is the schema's own, so a table says which package created
// it even in a database shared between applications.
func tableName(prefix string) string {
	return ddl.Qualify(prefix) + "webauthn_sessions"
}

// buildUpsert renders the write of one ceremony's state.
//
// It is an upsert rather than a plain insert because the alternative is worse
// in both directions: an insert that conflicts would have to be recognized from
// a dialect-specific SQLSTATE, and an insert-ignore would silently keep the old
// row and hand the next Consume state for a ceremony nobody is running. A
// challenge is at least 16 bytes of cryptographic randomness, so a conflict
// means the same ceremony was begun twice, and the later one is the live one.
func buildUpsert(
	d dialect.Dialect,
	table, challenge string,
	data []byte,
	expiresAt time.Time,
) (query string, args []any) {
	args = []any{challenge, data, expiresAt.UTC()}

	base := fmt.Sprintf(
		"INSERT INTO %s (challenge, session_data, expires_at) VALUES (%s)",
		table, d.Placeholders(1, len(args)),
	)

	switch d {
	case dialect.MySQL:
		return base + " ON DUPLICATE KEY UPDATE" +
			" session_data = VALUES(session_data), expires_at = VALUES(expires_at)", args
	case dialect.Postgres, dialect.SQLite:
		return base + " ON CONFLICT (challenge) DO UPDATE SET" +
			" session_data = EXCLUDED.session_data, expires_at = EXCLUDED.expires_at", args
	default:
		return base, args
	}
}

// buildSelect renders the read half of Consume.
func buildSelect(d dialect.Dialect, table, challenge string) (query string, args []any) {
	return fmt.Sprintf("SELECT %s FROM %s WHERE challenge = %s", sessionColumns, table, d.Placeholder(1)),
		[]any{challenge}
}

// buildDelete renders the delete half of Consume, which is also the half that
// decides who owns the ceremony.
func buildDelete(d dialect.Dialect, table, challenge string) (query string, args []any) {
	return fmt.Sprintf("DELETE FROM %s WHERE challenge = %s", table, d.Placeholder(1)),
		[]any{challenge}
}

// buildSweep renders the removal of every row whose deadline has passed.
func buildSweep(d dialect.Dialect, table string, now time.Time) (query string, args []any) {
	return fmt.Sprintf("DELETE FROM %s WHERE expires_at <= %s", table, d.Placeholder(1)),
		[]any{now.UTC()}
}

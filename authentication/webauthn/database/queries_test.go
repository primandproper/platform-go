package database

import (
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

func TestTableName(T *testing.T) {
	T.Parallel()

	T.Run("renders the schema's own name for an empty namespace", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "webauthn_sessions", tableName(""))
	})

	// The separator belongs to the renderer, so a caller supplies "ddb" rather
	// than "ddb_" and cannot produce ddb__webauthn_sessions.
	T.Run("prepends a namespace with one separator", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "ddb_webauthn_sessions", tableName("ddb"))
	})
}

func TestBuildUpsert(T *testing.T) {
	T.Parallel()

	at := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)

	T.Run("binds the challenge, the state, and the deadline in that order", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := buildUpsert(d, "webauthn_sessions", "chal", []byte("state"), at)

			test.StrContains(t, query, "INSERT INTO webauthn_sessions (challenge, session_data, expires_at)",
				test.Sprintf("dialect %q", d))
			test.Eq(t, []any{"chal", []byte("state"), at}, args, test.Sprintf("dialect %q", d))
		}
	})

	// A begun ceremony that is begun again replaces the earlier one, rather
	// than being ignored — an insert-ignore would leave the next Consume
	// answering with state for a ceremony nobody is running.
	T.Run("replaces the row a repeated challenge already has", func(t *testing.T) {
		t.Parallel()

		postgres, _ := buildUpsert(dialect.Postgres, "webauthn_sessions", "chal", nil, at)
		test.StrContains(t, postgres, "ON CONFLICT (challenge) DO UPDATE SET")
		test.StrContains(t, postgres, "session_data = EXCLUDED.session_data")

		sqlite, _ := buildUpsert(dialect.SQLite, "webauthn_sessions", "chal", nil, at)
		test.StrContains(t, sqlite, "ON CONFLICT (challenge) DO UPDATE SET")

		// MySQL names the incoming row through VALUES() and has no conflict
		// target, which is the same statement spelled the only way it accepts.
		mysql, _ := buildUpsert(dialect.MySQL, "webauthn_sessions", "chal", nil, at)
		test.StrContains(t, mysql, "ON DUPLICATE KEY UPDATE")
		test.StrContains(t, mysql, "session_data = VALUES(session_data)")
		test.StrNotContains(t, mysql, "ON CONFLICT")
	})

	T.Run("numbers placeholders only for postgres", func(t *testing.T) {
		t.Parallel()

		postgres, _ := buildUpsert(dialect.Postgres, "webauthn_sessions", "chal", nil, at)
		test.StrContains(t, postgres, "VALUES ($1, $2, $3)")

		for _, d := range []dialect.Dialect{dialect.MySQL, dialect.SQLite} {
			query, _ := buildUpsert(d, "webauthn_sessions", "chal", nil, at)
			test.StrContains(t, query, "VALUES (?, ?, ?)", test.Sprintf("dialect %q", d))
		}
	})

	// The bound instant is UTC whatever zone it arrived in, because SQLite
	// stores it as Go's own rendering and compares it as a string: a value
	// bound in another zone would sort into the wrong place.
	T.Run("binds the deadline as UTC", func(t *testing.T) {
		t.Parallel()

		zone := time.FixedZone("UTC+7", 7*60*60)

		_, args := buildUpsert(dialect.Postgres, "webauthn_sessions", "chal", nil, at.In(zone))

		bound, ok := args[2].(time.Time)
		test.True(t, ok)
		test.EqOp(t, time.UTC, bound.Location())
	})
}

func TestBuildSelect(T *testing.T) {
	T.Parallel()

	T.Run("projects exactly the columns the scan reads", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := buildSelect(d, "webauthn_sessions", "chal")

			test.StrContains(t, query, "SELECT "+sessionColumns+" FROM webauthn_sessions",
				test.Sprintf("dialect %q", d))
			test.Eq(t, []any{"chal"}, args, test.Sprintf("dialect %q", d))
		}
	})

	// The read does not filter on the deadline. Consume compares it in Go
	// instead, so that an expired row is deleted by the delete that follows
	// rather than left behind by a read that could not see it.
	T.Run("filters on the challenge alone", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, _ := buildSelect(d, "webauthn_sessions", "chal")

			_, where, found := strings.Cut(query, "WHERE")

			must.True(t, found, must.Sprintf("dialect %q renders no predicate", d))
			test.StrContains(t, where, "challenge = ", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, where, "expires_at", test.Sprintf("dialect %q", d))
		}
	})
}

func TestBuildDelete(T *testing.T) {
	T.Parallel()

	T.Run("removes exactly the challenge it names", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := buildDelete(d, "webauthn_sessions", "chal")

			test.StrContains(t, query, "DELETE FROM webauthn_sessions WHERE challenge = ",
				test.Sprintf("dialect %q", d))
			test.Eq(t, []any{"chal"}, args, test.Sprintf("dialect %q", d))
		}
	})
}

func TestBuildSweep(T *testing.T) {
	T.Parallel()

	at := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)

	// Inclusive, matching Consume's own comparison: a row at exactly its
	// deadline is expired in both places, so the sweeper cannot delete
	// something Consume would still hand out.
	T.Run("removes everything at or past the deadline", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := buildSweep(d, "webauthn_sessions", at)

			test.StrContains(t, query, "DELETE FROM webauthn_sessions WHERE expires_at <= ",
				test.Sprintf("dialect %q", d))
			test.Eq(t, []any{at}, args, test.Sprintf("dialect %q", d))
		}
	})

	T.Run("binds the instant as UTC", func(t *testing.T) {
		t.Parallel()

		zone := time.FixedZone("UTC-3", -3*60*60)

		_, args := buildSweep(dialect.SQLite, "webauthn_sessions", at.In(zone))

		bound, ok := args[0].(time.Time)
		test.True(t, ok)
		test.EqOp(t, time.UTC, bound.Location())
	})
}

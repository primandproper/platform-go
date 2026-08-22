package database

import (
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
)

var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

func TestTableName(T *testing.T) {
	T.Parallel()

	T.Run("renders the schema's own name for an empty namespace", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "sessions", tableName(""))
	})

	// The separator belongs to the renderer, so a caller supplies "ddb" rather
	// than "ddb_" and cannot produce ddb__sessions.
	T.Run("prepends a namespace with one separator", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "ddb_sessions", tableName("ddb"))
	})
}

func TestBuildSelect(T *testing.T) {
	T.Parallel()

	T.Run("projects exactly the columns the scan reads", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := buildSelect(d, "sessions", "id-1")

			test.StrContains(t, query, "SELECT "+recordColumns+" FROM sessions", test.Sprintf("dialect %q", d))
			test.Eq(t, []any{"id-1"}, args)
		}
	})

	// The column that exists for the sweeper never appears in a read predicate:
	// expiry belongs to the store, so clock skew between a writer and a reader
	// cannot hide a live session.
	T.Run("does not filter on expires_at", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, _ := buildSelect(d, "sessions", "id-1")
			test.StrNotContains(t, query, "expires_at", test.Sprintf("dialect %q", d))
		}
	})

	T.Run("numbers placeholders only for postgres", func(t *testing.T) {
		t.Parallel()

		pg, _ := buildSelect(dialect.Postgres, "sessions", "id-1")
		test.StrContains(t, pg, "id = $1")

		my, _ := buildSelect(dialect.MySQL, "sessions", "id-1")
		test.StrContains(t, my, "id = ?")
	})
}

func TestBuildInsert(T *testing.T) {
	T.Parallel()

	r := row{
		createdAt:  testEpoch,
		lastSeenAt: testEpoch,
		expiresAt:  testEpoch.Add(time.Hour),
		id:         "id-1",
		data:       []byte("payload"),
		version:    1,
	}

	// A duplicate primary key has to leave zero rows affected rather than
	// raising a dialect-specific error, or ErrIDConflict would mean parsing
	// three drivers' errors — and inside Rename's transaction, a constraint
	// violation would take the whole transaction down with it.
	T.Run("every dialect skips a duplicate row instead of raising", func(t *testing.T) {
		t.Parallel()

		pg, _ := buildInsert(dialect.Postgres, "sessions", &r)
		test.StrContains(t, pg, "ON CONFLICT (id) DO NOTHING")

		my, _ := buildInsert(dialect.MySQL, "sessions", &r)
		test.StrContains(t, my, "INSERT IGNORE INTO")
		test.StrNotContains(t, my, "ON CONFLICT")

		lite, _ := buildInsert(dialect.SQLite, "sessions", &r)
		test.StrContains(t, lite, "INSERT OR IGNORE INTO")
		test.StrNotContains(t, lite, "ON CONFLICT")
	})

	T.Run("binds every column in the declared order", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := buildInsert(d, "sessions", &r)

			test.StrContains(t, query,
				"(id, data, created_at, last_seen_at, expires_at, version)", test.Sprintf("dialect %q", d))
			test.SliceLen(t, 6, args, test.Sprintf("dialect %q", d))
			test.EqOp(t, "id-1", args[0].(string), test.Sprintf("dialect %q", d))
			test.EqOp(t, 1, args[5].(int), test.Sprintf("dialect %q", d))
		}
	})

	// Bound in UTC, which is what makes the sweeper's comparison correct on
	// SQLite — where it is a string comparison over Go's own time rendering.
	T.Run("binds times in UTC", func(t *testing.T) {
		t.Parallel()

		zoned := r
		zoned.createdAt = testEpoch.In(time.FixedZone("somewhere", 5*60*60))

		_, args := buildInsert(dialect.SQLite, "sessions", &zoned)

		bound, ok := args[2].(time.Time)
		test.True(t, ok)
		test.EqOp(t, time.UTC, bound.Location())
	})
}

func TestBuildUpdate(T *testing.T) {
	T.Parallel()

	r := row{
		createdAt:  testEpoch,
		lastSeenAt: testEpoch.Add(time.Minute),
		expiresAt:  testEpoch.Add(time.Hour),
		id:         "id-1",
		data:       []byte("payload"),
		version:    1,
	}

	// The structural half of "an update never extends a session's total
	// lifetime": the anchor is not in the statement at all, so no caller can
	// move it by accident.
	T.Run("never writes created_at", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := buildUpdate(d, "sessions", &r)

			test.StrNotContains(t, query, "created_at", test.Sprintf("dialect %q", d))
			test.SliceLen(t, 5, args, test.Sprintf("dialect %q", d))
		}
	})

	T.Run("is keyed by the identifier", func(t *testing.T) {
		t.Parallel()

		query, args := buildUpdate(dialect.Postgres, "sessions", &r)
		test.StrContains(t, query, "WHERE id = $5")
		test.EqOp(t, "id-1", args[4].(string))
	})
}

func TestBuildDeleteAndSweep(T *testing.T) {
	T.Parallel()

	T.Run("delete is keyed by the identifier", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := buildDelete(d, "sessions", "id-1")

			test.StrContains(t, query, "DELETE FROM sessions WHERE id = ", test.Sprintf("dialect %q", d))
			test.Eq(t, []any{"id-1"}, args)
		}
	})

	T.Run("sweep is keyed by the deadline", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := buildSweep(d, "sessions", testEpoch)

			test.StrContains(t, query, "DELETE FROM sessions WHERE expires_at <= ", test.Sprintf("dialect %q", d))
			test.SliceLen(t, 1, args, test.Sprintf("dialect %q", d))
		}
	})

	// The deadline instant itself is swept, matching the store's rule that a
	// session is over at its deadline rather than one moment after it.
	T.Run("sweep includes the deadline instant", func(t *testing.T) {
		t.Parallel()

		query, _ := buildSweep(dialect.SQLite, "sessions", testEpoch)
		test.StrContains(t, query, "<=")
	})
}

func TestBuildExists(T *testing.T) {
	T.Parallel()

	T.Run("asks only whether the row is there", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := buildExists(d, "sessions", "id-1")

			test.True(t, strings.HasPrefix(query, "SELECT 1 FROM sessions WHERE id = "),
				test.Sprintf("dialect %q rendered %q", d, query))
			test.Eq(t, []any{"id-1"}, args)
		}
	})
}

func TestIgnorePrefix(T *testing.T) {
	T.Parallel()

	T.Run("renders each dialect's skip-a-duplicate clause", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", ignorePrefix(dialect.Postgres))
		test.EqOp(t, "IGNORE ", ignorePrefix(dialect.MySQL))
		test.EqOp(t, "OR IGNORE ", ignorePrefix(dialect.SQLite))
	})

	// An unknown dialect renders a plain INSERT, which fails loudly on a
	// duplicate rather than silently ignoring one.
	T.Run("renders nothing for a dialect it does not know", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", ignorePrefix(dialect.Dialect("oracle")))
	})
}

var testEpoch = time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)

package migrations

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func allDialects() []dialect.Dialect {
	return []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}
}

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)
			must.SliceNotEmpty(t, stmts, must.Sprintf("dialect %q", d))
		}
	})

	T.Run("rejects a dialect it has no schema for", func(t *testing.T) {
		t.Parallel()

		_, err := Statements(dialect.Dialect("oracle"), "")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("substitutes the prefix everywhere", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "custom")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "custom_sessions", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_sessions_expires_at_idx", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, ddl.Placeholder, test.Sprintf("dialect %q", d))
		}
	})

	// An empty namespace is the ordinary case, not a missing value: it renders
	// the component's own name, which is what a consumer with one application
	// per database wants.
	T.Run("an empty prefix renders the schema's own names", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "sessions", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, "_sessions", test.Sprintf("dialect %q", d))
		}
	})

	// The index cannot be created before the table it indexes.
	T.Run("creates the table before its index", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			tableAt := strings.Index(joined, "CREATE TABLE")
			indexAt := strings.Index(joined, "CREATE INDEX")

			test.True(t, tableAt >= 0, test.Sprintf("dialect %q", d))
			test.True(t, indexAt > tableAt, test.Sprintf("dialect %q", d))
		}
	})

	// MySQL has no CREATE INDEX IF NOT EXISTS, so its index is declared inline
	// and there is no separate statement to order.
	T.Run("mysql declares its index inline", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.MySQL, "")
		must.NoError(t, err)
		test.SliceLen(t, 1, stmts)
		test.StrContains(t, stmts[0], "KEY sessions_expires_at_idx")
	})

	// Every schema declares the same columns, or a session written against one
	// engine would be unreadable against another.
	T.Run("every dialect declares the same columns", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			for _, column := range []string{"id", "data", "created_at", "last_seen_at", "expires_at", "version"} {
				test.StrContains(t, joined, column, test.Sprintf("dialect %q is missing %q", d, column))
			}
		}
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts an empty prefix and a plain identifier", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, ValidatePrefix(""))
		must.NoError(t, ValidatePrefix("ddb"))
	})

	// The renderer supplies the separator, so a prefix carrying one would
	// render ddb__sessions — legal SQL, and a table nobody meant to name.
	T.Run("rejects a trailing separator", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix("ddb_"), ddl.ErrPrefixTrailingSeparator)
	})

	T.Run("rejects a prefix that is not an identifier", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix("not an identifier"), dialect.ErrInvalidIdentifier)
		test.ErrorIs(t, ValidatePrefix("sessions; DROP TABLE"), dialect.ErrInvalidIdentifier)
	})

	// Vetting the prefix alone would not be enough: the longest identifier the
	// schema creates is the index name, and a prefix that is fine on its own can
	// still push it past what the engines accept.
	T.Run("rejects a prefix that renders an over-long index name", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix(strings.Repeat("a", 60)), ddl.ErrPrefixTooLong)
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("renders one body per dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			body, err := SQL(d, "")
			must.NoError(t, err)

			test.StrContains(t, body, "CREATE TABLE", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, body, ddl.Placeholder, test.Sprintf("dialect %q", d))
			// Comments are stripped, which matters: goose splits a migration on
			// semicolons, and a '--' comment containing one would be torn in
			// half.
			test.StrNotContains(t, body, "--", test.Sprintf("dialect %q", d))
		}
	})

	T.Run("rejects a dialect it has no schema for", func(t *testing.T) {
		t.Parallel()

		_, err := SQL(dialect.Dialect("oracle"), "")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}

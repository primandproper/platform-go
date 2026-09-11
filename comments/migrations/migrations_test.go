package migrations

import (
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// allDialects is every dialect this package renders DDL for.
var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// testPrefix is the namespace these tests render against. A non-empty one, so a
// prefix that failed to reach a statement shows up as a name without it.
const testPrefix = "cm"

// table is the one table this package creates, at testPrefix.
const table = testPrefix + "_comments"

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders every supported dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, testPrefix)
			must.NoError(t, err, must.Sprintf("dialect %s", d))
			must.SliceNotEmpty(t, stmts)

			// The table before the indexes on it — a CREATE INDEX against a table
			// that does not exist yet is a migration that fails on its first run
			// and every run after it.
			create := slices.IndexFunc(stmts, func(s string) bool {
				return strings.Contains(s, "CREATE TABLE") && strings.Contains(s, table)
			})
			must.GreaterEq(t, 0, create, must.Sprintf("dialect %s", d))

			for i, stmt := range stmts {
				if strings.Contains(stmt, "INDEX") {
					test.Greater(t, create, i, test.Sprintf("dialect %s", d))
				}

				test.StrNotContains(t, stmt, ddl.Placeholder, test.Sprintf("dialect %s", d))

				// Comments are stripped, which matters: goose splits a migration
				// on semicolons and would tear a '--' comment containing one in
				// half.
				test.StrNotContains(t, stmt, "--", test.Sprintf("dialect %s", d))
			}
		}
	})

	T.Run("scopes the table with no default", func(t *testing.T) {
		t.Parallel()

		// The empty string is tenancy.Global(), so a DEFAULT here would hand the
		// global scope to a write that forgot the column — the mistake
		// tenancy.Scope exists to make unspellable in Go.
		for _, d := range allDialects {
			scope := columnLine(t, createStatement(t, d), "scope")

			test.StrContains(t, scope, "NOT NULL", test.Sprintf("dialect %s", d))
			test.StrNotContains(t, scope, "DEFAULT", test.Sprintf("dialect %s", d))
		}
	})

	T.Run("defaults the parent to no parent", func(t *testing.T) {
		t.Parallel()

		// The opposite ruling to the scope's, and it is the same reasoning
		// arriving at the other answer. The empty scope is a tenant, so a default
		// would file a write in it; the empty parent is a root, which is what a
		// comment that names no parent actually is.
		for _, d := range allDialects {
			parent := columnLine(t, createStatement(t, d), "parent_id")

			test.StrContains(t, parent, "NOT NULL", test.Sprintf("dialect %s", d))
			test.StrContains(t, parent, "DEFAULT ''", test.Sprintf("dialect %s", d))
		}
	})

	T.Run("gives the target no default", func(t *testing.T) {
		t.Parallel()

		// Both halves of the target are required of every comment, and neither
		// has a sensible fallback: a comment about nothing is a comment no view
		// shows, and a default would write one rather than refuse it.
		for _, d := range allDialects {
			create := createStatement(t, d)

			for _, column := range []string{"target_type", "target_id"} {
				line := columnLine(t, create, column)

				test.StrContains(t, line, "NOT NULL", test.Sprintf("dialect %s: %s", d, column))
				test.StrNotContains(t, line, "DEFAULT", test.Sprintf("dialect %s: %s", d, column))
			}
		}
	})

	T.Run("leaves the edit stamp nullable", func(t *testing.T) {
		t.Parallel()

		// NULL is this module's spelling of "has not happened yet", and it is
		// what an unedited comment's last_updated_at means. A NOT NULL column
		// would need a sentinel instant, and every "edited" marker would be
		// rendered from it.
		for _, d := range allDialects {
			line := columnLine(t, createStatement(t, d), "last_updated_at")

			test.StrNotContains(t, line, "NOT NULL", test.Sprintf("dialect %s", d))
		}
	})

	T.Run("indexes the reads the store actually makes", func(t *testing.T) {
		t.Parallel()

		// One index per list statement, and every one of them leads with the
		// scope: an index that did not would be one no scoped read can use, which
		// is every read this package has.
		for _, d := range allDialects {
			stmts, err := Statements(d, testPrefix)
			must.NoError(t, err)

			var indexes int

			for _, stmt := range stmts {
				if !strings.Contains(stmt, "INDEX") {
					continue
				}

				indexes++

				test.StrContains(t, stmt, "(scope,", test.Sprintf("dialect %s: %s", d, stmt))
			}

			test.EqOp(t, 3, indexes, test.Sprintf("dialect %s", d))
		}
	})

	T.Run("indexes the parent beside the target", func(t *testing.T) {
		t.Parallel()

		// The discussion's two reads are one statement with a different parent,
		// which is only worth anything if one index answers both. An index that
		// stopped at the target would leave the reply read scanning every comment
		// on the thing.
		for _, d := range allDialects {
			stmts, err := Statements(d, testPrefix)
			must.NoError(t, err)

			var found bool

			for _, stmt := range stmts {
				if strings.Contains(stmt, "INDEX") &&
					strings.Contains(stmt, "target_id") &&
					strings.Contains(stmt, "parent_id") {
					found = true
				}
			}

			test.True(t, found, test.Sprintf("dialect %s indexes no target/parent pair", d))
		}
	})
}

// createStatement returns the CREATE TABLE for the one table, in dialect d.
func createStatement(t *testing.T, d dialect.Dialect) string {
	t.Helper()

	stmts, err := Statements(d, testPrefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		if strings.Contains(stmt, "CREATE TABLE") {
			return stmt
		}
	}

	t.Fatalf("dialect %q renders no CREATE TABLE", d)

	return ""
}

// columnLine returns the line of a CREATE TABLE that declares one column.
func columnLine(t *testing.T, create, column string) string {
	t.Helper()

	for line := range strings.SplitSeq(create, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, column+" ") {
			return trimmed
		}
	}

	t.Fatalf("no %s column in %q", column, create)

	return ""
}

func TestTables(T *testing.T) {
	T.Parallel()

	T.Run("names the table at the prefix", func(t *testing.T) {
		t.Parallel()

		tables, err := Tables(testPrefix)
		must.NoError(t, err)

		test.Eq(t, []string{table}, tables)
	})

	T.Run("refuses a prefix that would not render an identifier", func(t *testing.T) {
		t.Parallel()

		_, err := Tables("no-hyphens-allowed")
		test.Error(t, err)
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("renders one body per dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			body, err := SQL(d, testPrefix)
			must.NoError(t, err, must.Sprintf("dialect %s", d))

			test.StrContains(t, body, table, test.Sprintf("dialect %s", d))
			test.StrNotContains(t, body, ddl.Placeholder, test.Sprintf("dialect %s", d))
		}
	})

	T.Run("refuses a dialect it does not render", func(t *testing.T) {
		t.Parallel()

		_, err := SQL(dialect.Dialect("cassandra"), testPrefix)
		test.Error(t, err)
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts the empty prefix", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, ValidatePrefix(""))
	})

	T.Run("refuses one that would not render an identifier", func(t *testing.T) {
		t.Parallel()

		test.Error(t, ValidatePrefix("no-hyphens-allowed"))
	})
}

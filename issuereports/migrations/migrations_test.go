package migrations

import (
	"regexp"
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
const testPrefix = "ir"

// table is the one table this package creates, at testPrefix.
const table = testPrefix + "_issue_reports"

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

	T.Run("holds the status with no default either", func(t *testing.T) {
		t.Parallel()

		// Every report is created open, and the store is what says so. A column
		// default would be a second place the initial status lives, and the day
		// the two disagreed the row would be in a status nothing had decided.
		for _, d := range allDialects {
			status := columnLine(t, createStatement(t, d), "status")

			test.StrContains(t, status, "NOT NULL", test.Sprintf("dialect %s", d))
			test.StrNotContains(t, status, "DEFAULT", test.Sprintf("dialect %s", d))
		}
	})

	T.Run("leaves closed_at nullable", func(t *testing.T) {
		t.Parallel()

		// NULL is this module's spelling of "has not happened yet", and it is
		// what an open report's closed_at means. A NOT NULL column would need a
		// sentinel instant, and every time-to-resolution number would be computed
		// from it.
		for _, d := range allDialects {
			closedAt := columnLine(t, createStatement(t, d), "closed_at")

			test.StrNotContains(t, closedAt, "NOT NULL", test.Sprintf("dialect %s", d))
		}
	})

	T.Run("indexes the reads the store actually makes", func(t *testing.T) {
		t.Parallel()

		// One index per list statement, and every one of them leads with the
		// scope: an index that did not would be one no scoped read can use, which
		// is every read this package has.
		for _, d := range allDialects {
			declarations := indexDeclarations(t, d)

			for _, declaration := range declarations {
				test.StrContains(t, declaration, "(scope,", test.Sprintf("dialect %s: %s", d, declaration))
			}

			test.SliceLen(t, 4, declarations, test.Sprintf("dialect %s", d))
		}
	})
}

// indexDeclaration matches one index this schema declares, in either of the two
// shapes a dialect gives it: a CREATE INDEX statement of its own, which is what
// Postgres and SQLite render, and a KEY clause inside the CREATE TABLE, which is
// what MySQL renders because it has no CREATE INDEX IF NOT EXISTS to guard a
// standalone one with. internal/schemaconvention is where that rule lives, and
// where every schema in the module is held to it.
//
// PRIMARY KEY and UNIQUE KEY are deliberately not matched: this package declares
// neither, and a test that counted them would answer a different question than
// the one it asks.
var indexDeclaration = regexp.MustCompile(`(?is)(?:CREATE\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?(\S+)\s+ON\s+\S+\s*|(?:^|[,(\s])KEY\s+(\S+)\s*)\(([^)]*)\)`)

// indexDeclarations is every index the schema declares in dialect d, each
// normalized to "name (columns)" with its whitespace collapsed.
//
// The normalization is what lets one assertion read both shapes: a rendered KEY
// clause wraps its column list onto the next line when the line would be long,
// so a test looking for "(scope," in the raw text would find it on two dialects
// and miss it on the third for reasons of line width.
func indexDeclarations(t *testing.T, d dialect.Dialect) []string {
	t.Helper()

	stmts, err := Statements(d, testPrefix)
	must.NoError(t, err)

	declarations := []string{}

	for _, stmt := range stmts {
		for _, match := range indexDeclaration.FindAllStringSubmatch(stmt, -1) {
			name := match[1]
			if name == "" {
				name = match[2]
			}

			declarations = append(declarations,
				name+" ("+strings.Join(strings.Fields(match[3]), " ")+")")
		}
	}

	return declarations
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

package migrations

import (
	"regexp"
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"

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

			test.StrContains(t, joined, "custom_series (", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_series_occurrences (", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_series_occurrences_slot_uniq", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, ddl.Placeholder, test.Sprintf("dialect %q", d))
		}
	})

	// The slot index is what makes materializing idempotent, and it has to be
	// the exact column pair the insert names as its conflict target, or
	// Postgres refuses the statement.
	T.Run("one row per slot per series on every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			test.StrContains(t, strings.Join(stmts, "\n"), "series_occurrences_slot_uniq", test.Sprintf("dialect %q", d))
			test.StrContains(t, strings.Join(stmts, "\n"), "(series_id, slot_at)", test.Sprintf("dialect %q", d))
		}
	})

	// MySQL has no CREATE INDEX IF NOT EXISTS, so its keys are declared inside
	// the tables. Two statements is the right answer there, and more would be a
	// migration that fails on its second run.
	T.Run("mysql declares its indexes inline", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.MySQL, "")
		must.NoError(t, err)
		must.SliceLen(t, 2, stmts)
		test.StrContains(t, stmts[1], "UNIQUE KEY series_occurrences_slot_uniq (series_id, slot_at)")
	})

	// The empty string is tenancy.Global() rather than the absence of a scope,
	// so a default would file the write that forgot the column in the tenant
	// that matches nobody. State has none either: every write names one.
	T.Run("neither the scope nor the state carries a default", func(t *testing.T) {
		t.Parallel()

		columns := regexp.MustCompile(`(?m)^\s+(scope|state)\s+.*$`)

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			lines := columns.FindAllString(strings.Join(stmts, "\n"), -1)
			must.SliceLen(t, 3, lines, must.Sprintf("dialect %q", d))

			for _, line := range lines {
				test.StrNotContains(t, line, "DEFAULT", test.Sprintf("dialect %q: %s", d, line))
			}
		}
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("renders the same DDL as one body", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			body, err := SQL(d, "ddb")
			must.NoError(t, err)

			stmts, stmtErr := Statements(d, "ddb")
			must.NoError(t, stmtErr)

			for _, stmt := range stmts {
				test.StrContains(t, body, strings.TrimSuffix(stmt, ";"), test.Sprintf("dialect %q", d))
			}
		}
	})

	T.Run("rejects a dialect it has no schema for", func(t *testing.T) {
		t.Parallel()

		_, err := SQL(dialect.Dialect("oracle"), "")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts a namespace the schema can render", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"", "ddb", "app_two"} {
			test.NoError(t, ValidatePrefix(prefix), test.Sprintf("prefix %q", prefix))
		}
	})

	T.Run("rejects a namespace that is not an identifier fragment", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"ddb-1", "a b", "series; DROP TABLE users;--"} {
			test.Error(t, ValidatePrefix(prefix), test.Sprintf("prefix %q", prefix))
		}
	})

	T.Run("rejects a namespace that renders an over-long identifier", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix(strings.Repeat("a", ddl.MaxIdentifierLength)), ddl.ErrPrefixTooLong)
	})

	T.Run("rejects a namespace that ends in the separator", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix("ddb_"), ddl.ErrPrefixTrailingSeparator)
	})
}

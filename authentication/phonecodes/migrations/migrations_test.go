package migrations

import (
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

// joined is one dialect's DDL at a prefix, as one string.
func joined(t *testing.T, d dialect.Dialect, prefix string) string {
	t.Helper()

	stmts, err := Statements(d, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts, must.Sprintf("dialect %q", d))

	return strings.Join(stmts, "\n")
}

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("rejects a dialect it has no schema for", func(t *testing.T) {
		t.Parallel()

		_, err := Statements(dialect.Dialect("oracle"), "")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("substitutes the prefix everywhere", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			ddlText := joined(t, d, "custom")

			for _, name := range []string{
				"custom_phone_codes",
				"custom_phone_codes_phone_uniq",
				"custom_phone_codes_subject_idx",
				"custom_phone_codes_purge_after_idx",
			} {
				test.StrContains(t, ddlText, name, test.Sprintf("dialect %q", d))
			}

			test.StrNotContains(t, ddlText, ddl.Placeholder, test.Sprintf("dialect %q", d))
		}
	})

	T.Run("an empty prefix renders the schema's own names", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			ddlText := joined(t, d, "")

			test.StrContains(t, ddlText, "phone_codes", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, ddlText, "_phone_codes (", test.Sprintf("dialect %q", d))
		}
	})

	// The scope column carries no default: the empty string is
	// tenancy.Global(), and a default would hand the global scope to a write
	// that forgot the column.
	T.Run("gives the scope column no default", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			ddlText := joined(t, d, "")

			scopeAt := strings.Index(ddlText, "    scope")
			must.True(t, scopeAt >= 0, must.Sprintf("dialect %q", d))

			line := ddlText[scopeAt : strings.Index(ddlText[scopeAt:], "\n")+scopeAt]
			test.StrNotContains(t, line, "DEFAULT", test.Sprintf("dialect %q", d))
		}
	})

	// One code per number per scope is the unique key, not a convention.
	T.Run("keys a number's code on the scope and the number", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			ddlText := joined(t, d, "")

			test.StrContains(t, ddlText, "(scope, phone_number)", test.Sprintf("dialect %q", d))
			test.StrContains(t, ddlText, "UNIQUE", test.Sprintf("dialect %q", d))
			test.StrContains(t, ddlText, "(scope, subject_id)", test.Sprintf("dialect %q", d))
		}
	})

	T.Run("creates the table before its indexes", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
			stmts, err := Statements(d, "")
			must.NoError(t, err)
			must.SliceLen(t, 4, stmts, must.Sprintf("dialect %q", d))

			test.StrContains(t, stmts[0], "CREATE TABLE", test.Sprintf("dialect %q", d))

			for _, stmt := range stmts[1:] {
				test.StrContains(t, stmt, "INDEX", test.Sprintf("dialect %q", d))
			}
		}
	})

	// MySQL has no CREATE INDEX IF NOT EXISTS, so its indexes are declared
	// inside the table: one statement, which a second run leaves alone.
	T.Run("mysql declares its indexes inline", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.MySQL, "")
		must.NoError(t, err)
		must.SliceLen(t, 1, stmts)

		test.StrContains(t, stmts[0], "UNIQUE KEY phone_codes_phone_uniq")
		test.StrContains(t, stmts[0], "KEY phone_codes_subject_idx")
		test.StrContains(t, stmts[0], "KEY phone_codes_purge_after_idx")
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

func TestTables(T *testing.T) {
	T.Parallel()

	T.Run("names the table this package creates", func(t *testing.T) {
		t.Parallel()

		tables, err := Tables("")
		must.NoError(t, err)
		test.Eq(t, []string{"phone_codes"}, tables)

		tables, err = Tables("ddb")
		must.NoError(t, err)
		test.Eq(t, []string{"ddb_phone_codes"}, tables)
	})

	T.Run("rejects a prefix the schema cannot render", func(t *testing.T) {
		t.Parallel()

		tables, err := Tables("ddb_")
		test.Nil(t, tables)
		test.ErrorIs(t, err, ddl.ErrPrefixTrailingSeparator)
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

		for _, prefix := range []string{"ddb-1", "a b", "x; DROP TABLE users;--"} {
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

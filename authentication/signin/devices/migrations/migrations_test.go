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

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders the table in every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)
			must.SliceNotEmpty(t, stmts, must.Sprintf("dialect %q", d))
			test.StrContains(t, stmts[0], "CREATE TABLE IF NOT EXISTS signin_devices", test.Sprintf("dialect %q", d))
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

			test.StrContains(t, joined, "custom_signin_devices", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_signin_devices_user_idx", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_signin_devices_expires_at_idx", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, ddl.Placeholder, test.Sprintf("dialect %q", d))
		}
	})

	// The key is the login within its scope. Every refresh mints a token in the
	// same family, and the upsert converging on this key is what makes a refresh
	// a renewal of one row rather than a row per token.
	T.Run("keys a row on its scope and its login", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			test.StrContains(t, strings.Join(stmts, "\n"), "PRIMARY KEY (scope, family_id)",
				test.Sprintf("dialect %q", d))
		}
	})

	// The scope column carries no default, which is the one place this schema
	// departs from the module's habit of defaulting a text column to the empty
	// string. The empty string is tenancy.Global(), not the absence of a scope,
	// so a default would hand the global scope to a write that forgot the
	// column. The rest carry none either: the store binds every one.
	T.Run("gives no column a default", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			test.StrNotContains(t, strings.Join(stmts, "\n"), "DEFAULT", test.Sprintf("dialect %q", d))
		}
	})

	// The user column is an identifier this table cannot resolve, so that an
	// application whose directory is not identity's can adopt it — which is why
	// the privacy package ships an eraser rather than leaning on a cascade.
	T.Run("references no other table", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			test.StrNotContains(t, strings.Join(stmts, "\n"), "REFERENCES", test.Sprintf("dialect %q", d))
		}
	})

	T.Run("carries no convention triple", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			for _, column := range []string{"archived_at", "last_updated_at", "created_at"} {
				test.StrNotContains(t, joined, column, test.Sprintf("dialect %q column %q", d, column))
			}
		}
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("renders the same DDL as one body", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			body, err := SQL(d, "app")
			must.NoError(t, err)

			stmts, stmtErr := Statements(d, "app")
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
		test.Eq(t, []string{"signin_devices"}, tables)

		tables, err = Tables("app")
		must.NoError(t, err)
		test.Eq(t, []string{"app_signin_devices"}, tables)
	})

	T.Run("rejects a prefix the schema cannot render", func(t *testing.T) {
		t.Parallel()

		tables, err := Tables("app_")
		test.Nil(t, tables)
		test.ErrorIs(t, err, ddl.ErrPrefixTrailingSeparator)
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts a namespace the schema can render", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"", "app", "app_two"} {
			test.NoError(t, ValidatePrefix(prefix), test.Sprintf("prefix %q", prefix))
		}
	})

	T.Run("rejects a namespace that is not an identifier fragment", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"app-1", "a b", "signin_; DROP TABLE users;--"} {
			test.Error(t, ValidatePrefix(prefix), test.Sprintf("prefix %q", prefix))
		}
	})

	T.Run("rejects a namespace that renders an over-long identifier", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix(strings.Repeat("a", ddl.MaxIdentifierLength)), ddl.ErrPrefixTooLong)
	})

	T.Run("rejects a namespace that ends in the separator", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix("app_"), ddl.ErrPrefixTrailingSeparator)
	})
}

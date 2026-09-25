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

			test.StrContains(t, joined, "custom_oauth2_grants", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_oauth2_grants_subject_provider_uniq", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, ddl.Placeholder, test.Sprintf("dialect %q", d))
		}
	})

	T.Run("an empty prefix renders the schema's own names", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "oauth2_grants", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, "_oauth2_grants ", test.Sprintf("dialect %q", d))
		}
	})

	// One grant per subject per provider is the rule a consent's replacement
	// rests on, and it covers revoked rows too: a consent deletes what the key
	// holds before it inserts, so a partial clause would only make room for a
	// second row nothing ever writes.
	T.Run("the uniqueness covers every row on every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "(scope, subject, provider)", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, "WHERE archived_at IS NULL", test.Sprintf("dialect %q", d))
		}
	})

	T.Run("creates the table before its index", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
			stmts, err := Statements(d, "")
			must.NoError(t, err)
			must.SliceLen(t, 2, stmts, must.Sprintf("dialect %q", d))

			test.StrContains(t, stmts[0], "CREATE TABLE", test.Sprintf("dialect %q", d))
			test.StrContains(t, stmts[1], "CREATE UNIQUE INDEX", test.Sprintf("dialect %q", d))
		}
	})

	// MySQL has no CREATE INDEX IF NOT EXISTS, so its key is declared inside the
	// table. One statement is the right answer there, and two would be a
	// migration that fails on its second run.
	T.Run("mysql declares its index inline", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.MySQL, "")
		must.NoError(t, err)
		must.SliceLen(t, 1, stmts)
		test.StrContains(t, stmts[0], "UNIQUE KEY oauth2_grants_subject_provider_uniq (scope, subject, provider)")
	})

	// The empty string is tenancy.Global() rather than the absence of a scope,
	// so a default would file the write that forgot the column in the tenant
	// that matches nobody.
	T.Run("the scope column carries no default", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")
			scopeAt := strings.Index(joined, "scope ")
			must.True(t, scopeAt >= 0, must.Sprintf("dialect %q", d))

			line := joined[scopeAt : strings.Index(joined[scopeAt:], "\n")+scopeAt]
			test.StrNotContains(t, line, "DEFAULT", test.Sprintf("dialect %q", d))
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

		for _, prefix := range []string{"ddb-1", "a b", "grants; DROP TABLE users;--"} {
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

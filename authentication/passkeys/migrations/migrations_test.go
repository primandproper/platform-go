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

			test.StrContains(t, joined, "custom_webauthn_credentials", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_webauthn_credentials_credential_id_uniq", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_webauthn_credentials_user_idx", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, ddl.Placeholder, test.Sprintf("dialect %q", d))
		}
	})

	// An empty namespace is the ordinary case, not a missing value: it renders
	// the schema's own names, which is what a consumer with one application per
	// database wants.
	T.Run("an empty prefix renders the schema's own names", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "webauthn_credentials", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, "_webauthn_credentials", test.Sprintf("dialect %q", d))
		}
	})

	// The uniqueness this table rests on is the one thing every dialect has to
	// spell, and two of them spell it a way the third cannot. A schema that lost
	// the clause would still create a table, still pass sqlc, and still serve
	// every read — and would refuse a re-enrollment forever.
	T.Run("the uniqueness is live-rows-only on every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "CREATE UNIQUE INDEX", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "WHERE archived_at IS NULL", test.Sprintf("dialect %q", d))
		}

		// MySQL has no partial index, so the predicate lives in a generated
		// column the unique key is declared over: the credential id of a live
		// row, NULL of an archived one, and a unique index there admits any
		// number of NULLs.
		mysql, err := Statements(dialect.MySQL, "")
		must.NoError(t, err)
		must.SliceLen(t, 1, mysql)

		test.StrContains(t, mysql[0], "GENERATED ALWAYS AS")
		test.StrContains(t, mysql[0], "CASE WHEN archived_at IS NULL THEN credential_id END")
		test.StrContains(t, mysql[0], "UNIQUE KEY webauthn_credentials_credential_id_uniq (scope, live_credential_id)")
	})

	// An index cannot be created before the table it indexes.
	T.Run("creates the table before its indexes", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
			stmts, err := Statements(d, "")
			must.NoError(t, err)
			must.SliceLen(t, 3, stmts, must.Sprintf("dialect %q", d))

			test.StrContains(t, stmts[0], "CREATE TABLE", test.Sprintf("dialect %q", d))
			test.StrContains(t, stmts[1], "CREATE UNIQUE INDEX", test.Sprintf("dialect %q", d))
			test.StrContains(t, stmts[2], "CREATE INDEX", test.Sprintf("dialect %q", d))
		}
	})

	// MySQL has no CREATE INDEX IF NOT EXISTS, so its keys are declared inside
	// the table. One statement is the right answer there, and two would be a
	// migration that fails on its second run.
	T.Run("mysql declares its indexes inline", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.MySQL, "")
		must.NoError(t, err)
		must.SliceLen(t, 1, stmts)
		test.StrContains(t, stmts[0], "KEY webauthn_credentials_user_idx")
	})

	// The empty string is tenancy.Global() rather than the absence of a scope,
	// so a default would file the write that forgot the column in the tenant
	// that matches nobody. internal/scopeddl checks this across the module; the
	// case is here too because this is the schema a reader is looking at.
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

		for _, prefix := range []string{"ddb-1", "a b", "webauthn_; DROP TABLE users;--"} {
			test.Error(t, ValidatePrefix(prefix), test.Sprintf("prefix %q", prefix))
		}
	})

	// The unique index's name is the longest identifier this schema renders, so
	// it is the one a long prefix pushes over the limit. Catching it here turns
	// a migration that half ran into a config that would not load.
	T.Run("rejects a namespace that renders an over-long identifier", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix(strings.Repeat("a", ddl.MaxIdentifierLength)), ddl.ErrPrefixTooLong)
	})

	T.Run("rejects a namespace that ends in the separator", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix("ddb_"), ddl.ErrPrefixTrailingSeparator)
	})
}

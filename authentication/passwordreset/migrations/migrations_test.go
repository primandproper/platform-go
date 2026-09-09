package migrations

import (
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"

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

			test.StrContains(t, joined, "custom_password_reset_tokens", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_password_reset_tokens_digest_idx", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_password_reset_tokens_user_idx", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_password_reset_tokens_expires_at_idx", test.Sprintf("dialect %q", d))
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

			test.StrContains(t, joined, "password_reset_tokens", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, "_password_reset_tokens", test.Sprintf("dialect %q", d))
		}
	})

	// The lookup every verification makes is unique, not merely indexed: two
	// live rows with one digest would be one link that unlocks two accounts.
	T.Run("makes the digest unique", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "UNIQUE", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "password_reset_tokens_digest_idx", test.Sprintf("dialect %q", d))
		}
	})

	// The scope column carries no default, which is the one place this schema
	// departs from the module's habit of defaulting a text column to the empty
	// string. The empty string is tenancy.Global(), not the absence of a scope.
	T.Run("gives the scope column no default", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			scopeAt := strings.Index(joined, "scope")
			must.True(t, scopeAt >= 0, must.Sprintf("dialect %q", d))

			line := joined[scopeAt : strings.Index(joined[scopeAt:], "\n")+scopeAt]
			test.StrNotContains(t, line, "DEFAULT", test.Sprintf("dialect %q", d))
		}
	})

	// A reset token is issued, used once, and gone. archived_at would keep rows
	// nothing can read, and last_updated_at would be a second copy of
	// redeemed_at.
	T.Run("carries no convention triple", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrNotContains(t, joined, "archived_at", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, "last_updated_at", test.Sprintf("dialect %q", d))
		}
	})

	// An index cannot be created before the table it indexes.
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
	// inside the table. One statement is the right answer there, and four would
	// be a migration that fails on its second run.
	T.Run("mysql declares its indexes inline", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.MySQL, "")
		must.NoError(t, err)
		must.SliceLen(t, 1, stmts)

		test.StrContains(t, stmts[0], "UNIQUE KEY password_reset_tokens_digest_idx")
		test.StrContains(t, stmts[0], "KEY password_reset_tokens_user_idx")
		test.StrContains(t, stmts[0], "KEY password_reset_tokens_expires_at_idx")
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
		test.Eq(t, []string{"password_reset_tokens"}, tables)

		tables, err = Tables("ddb")
		must.NoError(t, err)
		test.Eq(t, []string{"ddb_password_reset_tokens"}, tables)
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

		for _, prefix := range []string{"ddb-1", "a b", "reset_; DROP TABLE users;--"} {
			test.Error(t, ValidatePrefix(prefix), test.Sprintf("prefix %q", prefix))
		}
	})

	// The expiry index's name is the longest identifier this schema renders, so
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

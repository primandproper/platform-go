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

	T.Run("renders every dialect as one statement", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)
			must.SliceLen(t, 1, stmts, must.Sprintf("dialect %q", d))
			test.StrContains(t, stmts[0], "CREATE TABLE", test.Sprintf("dialect %q", d))
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

			test.StrContains(t, joined, "custom_signin_recovery_codes", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, ddl.Placeholder, test.Sprintf("dialect %q", d))
		}
	})

	// The key is the owner and the digest together. Sixty bits is enough that
	// nobody guesses a code through a door that counts failures, and not enough
	// that two people's codes never collide across a directory — so a key on the
	// digest alone would be a mint that fails, some day, for somebody else's
	// code.
	T.Run("keys a code on its owner and its digest", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			test.StrContains(t, strings.Join(stmts, "\n"), "PRIMARY KEY (scope, user_id, hash)",
				test.Sprintf("dialect %q", d))
		}
	})

	// The scope column carries no default, which is the one place this schema
	// departs from the module's habit of defaulting a text column to the empty
	// string. The empty string is tenancy.Global(), not the absence of a scope,
	// so a default would hand the global scope to a write that forgot the
	// column.
	T.Run("gives no column a default", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			test.StrNotContains(t, strings.Join(stmts, "\n"), "DEFAULT", test.Sprintf("dialect %q", d))
		}
	})

	// A recovery code does not lapse, so there is nothing for a sweep to key
	// on; and a replaced set must stop working, so there is nothing for an
	// archive to keep.
	T.Run("carries no deadline and no convention triple", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			for _, column := range []string{"expires_at", "purge_after", "archived_at", "last_updated_at"} {
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
		test.Eq(t, []string{"signin_recovery_codes"}, tables)

		tables, err = Tables("ddb")
		must.NoError(t, err)
		test.Eq(t, []string{"ddb_signin_recovery_codes"}, tables)
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

		for _, prefix := range []string{"ddb-1", "a b", "signin_; DROP TABLE users;--"} {
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

package migrations

import (
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is the roster the renderings are asserted over.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders the table before its indexes", func(t *testing.T) {
		t.Parallel()

		// MySQL declares its indexes inside the table, having no partial
		// index to give either of them a statement of its own.
		want := map[dialect.Dialect]int{dialect.Postgres: 4, dialect.MySQL: 1, dialect.SQLite: 4}

		for _, d := range everyDialect {
			stmts, err := Statements(d, "")

			must.NoError(t, err)
			must.SliceLen(t, want[d], stmts, must.Sprintf("dialect %s", d))

			test.StrContains(t, stmts[0], "CREATE TABLE")
			test.StrContains(t, stmts[0], "operations")

			for _, stmt := range stmts[1:] {
				test.StrContains(t, stmt, "CREATE INDEX")
			}
		}
	})

	T.Run("the prefix reaches every identifier", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			stmts, err := Statements(d, "ddb")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "ddb_operations")
			test.StrNotContains(t, joined, ddl.Placeholder)

			// An index that kept the unprefixed name would collide with another
			// application's in a shared database, which is the whole reason the
			// prefix exists.
			test.StrNotContains(t, joined, " operations_", test.Sprintf("dialect %s", d))
		}
	})

	T.Run("rejects a dialect this module does not name", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Dialect("oracle"), ""} {
			stmts, err := Statements(d, "")

			test.ErrorIs(t, err, dialect.ErrUnsupported, test.Sprintf("dialect %q", d))
			test.SliceEmpty(t, stmts)

			body, err := SQL(d, "")

			test.ErrorIs(t, err, dialect.ErrUnsupported)
			test.EqOp(t, "", body)
		}
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		body, err := SQL(d, "")

		must.NoError(T, err)
		test.StrContains(T, body, "CREATE TABLE")

		// Comments are stripped by the shared renderer, which matters because
		// goose splits a migration on semicolons and a '--' comment containing
		// one would be torn in half.
		test.StrNotContains(T, body, "--", test.Sprintf("dialect %s", d))
	}
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	test.NoError(T, ValidatePrefix(""))
	test.NoError(T, ValidatePrefix("ddb"))

	// database/ddl supplies the separator, so a prefix that brings its own
	// renders a double underscore.
	test.Error(T, ValidatePrefix("ddb_"))

	test.Error(T, ValidatePrefix("has spaces"))
	test.Error(T, ValidatePrefix(strings.Repeat("a", 64)))
}

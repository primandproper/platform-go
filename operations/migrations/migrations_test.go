package migrations

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders the table before its indexes", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.Postgres, "")

		must.NoError(t, err)
		must.SliceNotEmpty(t, stmts)

		test.StrContains(t, stmts[0], "CREATE TABLE")
		test.StrContains(t, stmts[0], "operations")

		for _, stmt := range stmts[1:] {
			test.StrContains(t, stmt, "CREATE INDEX")
		}
	})

	T.Run("the prefix reaches every identifier", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.Postgres, "ddb")
		must.NoError(t, err)

		joined := strings.Join(stmts, "\n")

		test.StrContains(t, joined, "ddb_operations")

		// An index that kept the unprefixed name would collide with another
		// application's in a shared database, which is the whole reason the
		// prefix exists.
		test.StrNotContains(t, joined, " operations_")
	})

	// The reason this package guards the dialect itself: the shared renderer
	// reads the member for the dialect it was asked about, and an absent member
	// is an empty string rather than a missing one. Without the guard, asking
	// for MySQL would return no statements and no error — a migration run that
	// creates nothing and reports success.
	T.Run("refuses a dialect it has no schema for", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.MySQL, dialect.SQLite, dialect.Dialect("oracle")} {
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

	body, err := SQL(dialect.Postgres, "")

	must.NoError(T, err)
	test.StrContains(T, body, "CREATE TABLE")
	test.StrContains(T, body, "CREATE INDEX")

	// Comments are stripped by the shared renderer, which matters because goose
	// splits a migration on semicolons and a '--' comment containing one would
	// be torn in half.
	test.StrNotContains(T, body, "--")
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

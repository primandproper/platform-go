package migrations

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders every supported dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := Statements(d, "dp")
			must.NoError(t, err, must.Sprintf("dialect %s", d))
			must.SliceNotEmpty(t, stmts)

			// The table is created before the indexes that reference it.
			test.StrContains(t, stmts[0], "CREATE TABLE")
			test.StrContains(t, stmts[0], "dp_dataprivacy_requests")

			for _, stmt := range stmts {
				test.StrNotContains(t, stmt, ddl.Placeholder, test.Sprintf("dialect %s", d))

				// Comments are stripped, which matters: goose splits a
				// migration on semicolons and would tear a '--' comment
				// containing one in half.
				test.StrNotContains(t, stmt, "--")
			}
		}
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := Statements(dialect.Dialect("oracle"), "dp")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects a prefix that would not render a legal identifier", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"drop table;--", "has space", "1leading"} {
			_, err := Statements(dialect.SQLite, prefix)
			test.ErrorIs(t, err, dialect.ErrInvalidIdentifier, test.Sprintf("prefix %q", prefix))
		}
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("joins the statements into one migration body", func(t *testing.T) {
		t.Parallel()

		body, err := SQL(dialect.Postgres, "dp")
		must.NoError(t, err)

		stmts, err := Statements(dialect.Postgres, "dp")
		must.NoError(t, err)

		test.EqOp(t, len(stmts), strings.Count(body, ";"))
		test.True(t, strings.HasSuffix(body, ";\n"))
	})

	T.Run("propagates a bad dialect", func(t *testing.T) {
		t.Parallel()

		_, err := SQL(dialect.Dialect("oracle"), "dp")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts a plain identifier fragment", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"dp", "dataprivacy", "_private", "a1"} {
			test.NoError(t, ValidatePrefix(prefix), test.Sprintf("prefix %q", prefix))
		}
	})

	T.Run("accepts an empty prefix, which renders the component's own names", func(t *testing.T) {
		t.Parallel()

		// Empty is the ordinary case, not a missing value: it is what a
		// consumer with one application per database wants.
		test.NoError(t, ValidatePrefix(""))
	})
}

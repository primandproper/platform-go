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
			stmts, err := Statements(d, "")
			must.NoError(t, err, must.Sprintf("dialect %q", d))
			must.SliceNotEmpty(t, stmts)

			// The table before its indexes, or the indexes have nothing to be
			// on.
			test.StrContains(t, stmts[0], "CREATE TABLE")

			for _, stmt := range stmts {
				test.StrNotContains(t, stmt, ddl.Placeholder)
				test.StrContains(t, stmt, "saga_instances")
			}
		}
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := Statements(dialect.Dialect("oracle"), "saga")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects a prefix that would not render a legal identifier", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"saga-1", "drop table;--", "9saga"} {
			_, err := Statements(dialect.SQLite, prefix)
			test.ErrorIs(t, err, dialect.ErrInvalidIdentifier, test.Sprintf("prefix %q", prefix))
		}
	})

	T.Run("carries every column the store reads", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			create := stmts[0]
			for _, column := range []string{
				"id", "definition", "status", "current_step", "step_names", "state",
				"attempts", "last_error", "resume_status", "started_at", "updated_at",
				"next_attempt", "claimed_until",
			} {
				test.StrContains(t, create, column, test.Sprintf("dialect %q column %q", d, column))
			}
		}
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts a plain identifier fragment", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, ValidatePrefix("saga"))
		test.NoError(t, ValidatePrefix("app_saga"))
	})

	T.Run("accepts an empty prefix, which renders the component's own names", func(t *testing.T) {
		t.Parallel()

		// Empty is the ordinary case, not a missing value: it is what a
		// consumer with one application per database wants.
		test.NoError(t, ValidatePrefix(""))
	})

	T.Run("names the table that would be illegal", func(t *testing.T) {
		t.Parallel()

		err := ValidatePrefix("saga-1")
		must.Error(t, err)
		test.StrContains(t, err.Error(), "saga-1_saga_instances")
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("joins the statements into one migration body", func(t *testing.T) {
		t.Parallel()

		body, err := SQL(dialect.Postgres, "saga")
		must.NoError(t, err)

		stmts, err := Statements(dialect.Postgres, "saga")
		must.NoError(t, err)

		test.EqOp(t, len(stmts), strings.Count(body, ";"))
		test.True(t, strings.HasSuffix(body, ";\n"))

		// Comments are already stripped: goose splits on semicolons, and a '--'
		// comment containing one would be torn in half.
		test.StrNotContains(t, body, "--")
	})

	T.Run("propagates a rendering failure", func(t *testing.T) {
		t.Parallel()

		_, err := SQL(dialect.Dialect("oracle"), "saga")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}

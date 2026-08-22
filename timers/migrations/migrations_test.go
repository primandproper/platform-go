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

	T.Run("renders the table name into every statement", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.Postgres, "ddb")
		must.NoError(t, err)
		must.SliceNotEmpty(t, stmts)

		for _, stmt := range stmts {
			test.True(t, strings.Contains(stmt, "ddb_scheduled_timers"),
				test.Sprintf("statement missing table name: %s", stmt))
			test.False(t, strings.Contains(stmt, ddl.Placeholder),
				test.Sprintf("statement left an unrendered placeholder: %s", stmt))
		}
	})

	T.Run("puts the table before its indexes", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.Postgres, "")
		must.NoError(t, err)
		must.SliceLen(t, 3, stmts)

		test.True(t, strings.HasPrefix(stmts[0], "CREATE TABLE"))
		for _, stmt := range stmts[1:] {
			test.True(t, strings.HasPrefix(stmt, "CREATE INDEX"))
		}
	})

	T.Run("strips comments and empty fragments", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.Postgres, "")
		must.NoError(t, err)

		for _, stmt := range stmts {
			test.False(t, strings.Contains(stmt, "--"), test.Sprintf("statement leaked a comment: %s", stmt))
			test.EqOp(t, stmt, strings.TrimSpace(stmt))
		}
	})

	// A dialect with no body would otherwise render zero statements and no
	// error, leaving a caller with nothing created and no way to tell.
	T.Run("rejects the dialects this package has no schema for", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.MySQL, dialect.SQLite, dialect.Dialect("oracle"), ""} {
			_, err := Statements(d, "")
			test.ErrorIs(t, err, dialect.ErrUnsupported, test.Sprintf("dialect %q", d))

			_, err = SQL(d, "")
			test.ErrorIs(t, err, dialect.ErrUnsupported, test.Sprintf("dialect %q", d))
		}
	})

	T.Run("rejects a prefix that would render an illegal identifier", func(t *testing.T) {
		t.Parallel()

		_, err := Statements(dialect.Postgres, "not a prefix")
		test.Error(t, err)
	})

	T.Run("rejects a prefix carrying its own separator", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix("ddb_"), ddl.ErrPrefixTrailingSeparator)
	})

	T.Run("rejects a prefix that pushes an index name over the limit", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix(strings.Repeat("p", ddl.MaxIdentifierLength)), ddl.ErrPrefixTooLong)
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	// goose splits a migration on semicolons, so a comment containing one would
	// be torn in half. The renderer strips them before joining; this is the
	// assertion that keeps that true.
	T.Run("joins the statements with no comments left in", func(t *testing.T) {
		t.Parallel()

		body, err := SQL(dialect.Postgres, "ddb")
		must.NoError(t, err)

		test.False(t, strings.Contains(body, "--"))
		test.True(t, strings.Contains(body, "ddb_scheduled_timers"))
		test.True(t, strings.HasSuffix(body, ";\n"))
	})
}

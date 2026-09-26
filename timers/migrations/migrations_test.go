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

	T.Run("renders the table name into every statement", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			stmts, err := Statements(d, "ddb")
			must.NoError(t, err)
			must.SliceNotEmpty(t, stmts)

			for _, stmt := range stmts {
				test.True(t, strings.Contains(stmt, "ddb_scheduled_timers"),
					test.Sprintf("%s statement missing table name: %s", d, stmt))
				test.False(t, strings.Contains(stmt, ddl.Placeholder),
					test.Sprintf("%s statement left an unrendered placeholder: %s", d, stmt))
			}
		}
	})

	T.Run("puts the table before its indexes", func(t *testing.T) {
		t.Parallel()

		// MySQL declares its one index inside the table, having no partial
		// index to give the reaper a second one of its own.
		want := map[dialect.Dialect]int{dialect.Postgres: 3, dialect.MySQL: 1, dialect.SQLite: 3}

		for _, d := range everyDialect {
			stmts, err := Statements(d, "")
			must.NoError(t, err)
			must.SliceLen(t, want[d], stmts, must.Sprintf("dialect %s", d))

			test.True(t, strings.HasPrefix(stmts[0], "CREATE TABLE"))
			for _, stmt := range stmts[1:] {
				test.True(t, strings.HasPrefix(stmt, "CREATE INDEX"))
			}
		}
	})

	T.Run("strips comments and empty fragments", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			for _, stmt := range stmts {
				test.False(t, strings.Contains(stmt, "--"), test.Sprintf("%s statement leaked a comment: %s", d, stmt))
				test.EqOp(t, stmt, strings.TrimSpace(stmt))
			}
		}
	})

	// The largest payload the package admits has to be one the column takes.
	// MySQL's BLOB is 65535 bytes, one short of MaxPayloadSize, and a strict
	// server refuses the write while a lax one truncates it.
	T.Run("gives MySQL a payload column the size limit fits in", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.MySQL, "")
		must.NoError(t, err)

		test.StrContains(t, stmts[0], "payload         MEDIUMBLOB")
	})

	T.Run("rejects a dialect this module does not name", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Dialect("oracle"), ""} {
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

		for _, d := range everyDialect {
			body, err := SQL(d, "ddb")
			must.NoError(t, err)

			test.False(t, strings.Contains(body, "--"), test.Sprintf("dialect %s", d))
			test.True(t, strings.Contains(body, "ddb_scheduled_timers"), test.Sprintf("dialect %s", d))
			test.True(t, strings.HasSuffix(body, ";\n"), test.Sprintf("dialect %s", d))
		}
	})
}

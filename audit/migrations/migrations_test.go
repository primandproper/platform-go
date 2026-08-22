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

	T.Run("renders both tables for every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			t.Run(string(d), func(t *testing.T) {
				t.Parallel()

				stmts, err := Statements(d, "")
				must.NoError(t, err)
				must.SliceNotEmpty(t, stmts)

				joined := strings.Join(stmts, "\n")
				test.StrContains(t, joined, "audit_log_entries")
				test.StrContains(t, joined, "audit_log_chains")
				test.StrNotContains(t, joined, prefixPlaceholder)

				// The uniqueness of (scope, seq) is the guarantee that a forked
				// chain cannot commit, so it is not optional in any dialect.
				test.StrContains(t, joined, "UNIQUE")

				for _, stmt := range stmts {
					test.StrNotContains(t, stmt, "--")
					test.StrNotContains(t, stmt, ";")
				}
			})
		}
	})

	T.Run("orders the table ahead of its indexes", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.Postgres, "")
		must.NoError(t, err)
		must.SliceNotEmpty(t, stmts)

		test.StrContains(t, stmts[0], "CREATE TABLE")
	})

	T.Run("accepts an empty prefix", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.SQLite, "")
		must.NoError(t, err)
		test.StrContains(t, strings.Join(stmts, "\n"), "CREATE TABLE IF NOT EXISTS audit_log_entries")
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := Statements("cassandra", "audit")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects an unsafe prefix", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"audit-", "audit_; DROP TABLE users; --", "1audit"} {
			_, err := Statements(dialect.Postgres, prefix)
			test.ErrorIs(t, err, ErrInvalidPrefix, test.Sprintf("prefix %q", prefix))
		}
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("joins the statements back into a migration body", func(t *testing.T) {
		t.Parallel()

		body, err := SQL(dialect.Postgres, "audit")
		must.NoError(t, err)

		test.StrContains(t, body, "CREATE TABLE")
		test.StrHasSuffix(t, ";\n", body)

		// Comments are stripped before joining: goose splits on semicolons, and
		// a '--' comment containing one would be torn in half.
		test.StrNotContains(t, body, "--")
	})

	T.Run("propagates a rendering error", func(t *testing.T) {
		t.Parallel()

		_, err := SQL("cassandra", "audit")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}

func TestAppendOnlyStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders an update-rejecting trigger for every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			t.Run(string(d), func(t *testing.T) {
				t.Parallel()

				stmts, err := AppendOnlyStatements(d, "")
				must.NoError(t, err)
				must.SliceNotEmpty(t, stmts)

				joined := strings.Join(stmts, "\n")
				test.StrContains(t, joined, "audit_log_entries")
				test.StrContains(t, joined, "BEFORE UPDATE")
				test.StrContains(t, joined, appendOnlyMessage)

				// DELETE is deliberately not blocked: retention has to delete,
				// and the chain is what covers deletion instead.
				test.StrNotContains(t, joined, "BEFORE DELETE")
			})
		}
	})

	T.Run("keeps a plpgsql body whole", func(t *testing.T) {
		t.Parallel()

		stmts, err := AppendOnlyStatements(dialect.Postgres, "")
		must.NoError(t, err)
		must.SliceLen(t, 2, stmts)

		// The function's body contains semicolons, which is exactly why these
		// are returned pre-split and never joined for a tool that would split
		// them again.
		test.StrContains(t, stmts[0], "RAISE EXCEPTION")
		test.StrContains(t, stmts[0], "LANGUAGE plpgsql")
		test.StrContains(t, stmts[1], "CREATE TRIGGER")
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := AppendOnlyStatements("cassandra", "audit")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects an unsafe prefix", func(t *testing.T) {
		t.Parallel()

		_, err := AppendOnlyStatements(dialect.Postgres, "audit-")
		test.ErrorIs(t, err, ErrInvalidPrefix)
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts an empty namespace", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, ValidatePrefix(""))
	})

	T.Run("accepts a plain identifier fragment", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, ValidatePrefix("ddb"))
	})

	T.Run("rejects a malformed namespace with this package's own sentinel", func(t *testing.T) {
		t.Parallel()

		// The local regex runs before the shared check so a malformed namespace
		// still reports ErrInvalidPrefix rather than the dialect package's.
		test.ErrorIs(t, ValidatePrefix("ddb-1"), ErrInvalidPrefix)
	})

	T.Run("rejects a trailing separator", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix("ddb_"), ddl.ErrPrefixTrailingSeparator)
	})

	T.Run("rejects a namespace that pushes an index name past the limit", func(t *testing.T) {
		t.Parallel()

		// The four index names are the longest identifiers this schema renders,
		// and the ones the local regex cannot see.
		namespace := strings.Repeat("n", ddl.MaxIdentifierLength-len("audit_log_entries_scope_time_idx"))

		test.ErrorIs(t, ValidatePrefix(namespace), ddl.ErrPrefixTooLong)
	})
}

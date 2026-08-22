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

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := Statements(d, "events_outbox")
			must.NoError(t, err)
			must.True(t, len(stmts) > 0)

			for _, stmt := range stmts {
				test.True(t, strings.Contains(stmt, "events_outbox"),
					test.Sprintf("dialect %q statement missing table name: %s", d, stmt))
				test.False(t, strings.Contains(stmt, ddl.Placeholder),
					test.Sprintf("dialect %q left an unrendered placeholder", d))
			}
		}
	})

	T.Run("puts the table before its indexes", func(t *testing.T) {
		t.Parallel()

		// Postgres declares indexes separately, so ordering matters here in a
		// way it does not for MySQL's inline KEY clauses.
		stmts, err := Statements(dialect.Postgres, "")
		must.NoError(t, err)
		must.True(t, len(stmts) >= 2)

		test.True(t, strings.HasPrefix(stmts[0], "CREATE TABLE"))
		for _, stmt := range stmts[1:] {
			test.True(t, strings.HasPrefix(stmt, "CREATE INDEX"))
		}
	})

	T.Run("strips comments and empty fragments", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			for _, stmt := range stmts {
				test.False(t, strings.Contains(stmt, "--"),
					test.Sprintf("dialect %q leaked a comment: %s", d, stmt))
				test.EqOp(t, stmt, strings.TrimSpace(stmt))
			}
		}
	})

	T.Run("survives a semicolon inside a comment", func(t *testing.T) {
		t.Parallel()

		// Regression: comments were stripped after splitting on ';', so a
		// comment containing a semicolon was torn in half and its tail arrived
		// at the head of the next statement as bogus SQL. MariaDB rejected the
		// result; Postgres never saw it, because only the MySQL DDL happened to
		// have prose with a semicolon in it.
		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			for _, stmt := range stmts {
				test.True(t, strings.HasPrefix(stmt, "CREATE "),
					test.Sprintf("dialect %q produced a non-DDL fragment: %q", d, stmt))
			}
		}
	})

	T.Run("declares the columns the relay reads and writes", func(t *testing.T) {
		t.Parallel()

		required := []string{
			"id", "topic", "partition_key", "payload", "created_at",
			"next_attempt", "claimed_until", "published_at", "attempts", "last_error", "quarantined",
		}

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			for _, col := range required {
				test.True(t, strings.Contains(stmts[0], col),
					test.Sprintf("dialect %q is missing column %q", d, col))
			}
		}
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := Statements("cassandra", "outbox_messages")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects a table name that is not an identifier", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"outbox messages", "outbox; DROP TABLE users", "1outbox"} {
			_, err := Statements(dialect.Postgres, name)
			test.ErrorIs(t, err, dialect.ErrInvalidIdentifier,
				test.Sprintf("expected %q to be rejected", name))
		}
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("joins every statement into one terminated body", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := Statements(d, "events_outbox")
			must.NoError(t, err)

			body, err := SQL(d, "events_outbox")
			must.NoError(t, err)

			test.EqOp(t, strings.Join(stmts, ";\n\n")+";\n", body,
				test.Sprintf("dialect %q", d))

			// Every statement is present and each one is terminated, which is
			// what goose needs to split the body back apart.
			test.EqOp(t, len(stmts), strings.Count(body, ";"),
				test.Sprintf("dialect %q has an unterminated statement", d))
			test.True(t, strings.HasSuffix(body, ";\n"),
				test.Sprintf("dialect %q body is not terminated", d))
		}
	})

	T.Run("renders the table name and leaks no comments", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			body, err := SQL(d, "events_outbox")
			must.NoError(t, err)

			test.True(t, strings.Contains(body, "events_outbox"),
				test.Sprintf("dialect %q missing table name", d))
			test.False(t, strings.Contains(body, ddl.Placeholder),
				test.Sprintf("dialect %q left an unrendered placeholder", d))

			// Comments must already be gone: goose splits on ';', so a comment
			// carrying one would be torn in half.
			test.False(t, strings.Contains(body, "--"),
				test.Sprintf("dialect %q leaked a comment", d))
		}
	})

	T.Run("propagates an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		body, err := SQL("cassandra", "outbox_messages")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
		test.EqOp(t, "", body)
	})

	T.Run("propagates a table name that is not an identifier", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"outbox messages", "outbox; DROP TABLE users", "1outbox"} {
			body, err := SQL(dialect.Postgres, name)
			test.ErrorIs(t, err, dialect.ErrInvalidIdentifier,
				test.Sprintf("expected %q to be rejected", name))
			test.EqOp(t, "", body)
		}
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts an empty namespace", func(t *testing.T) {
		t.Parallel()

		// Empty renders the component's own names, which is the default.
		test.NoError(t, ValidatePrefix(""))
	})

	T.Run("accepts a plain identifier fragment", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, ValidatePrefix("ddb"))
	})

	T.Run("rejects a trailing separator", func(t *testing.T) {
		t.Parallel()

		// The renderer supplies the separator; a namespace carrying one too
		// would render a doubled separator rather than an error.
		test.ErrorIs(t, ValidatePrefix("ddb_"), ddl.ErrPrefixTrailingSeparator)
	})

	T.Run("rejects a namespace that would not render an identifier", func(t *testing.T) {
		t.Parallel()

		for _, namespace := range []string{"ddb-1", "1ddb", "ddb 1"} {
			test.ErrorIs(t, ValidatePrefix(namespace), dialect.ErrInvalidIdentifier,
				test.Sprintf("namespace %q", namespace))
		}
	})

	T.Run("rejects a namespace that pushes an index name past the limit", func(t *testing.T) {
		t.Parallel()

		// Index names are the longest identifiers this schema renders, and are
		// what a table-only check would miss.
		namespace := strings.Repeat("n", ddl.MaxIdentifierLength-len("outbox_messages_ordering_idx"))

		test.ErrorIs(t, ValidatePrefix(namespace), ddl.ErrPrefixTooLong)
	})
}

package migrations

import (
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := Statements(d, "")
			must.NoError(t, err)
			test.True(t, len(stmts) > 0)

			for _, stmt := range stmts {
				// Comments are stripped before the split, so no statement may
				// carry one — goose splits on semicolons and a '--' comment
				// containing one would be torn in half.
				test.False(t, strings.Contains(stmt, "--"))
				test.False(t, strings.Contains(stmt, ddl.Placeholder))
			}
		}
	})

	// Every table has to exist before anything referencing it. A migration that
	// creates webhooks_subscriptions before webhooks_endpoints fails outright on
	// the foreign key.
	T.Run("creates tables in dependency order", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.True(t, indexOfTable(joined, "webhooks_endpoints") < indexOfTable(joined, "webhooks_subscriptions"))
			test.True(t, indexOfTable(joined, "webhooks_deliveries") < indexOfTable(joined, "webhooks_dispatches"))
		}
	})

	T.Run("renders the prefix into every table", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.Postgres, "acme_hook")
		must.NoError(t, err)

		joined := strings.Join(stmts, "\n")

		for _, suffix := range []string{"endpoints", "subscriptions", "deliveries", "dispatches", "attempts"} {
			test.True(t, strings.Contains(joined, "acme_hook_webhooks_"+suffix))
		}
	})

	T.Run("unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := Statements(dialect.Dialect("cockroach"), "webhook")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	// The prefix is interpolated into query text, not bound, so it is vetted
	// rather than escaped.
	T.Run("rejects a prefix that is not an identifier", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"web hook", "webhook; DROP TABLE users", "web-hook", "1webhook"} {
			_, err := Statements(dialect.Postgres, prefix)
			test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
		}
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		body, err := SQL(dialect.Postgres, "webhook")
		must.NoError(t, err)

		test.True(t, strings.HasSuffix(body, ";\n"))
		test.False(t, strings.Contains(body, "--"))

		stmts, err := Statements(dialect.Postgres, "webhook")
		must.NoError(t, err)
		test.EqOp(t, len(stmts), strings.Count(body, ";"))
	})

	T.Run("unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := SQL(dialect.Dialect("cockroach"), "webhook")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}

func TestUpgradeStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := UpgradeStatements(d, "")
			must.NoError(t, err)
			test.True(t, len(stmts) > 0)

			for _, stmt := range stmts {
				test.False(t, strings.Contains(stmt, "--"))
				test.False(t, strings.Contains(stmt, ddl.Placeholder))
			}
		}
	})

	// The columns the upgrade exists to add. A dialect that quietly lost one of
	// them would leave the store selecting a column that is not there, which
	// surfaces at the first read rather than at the migration.
	T.Run("adds every column the current schema gained", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := UpgradeStatements(d, "")
			must.NoError(t, err)

			// Postgres spells it ADD COLUMN IF NOT EXISTS and the other two cannot,
			// so the guard is dropped before matching rather than written out per
			// dialect — what is being checked is the column, not the spelling.
			joined := strings.ReplaceAll(strings.Join(stmts, "\n"), "ADD COLUMN IF NOT EXISTS ", "ADD COLUMN ")

			for _, column := range []string{"created_by", "name", "id", "created_at", "last_updated_at", "archived_at"} {
				test.True(t, strings.Contains(joined, "ADD COLUMN "+column),
					test.Sprintf("dialect %q column %q", d, column))
			}
		}
	})

	// Every identifier the upgrade names has to exist in the schema it upgrades
	// to, or the two have drifted: the upgrade would be creating an index the
	// create path does not, or under a name the store's prefix validation has
	// never seen.
	T.Run("names nothing the created schema does not", func(t *testing.T) {
		t.Parallel()

		created := map[string]struct{}{}
		for _, name := range schema.Identifiers("acme") {
			created[name] = struct{}{}
		}

		for _, name := range upgrade.Identifiers("acme") {
			_, ok := created[name]
			test.True(t, ok, test.Sprintf("upgrade names %q, which the schema does not create", name))
		}
	})

	T.Run("unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := UpgradeStatements(dialect.Dialect("cockroach"), "webhook")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects a prefix that is not an identifier", func(t *testing.T) {
		t.Parallel()

		_, err := UpgradeStatements(dialect.Postgres, "web hook")
		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
	})
}

func TestUpgradeSQL(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		body, err := UpgradeSQL(dialect.Postgres, "webhook")
		must.NoError(t, err)

		test.True(t, strings.HasSuffix(body, ";\n"))
		test.StrContains(t, body, "webhook_webhooks_subscriptions")

		stmts, err := UpgradeStatements(dialect.Postgres, "webhook")
		must.NoError(t, err)
		test.EqOp(t, len(stmts), strings.Count(body, ";"))
	})

	T.Run("unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := UpgradeSQL(dialect.Dialect("cockroach"), "webhook")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, ValidatePrefix("webhook"))
		test.NoError(t, ValidatePrefix("acme_hook"))
	})

	T.Run("rejects what would not be an identifier", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"web hook", "web-hook", "webhook;"} {
			test.ErrorIs(t, ValidatePrefix(prefix), dialect.ErrInvalidIdentifier)
		}
	})
}

// indexOfTable finds where a table is created, ignoring the index statements
// that also name it.
func indexOfTable(body, table string) int {
	return strings.Index(body, "TABLE IF NOT EXISTS "+table)
}

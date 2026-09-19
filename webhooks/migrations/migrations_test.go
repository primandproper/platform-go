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

	// This package once shipped a second, ALTER-only schema beside this one, to
	// carry a deployment whose subscriptions were a bare (endpoint_id,
	// event_type) mapping up to the shape here. Deleting it was safe only
	// because every column and index it added is created outright below, so
	// there was no end state it reached that this DDL does not — and that is the
	// claim worth keeping checkable rather than remembering. A column dropped
	// from here would not just lose a column; it would retroactively make the
	// deletion wrong.
	T.Run("creates everything the retired upgrade DDL added", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := Statements(d, "acme")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			for _, name := range []string{
				// The endpoint's metadata.
				"created_by", "name",
				// A subscription became an identified, archivable row.
				"id", "created_at", "last_updated_at", "archived_at",
				// The dispatch's lease holder.
				"claimed_by",
				// Both indexes the upgrade created, at the prefix.
				"acme_webhooks_subscriptions_id_idx",
				"acme_webhooks_subscriptions_endpoint_idx",
			} {
				test.StrContains(t, joined, name, test.Sprintf("dialect %q", d))
			}
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

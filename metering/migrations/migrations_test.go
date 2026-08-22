package migrations

import (
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// allDialects is every dialect this package renders DDL for.
var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders every supported dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, "mtr")
			must.NoError(t, err, must.Sprintf("dialect %s", d))
			must.SliceNotEmpty(t, stmts)

			// Each table before the indexes that reference it — a CREATE INDEX
			// against a table that does not exist yet is a migration that fails on
			// its first run and every run after it.
			eventsTable := slices.IndexFunc(stmts, func(s string) bool {
				return strings.Contains(s, "CREATE TABLE") && strings.Contains(s, "mtr_metering_events")
			})
			totalsTable := slices.IndexFunc(stmts, func(s string) bool {
				return strings.Contains(s, "CREATE TABLE") && strings.Contains(s, "mtr_metering_totals")
			})

			must.GreaterEq(t, 0, eventsTable, must.Sprintf("dialect %s", d))
			must.GreaterEq(t, 0, totalsTable, must.Sprintf("dialect %s", d))

			for i, stmt := range stmts {
				if strings.Contains(stmt, "CREATE INDEX") && strings.Contains(stmt, "mtr_metering_events") {
					test.Greater(t, eventsTable, i, test.Sprintf("dialect %s", d))
				}
				if strings.Contains(stmt, "CREATE INDEX") && strings.Contains(stmt, "mtr_metering_totals") {
					test.Greater(t, totalsTable, i, test.Sprintf("dialect %s", d))
				}

				test.StrNotContains(t, stmt, ddl.Placeholder, test.Sprintf("dialect %s", d))

				// Comments are stripped, which matters: goose splits a migration
				// on semicolons and would tear a '--' comment containing one in
				// half.
				test.StrNotContains(t, stmt, "--", test.Sprintf("dialect %s", d))
			}
		}
	})

	T.Run("keys the event ledger on the idempotency key", func(t *testing.T) {
		t.Parallel()

		// The dedupe that makes counting exactly-once. If this stops being the
		// primary key, every retry becomes a second invoice line.
		for _, d := range allDialects {
			stmts, err := Statements(d, "mtr")
			must.NoError(t, err)

			var found bool
			for _, stmt := range stmts {
				if strings.Contains(stmt, "CREATE TABLE") && strings.Contains(stmt, "mtr_metering_events") {
					found = true

					test.StrContains(t, stmt, "idempotency_key", test.Sprintf("dialect %s", d))
					test.StrContains(t, stmt, "PRIMARY KEY", test.Sprintf("dialect %s", d))
				}
			}

			test.True(t, found, test.Sprintf("dialect %s", d))
		}
	})

	T.Run("keys totals on subject, meter, and period", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, "mtr")
			must.NoError(t, err)

			var found bool
			for _, stmt := range stmts {
				if strings.Contains(stmt, "CREATE TABLE") && strings.Contains(stmt, "mtr_metering_totals") {
					found = true

					test.StrContains(t, stmt, "PRIMARY KEY (subject, meter, period_start)",
						test.Sprintf("dialect %s", d))
				}
			}

			test.True(t, found, test.Sprintf("dialect %s", d))
		}
	})

	T.Run("uses partial indexes only where they exist", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
			stmts, err := Statements(d, "mtr")
			must.NoError(t, err)

			test.True(t, slices.ContainsFunc(stmts, func(s string) bool {
				return strings.Contains(s, "mtr_metering_totals_flush_idx") &&
					strings.Contains(s, "WHERE quantity > flushed_quantity")
			}), test.Sprintf("dialect %s", d))
		}

		// MySQL has no partial indexes, so its flush index covers the whole table.
		stmts, err := Statements(dialect.MySQL, "mtr")
		must.NoError(t, err)

		for _, stmt := range stmts {
			if strings.Contains(stmt, "CREATE INDEX") {
				test.StrNotContains(t, stmt, "WHERE")
			}
		}
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := Statements(dialect.Dialect("oracle"), "mtr")
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

		body, err := SQL(dialect.Postgres, "mtr")
		must.NoError(t, err)

		stmts, err := Statements(dialect.Postgres, "mtr")
		must.NoError(t, err)

		test.EqOp(t, len(stmts), strings.Count(body, ";"))
		test.True(t, strings.HasSuffix(body, ";\n"))
	})

	T.Run("propagates a bad dialect", func(t *testing.T) {
		t.Parallel()

		_, err := SQL(dialect.Dialect("oracle"), "mtr")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts a plain identifier fragment", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"mtr", "metering", "_private", "a1"} {
			test.NoError(t, ValidatePrefix(prefix), test.Sprintf("prefix %q", prefix))
		}
	})

	T.Run("accepts an empty prefix, which renders the component's own names", func(t *testing.T) {
		t.Parallel()

		// Empty is the ordinary case, not a missing value: it is what a
		// consumer with one application per database wants.
		test.NoError(t, ValidatePrefix(""))
	})

	T.Run("vets every rendered name, not only the prefix", func(t *testing.T) {
		t.Parallel()

		// A prefix ending in a character that is fine mid-identifier could still
		// produce a table name that is not one. Identifiers covers the index
		// names too, which are the longest ones in the schema.
		test.SliceNotEmpty(t, schema.Identifiers("a"))
		test.ErrorIs(t, ValidatePrefix("a b"), dialect.ErrInvalidIdentifier)
	})
}

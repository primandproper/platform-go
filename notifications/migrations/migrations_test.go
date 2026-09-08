package migrations

import (
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// allDialects is every dialect this package renders DDL for.
var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// testPrefix is the namespace these tests render against. A non-empty one, so a
// prefix that failed to reach a statement shows up as a name without it.
const testPrefix = "ntf"

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders every supported dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, testPrefix)
			must.NoError(t, err, must.Sprintf("dialect %s", d))
			must.SliceNotEmpty(t, stmts)

			// Each table before the indexes that reference it — a CREATE INDEX
			// against a table that does not exist yet is a migration that fails on
			// its first run and every run after it.
			inboxTable := slices.IndexFunc(stmts, func(s string) bool {
				return strings.Contains(s, "CREATE TABLE") && strings.Contains(s, testPrefix+"_notifications_inbox")
			})
			devicesTable := slices.IndexFunc(stmts, func(s string) bool {
				return strings.Contains(s, "CREATE TABLE") && strings.Contains(s, testPrefix+"_notifications_devices")
			})

			must.GreaterEq(t, 0, inboxTable, must.Sprintf("dialect %s", d))
			must.GreaterEq(t, 0, devicesTable, must.Sprintf("dialect %s", d))

			for i, stmt := range stmts {
				if strings.Contains(stmt, "INDEX") && strings.Contains(stmt, testPrefix+"_notifications_inbox") {
					test.Greater(t, inboxTable, i, test.Sprintf("dialect %s", d))
				}
				if strings.Contains(stmt, "CREATE INDEX") && strings.Contains(stmt, testPrefix+"_notifications_devices") {
					test.Greater(t, devicesTable, i, test.Sprintf("dialect %s", d))
				}

				test.StrNotContains(t, stmt, ddl.Placeholder, test.Sprintf("dialect %s", d))

				// Comments are stripped, which matters: goose splits a migration on
				// semicolons and would tear a '--' comment containing one in half.
				test.StrNotContains(t, stmt, "--", test.Sprintf("dialect %s", d))
			}
		}
	})

	T.Run("scopes both tables with no default", func(t *testing.T) {
		t.Parallel()

		// The empty string is tenancy.Global(), so a DEFAULT here would hand the
		// global scope to a write that forgot the column — the mistake
		// tenancy.Scope exists to make unspellable in Go.
		for _, d := range allDialects {
			stmts, err := Statements(d, testPrefix)
			must.NoError(t, err)

			var tables int
			for _, stmt := range stmts {
				if !strings.Contains(stmt, "CREATE TABLE") {
					continue
				}

				tables++

				scope := scopeColumn(t, stmt)
				test.StrContains(t, scope, "NOT NULL", test.Sprintf("dialect %s", d))
				test.StrNotContains(t, scope, "DEFAULT", test.Sprintf("dialect %s", d))
			}

			test.EqOp(t, 2, tables, test.Sprintf("dialect %s", d))
		}
	})

	T.Run("keys a device on its platform and token", func(t *testing.T) {
		t.Parallel()

		// The uniqueness that makes a re-registration a move rather than a
		// fan-out, and the conflict target the registration upsert names. If it
		// stops being unique on exactly this pair, a handset that changes hands
		// keeps delivering the previous owner's notifications.
		for _, d := range allDialects {
			stmts, err := Statements(d, testPrefix)
			must.NoError(t, err)

			var found bool
			for _, stmt := range stmts {
				if !strings.Contains(stmt, "UNIQUE") || !strings.Contains(stmt, "notifications_devices") {
					continue
				}

				found = true

				test.StrContains(t, stmt, "(platform, token)", test.Sprintf("dialect %s", d))
			}

			test.True(t, found, test.Sprintf("dialect %s", d))
		}
	})
}

// scopeColumn returns the line of a CREATE TABLE that declares the scope column.
func scopeColumn(t *testing.T, create string) string {
	t.Helper()

	for line := range strings.SplitSeq(create, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "scope ") {
			return trimmed
		}
	}

	t.Fatalf("no scope column in %q", create)

	return ""
}

func TestTables(T *testing.T) {
	T.Parallel()

	T.Run("names both tables at the prefix", func(t *testing.T) {
		t.Parallel()

		tables, err := Tables(testPrefix)
		must.NoError(t, err)

		test.Eq(t, []string{
			testPrefix + "_notifications_devices",
			testPrefix + "_notifications_inbox",
		}, tables)
	})

	T.Run("refuses a prefix that would not render an identifier", func(t *testing.T) {
		t.Parallel()

		_, err := Tables("no-hyphens-allowed")
		test.Error(t, err)
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("renders one body per dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			body, err := SQL(d, testPrefix)
			must.NoError(t, err, must.Sprintf("dialect %s", d))

			test.StrContains(t, body, testPrefix+"_notifications_inbox", test.Sprintf("dialect %s", d))
			test.StrContains(t, body, testPrefix+"_notifications_devices", test.Sprintf("dialect %s", d))
			test.StrNotContains(t, body, ddl.Placeholder, test.Sprintf("dialect %s", d))
		}
	})

	T.Run("refuses a dialect it does not render", func(t *testing.T) {
		t.Parallel()

		_, err := SQL(dialect.Dialect("cassandra"), testPrefix)
		test.Error(t, err)
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts the empty prefix", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, ValidatePrefix(""))
	})

	T.Run("refuses one that would not render an identifier", func(t *testing.T) {
		t.Parallel()

		test.Error(t, ValidatePrefix("no-hyphens-allowed"))
	})
}

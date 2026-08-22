package migrations

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func allDialects() []dialect.Dialect {
	return []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}
}

// tables is every table this schema creates. A deployment with three of the four
// has an authorization server that fails at whichever step the missing one
// serves, so they are asserted together.
var tables = []string{
	"oauth2_clients",
	"oauth2_authorization_codes",
	"oauth2_access_tokens",
	"oauth2_refresh_tokens",
}

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)
			must.SliceNotEmpty(t, stmts, must.Sprintf("dialect %q", d))
		}
	})

	T.Run("rejects a dialect it has no schema for", func(t *testing.T) {
		t.Parallel()

		_, err := Statements(dialect.Dialect("oracle"), "")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("creates all four tables in every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			for _, table := range tables {
				test.StrContains(t, joined, table, test.Sprintf("dialect %q is missing %q", d, table))
			}
		}
	})

	T.Run("substitutes the prefix everywhere", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "custom")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			for _, table := range tables {
				test.StrContains(t, joined, "custom_"+table, test.Sprintf("dialect %q", d))
			}

			// A placeholder left behind is a table named "{{PREFIX}}oauth2_..."
			// on an engine that accepts it and a syntax error on one that does
			// not.
			test.StrNotContains(t, joined, ddl.Placeholder, test.Sprintf("dialect %q", d))
		}
	})

	// An empty namespace is the ordinary case, not a missing value: it renders
	// the component's own names, which is what a consumer with one application
	// per database wants.
	T.Run("an empty prefix renders the schema's own names", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "oauth2_clients", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, "_oauth2_clients", test.Sprintf("dialect %q", d))
		}
	})

	// An index cannot be created before the table it indexes.
	T.Run("creates each table before its indexes", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			for _, table := range tables {
				joined := strings.Join(stmts, "\n")

				tableAt := strings.Index(joined, "CREATE TABLE IF NOT EXISTS "+table)
				indexAt := strings.Index(joined, "ON "+table)

				test.True(t, tableAt >= 0, test.Sprintf("dialect %q, table %q", d, table))
				test.True(t, indexAt > tableAt, test.Sprintf("dialect %q, table %q", d, table))
			}
		}
	})

	// MySQL has no CREATE INDEX IF NOT EXISTS, so its indexes are declared
	// inline and there are exactly four statements to run.
	T.Run("mysql declares its indexes inline", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.MySQL, "")
		must.NoError(t, err)
		test.SliceLen(t, len(tables), stmts)

		joined := strings.Join(stmts, "\n")
		test.StrContains(t, joined, "KEY oauth2_access_tokens_family_id_idx")
	})

	// The family index is what a detected token reuse depends on: without it,
	// revoking a family scans every token this server has ever issued, at the
	// one moment where being slow is being unavailable.
	T.Run("every dialect indexes family_id on both token tables", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "oauth2_access_tokens_family_id_idx", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "oauth2_refresh_tokens_family_id_idx", test.Sprintf("dialect %q", d))
		}
	})

	// A record written against one engine has to be readable against another,
	// and the nullable columns in particular are what every expiry and
	// one-time-use predicate keys on.
	T.Run("every dialect declares the same columns", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			for _, column := range []string{
				"secret_hash", "redirect_uris", "token_endpoint_auth_method",
				"code_challenge", "subject_id", "subject_claims", "audience",
				"family_id", "issued_at", "expires_at", "redeemed_at", "revoked_at",
			} {
				test.StrContains(t, joined, column, test.Sprintf("dialect %q is missing %q", d, column))
			}
		}
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts an empty prefix and a plain identifier", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, ValidatePrefix(""))
		test.NoError(t, ValidatePrefix("ddb"))
	})

	T.Run("refuses a prefix carrying its own separator", func(t *testing.T) {
		t.Parallel()

		// database/ddl supplies the separator, so this would render a double
		// underscore.
		test.Error(t, ValidatePrefix("ddb_"))
	})

	T.Run("refuses a prefix that pushes an index name past the limit", func(t *testing.T) {
		t.Parallel()

		// The longest identifier here is 49 bytes before a prefix, so there is
		// noticeably less room than in most schemas in this module — a prefix
		// that works elsewhere can fail here, which is exactly what this check
		// exists to catch before a migration half runs.
		test.Error(t, ValidatePrefix(strings.Repeat("x", 32)))
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("joins the statements into one migration body", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			body, err := SQL(d, "")
			must.NoError(t, err)

			test.StrHasSuffix(t, ";\n", body)

			// Comments are already stripped, which matters: goose splits a
			// migration on semicolons, and a '--' comment containing one would
			// be torn in half.
			test.StrNotContains(t, body, "--", test.Sprintf("dialect %q", d))
		}
	})
}

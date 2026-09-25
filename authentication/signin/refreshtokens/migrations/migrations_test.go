package migrations

import (
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func allDialects() []dialect.Dialect {
	return []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}
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

	T.Run("substitutes the prefix everywhere", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "custom")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "custom_signin_refresh_tokens", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_signin_refresh_tokens_family_idx", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_signin_refresh_tokens_subject_idx", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_signin_refresh_tokens_purge_after_idx", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, ddl.Placeholder, test.Sprintf("dialect %q", d))
		}
	})

	// An empty namespace is the ordinary case, not a missing value: it renders
	// the component's own name, which is what a consumer with one application
	// per database wants.
	T.Run("an empty prefix renders the schema's own names", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "signin_refresh_tokens", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, "_signin_refresh_tokens", test.Sprintf("dialect %q", d))
		}
	})

	// The four identifier columns, which are four different questions — see
	// postgres.sql — and the one column that is not an identifier. Both of the
	// last two are easy to leave out and load-bearing: without active_account_id
	// an exchange hands back a token for whatever the user's default account has
	// since become, and without administrative it hands an administrative
	// session an ordinary token on an ordinary lifetime.
	T.Run("carries the four identifier columns and the door", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			for _, column := range []string{"scope", "family_id", "subject_id", "active_account_id", "administrative"} {
				test.StrContains(t, joined, column, test.Sprintf("dialect %q column %q", d, column))
			}
		}
	})

	// The scope column carries no default, which is the one place this schema
	// departs from the module's habit of defaulting a text column to the empty
	// string. The empty string is tenancy.Global(), not the absence of a scope,
	// so a default would hand the global scope to a write that forgot the
	// column.
	T.Run("gives the scope column no default", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			scopeAt := strings.Index(joined, "scope")
			must.True(t, scopeAt >= 0, must.Sprintf("dialect %q", d))

			line := joined[scopeAt : strings.Index(joined[scopeAt:], "\n")+scopeAt]
			test.StrNotContains(t, line, "DEFAULT", test.Sprintf("dialect %q", d))
		}
	})

	// A refresh token is minted, exchanged once, and collected. archived_at
	// would keep rows the sweep could not reach, and last_updated_at would be a
	// third copy of redeemed_at and revoked_at — the row's only mutations.
	//
	// There is no id either: the key is the digest of the token, because the
	// only way to name one of these rows is to hold the credential it was
	// minted from.
	T.Run("carries no convention triple and no surrogate id", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrNotContains(t, joined, "archived_at", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, "last_updated_at", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "hash", test.Sprintf("dialect %q", d))
		}
	})

	// Three composite indexes rather than three partial ones. MySQL has none,
	// and a third spelling of one index across three dialect files is the drift
	// links/database/migrations already declined to carry.
	//
	// The two revocations lead with the scope so that one tenant's revocation
	// does not walk every other tenant's rows.
	//
	// Only the statements that declare an index are read for a WHERE: version 3's
	// backfill has one of its own, and it is not an index predicate.
	T.Run("leads the revocation indexes with the scope", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "(scope, family_id)", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "(scope, subject_id)", test.Sprintf("dialect %q", d))

			for _, stmt := range stmts {
				if strings.Contains(stmt, "INDEX") || strings.Contains(stmt, "KEY ") {
					test.StrNotContains(t, stmt, "WHERE", test.Sprintf("dialect %q", d))
				}
			}
		}
	})

	// An index cannot be created before the table it indexes.
	T.Run("creates the table before its indexes", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			test.StrContains(t, stmts[0], "CREATE TABLE", test.Sprintf("dialect %q", d))

			indexes := 0

			for _, stmt := range stmts {
				if strings.HasPrefix(stmt, "CREATE INDEX") {
					indexes++
				}
			}

			// Version 1's three, and SQLite's version 3 recreating them after
			// its rebuild.
			want := 3
			if d == dialect.SQLite {
				want = 6
			}

			test.EqOp(t, want, indexes, test.Sprintf("dialect %q", d))
		}
	})

	// MySQL has no CREATE INDEX IF NOT EXISTS, so its indexes are declared
	// inside the table. One CREATE TABLE holding all three is the right answer
	// there, and a standalone index would be a migration that fails on its
	// second run.
	T.Run("mysql declares its indexes inline", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.MySQL, "")
		must.NoError(t, err)

		test.StrContains(t, stmts[0], "KEY signin_refresh_tokens_family_idx")
		test.StrContains(t, stmts[0], "KEY signin_refresh_tokens_subject_idx")
		test.StrContains(t, stmts[0], "KEY signin_refresh_tokens_purge_after_idx")

		for _, stmt := range stmts {
			test.False(t, strings.HasPrefix(stmt, "CREATE INDEX"), test.Sprintf("standalone index %q", stmt))
		}
	})

	// The columns every later version added are part of a fresh install, which
	// is the whole sequence rendered from the start rather than version 1 alone.
	T.Run("a fresh install carries every version's columns", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			for _, column := range []string{"redeemed_with_key", "successor_hash", "signed_in_at"} {
				test.StrContains(t, joined, column, test.Sprintf("dialect %q column %q", d, column))
			}
		}
	})
}

func TestSequence(T *testing.T) {
	T.Parallel()

	// Pinned here as well as refused at render time, so a malformed sequence
	// fails this package's build rather than a consumer's migration run.
	T.Run("validates", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, sequence.Validate())
	})

	T.Run("reports the latest version", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, uint64(3), Latest())
	})

	// Every version carries every dialect. A version missing one would render
	// as ErrUnsupported only once a consumer on that dialect reached it.
	T.Run("renders every version in every dialect", func(t *testing.T) {
		t.Parallel()

		for i := range sequence {
			for _, d := range allDialects() {
				stmts, err := sequence[i].Schema.Statements(d, "")
				must.NoError(t, err, must.Sprintf("version %d dialect %q", sequence[i].Version, d))
				test.SliceNotEmpty(t, stmts, test.Sprintf("version %d dialect %q", sequence[i].Version, d))
			}
		}
	})

	// A fresh install is the versions in order, with nothing dropped or added
	// between them.
	T.Run("renders a fresh install as every version in order", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			var want []string

			for i := range sequence {
				stmts, err := sequence[i].Schema.Statements(d, "ddb")
				must.NoError(t, err)

				want = append(want, stmts...)
			}

			got, err := Statements(d, "ddb")
			must.NoError(t, err)
			test.Eq(t, want, got, test.Sprintf("dialect %q", d))
		}
	})
}

func TestStatementsSince(T *testing.T) {
	T.Parallel()

	// A database created from v14.0.0 has version 1's table: it owes the two
	// changes after it and not the CREATE TABLE it already ran.
	T.Run("renders only what a database at version 1 owes", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := StatementsSince(d, "", 1)
			must.NoError(t, err)

			var want []string

			for _, m := range sequence[1:] {
				versionStmts, versionErr := m.Schema.Statements(d, "")
				must.NoError(t, versionErr)

				want = append(want, versionStmts...)
			}

			test.Eq(t, want, stmts, test.Sprintf("dialect %q", d))

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "redeemed_with_key", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "signed_in_at", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "MIN(", test.Sprintf("dialect %q backfills", d))

			// SQLite's version 3 does create the table, as the rebuild;
			// Postgres and MySQL owe nothing but ALTERs and the backfill.
			if d != dialect.SQLite {
				test.StrNotContains(t, joined, "CREATE TABLE", test.Sprintf("dialect %q", d))
			}
		}
	})

	// A database created from v14.1.0 already has the idempotency columns.
	T.Run("renders only what a database at version 2 owes", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := StatementsSince(d, "", 2)
			must.NoError(t, err)

			want, versionErr := sequence[2].Schema.Statements(d, "")
			must.NoError(t, versionErr)
			test.Eq(t, want, stmts, test.Sprintf("dialect %q", d))

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "signed_in_at", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, "ADD COLUMN redeemed_with_key", test.Sprintf("dialect %q", d))
		}
	})

	T.Run("owes nothing at the latest version", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := StatementsSince(d, "", Latest())
			must.NoError(t, err)
			test.SliceEmpty(t, stmts, test.Sprintf("dialect %q", d))

			body, sqlErr := SQLSince(d, "", Latest())
			must.NoError(t, sqlErr)
			test.EqOp(t, "", body, test.Sprintf("dialect %q", d))
		}
	})

	// A database past Latest was migrated by a newer release than this one, and
	// an empty answer would let this one go on writing a table it does not know.
	T.Run("refuses a version past the latest", func(t *testing.T) {
		t.Parallel()

		_, err := StatementsSince(dialect.Postgres, "", Latest()+1)
		test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)

		_, err = SQLSince(dialect.Postgres, "", Latest()+1)
		test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)
	})

	// Owing nothing is not a reason to accept a configuration that would fail
	// the next time this package ships a version.
	T.Run("vets the dialect and prefix even when nothing is owed", func(t *testing.T) {
		t.Parallel()

		_, err := StatementsSince(dialect.Dialect("oracle"), "", Latest())
		test.ErrorIs(t, err, dialect.ErrUnsupported)

		_, err = SQLSince(dialect.Postgres, "ddb_", Latest())
		test.ErrorIs(t, err, ddl.ErrPrefixTrailingSeparator)
	})

	T.Run("substitutes the prefix", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := StatementsSince(d, "custom", 1)
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "custom_signin_refresh_tokens", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, ddl.Placeholder, test.Sprintf("dialect %q", d))
		}
	})
}

func TestSQLiteRebuild(T *testing.T) {
	T.Parallel()

	// The rename carries the old table's indexes with it, names and all, so a
	// CREATE INDEX IF NOT EXISTS run before the DROP finds each name taken and
	// creates nothing. The order is what the upgrade suite proves against a real
	// engine; this is what names it when it breaks.
	T.Run("recreates the indexes only after dropping the table they moved with", func(t *testing.T) {
		t.Parallel()

		stmts, err := sequence[2].Schema.Statements(dialect.SQLite, "")
		must.NoError(t, err)

		at := func(prefix string) int {
			for i, stmt := range stmts {
				if strings.HasPrefix(stmt, prefix) {
					return i
				}
			}

			t.Fatalf("no statement begins %q", prefix)

			return -1
		}

		rename, create, insert, drop := at("ALTER TABLE"), at("CREATE TABLE"), at("INSERT INTO"), at("DROP TABLE")

		test.True(t, rename < create && create < insert && insert < drop,
			test.Sprintf("rename %d, create %d, insert %d, drop %d", rename, create, insert, drop))

		for i, stmt := range stmts {
			if strings.HasPrefix(stmt, "CREATE INDEX") {
				test.True(t, i > drop, test.Sprintf("index at %d precedes the drop at %d: %s", i, drop, stmt))
			}
		}
	})

	// The table set is read off CREATE TABLE, and the rebuild creates only the
	// table that remains; the name it renames the old one to is never created.
	T.Run("reports no table for the name the old one is renamed to", func(t *testing.T) {
		t.Parallel()

		tables, err := Tables("")
		must.NoError(t, err)
		test.Eq(t, []string{"signin_refresh_tokens"}, tables)

		// It is still a name the DDL renders, so the prefix is vetted against it.
		test.SliceContains(t, sequence.Identifiers(""), "signin_refresh_tokens_rebuild")
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("renders the same DDL as one body", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			body, err := SQL(d, "ddb")
			must.NoError(t, err)

			stmts, stmtErr := Statements(d, "ddb")
			must.NoError(t, stmtErr)

			for _, stmt := range stmts {
				test.StrContains(t, body, strings.TrimSuffix(stmt, ";"), test.Sprintf("dialect %q", d))
			}
		}
	})

	T.Run("rejects a dialect it has no schema for", func(t *testing.T) {
		t.Parallel()

		_, err := SQL(dialect.Dialect("oracle"), "")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}

func TestTables(T *testing.T) {
	T.Parallel()

	T.Run("names the table this package creates", func(t *testing.T) {
		t.Parallel()

		tables, err := Tables("")
		must.NoError(t, err)
		test.Eq(t, []string{"signin_refresh_tokens"}, tables)

		tables, err = Tables("ddb")
		must.NoError(t, err)
		test.Eq(t, []string{"ddb_signin_refresh_tokens"}, tables)
	})

	T.Run("rejects a prefix the schema cannot render", func(t *testing.T) {
		t.Parallel()

		tables, err := Tables("ddb_")
		test.Nil(t, tables)
		test.ErrorIs(t, err, ddl.ErrPrefixTrailingSeparator)
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts a namespace the schema can render", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"", "ddb", "app_two"} {
			test.NoError(t, ValidatePrefix(prefix), test.Sprintf("prefix %q", prefix))
		}
	})

	T.Run("rejects a namespace that is not an identifier fragment", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"ddb-1", "a b", "signin_; DROP TABLE users;--"} {
			test.Error(t, ValidatePrefix(prefix), test.Sprintf("prefix %q", prefix))
		}
	})

	// The sweeper's index name is the longest identifier this schema renders, so
	// it is the one a long prefix pushes over the limit. Catching it here turns
	// a migration that half ran into a config that would not load.
	T.Run("rejects a namespace that renders an over-long identifier", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix(strings.Repeat("a", ddl.MaxIdentifierLength)), ddl.ErrPrefixTooLong)
	})

	T.Run("rejects a namespace that ends in the separator", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidatePrefix("ddb_"), ddl.ErrPrefixTrailingSeparator)
	})
}

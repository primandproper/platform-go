package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// allDialects is what every rendering assertion runs against: a schema that is
// right on two of three is the failure mode this package exists to prevent.
var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// tableName is the one table this schema creates, unprefixed.
//
// It is spelled here rather than read from internal/queries because that
// package's test reads it from *this* one: the two are cross-checked against
// each other, and a check where each side derives from the other checks nothing.
const tableName = "oauth2_registered_clients"

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("creates the table in every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, "")
			must.NoError(t, err)
			must.SliceNotEmpty(t, stmts)

			test.StrContains(t, strings.Join(stmts, "\n"), "CREATE TABLE IF NOT EXISTS "+tableName+" ",
				test.Sprintf("%s does not create %s", d, tableName))
		}
	})

	T.Run("renders at the prefix", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, "ddb")
			must.NoError(t, err)

			test.StrContains(t, strings.Join(stmts, "\n"), "ddb_"+tableName,
				test.Sprintf("%s does not create ddb_%s", d, tableName))
		}
	})

	T.Run("refuses an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := Statements(dialect.Dialect("oracle"), "")
		must.Error(t, err)
	})

	T.Run("refuses a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		// These names are interpolated into statement text rather than bound, so
		// vetting is the whole defense.
		for _, prefix := range []string{"has space", "trailing_", strings.Repeat("x", 200)} {
			test.Error(t, ValidatePrefix(prefix), test.Sprintf("prefix %q", prefix))
		}
	})
}

func TestTables(T *testing.T) {
	T.Parallel()

	T.Run("is every table the schema creates", func(t *testing.T) {
		t.Parallel()

		names, err := Tables("")
		must.NoError(t, err)
		test.Eq(t, []string{tableName}, names)
	})

	T.Run("names what the DDL creates, at the same prefix", func(t *testing.T) {
		t.Parallel()

		// The list a consumer truncates by has to agree with the statements that
		// created the tables, and agreeing at the empty prefix is not the same as
		// agreeing at theirs.
		for _, d := range allDialects {
			stmts, err := Statements(d, "ddb")
			must.NoError(t, err)

			names, err := Tables("ddb")
			must.NoError(t, err)

			for _, name := range names {
				test.StrContains(t, strings.Join(stmts, "\n"), "CREATE TABLE IF NOT EXISTS "+name+" ",
					test.Sprintf("%s does not create %s", d, name))
			}
		}
	})

	T.Run("rejects a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		_, err := Tables("has space")
		test.Error(t, err)
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("is the statements joined back together", func(t *testing.T) {
		t.Parallel()

		// It is what a consumer hands database/migrate's
		// WithGeneratedMigration, so it has to carry everything Statements does.
		for _, d := range allDialects {
			body, err := SQL(d, "")
			must.NoError(t, err)

			stmts, err := Statements(d, "")
			must.NoError(t, err)

			for _, stmt := range stmts {
				test.StrContains(t, body, strings.TrimSuffix(strings.TrimSpace(stmt), ";"),
					test.Sprintf("%s's migration body is missing a statement", d))
			}
		}
	})

	T.Run("refuses a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		_, err := SQL(dialect.Postgres, "has space")
		test.Error(t, err)
	})
}

// TestSchema_ScopeColumnHasNoDefault is the tenancy obligation in the schema.
//
// The empty string is tenancy.Global(), not the absence of a scope. A column
// that supplied it for a write which did not name one would hand out the global
// registry to whoever forgot the column, which is the mistake tenancy.Scope
// exists to make unspellable in Go.
func TestSchema_ScopeColumnHasNoDefault(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			line := columnLine(t, d, "scope")

			test.StrContains(t, line, "NOT NULL")
			test.StrNotContains(t, line, "DEFAULT")
		})
	}
}

// TestSchema_OwnerColumnHasNoDefault is the sharper version of the same reason,
// and the one this schema turns on.
//
// An empty belongs_to_user is the *more* privileged arrangement: it is the
// administered registration, which oauth2clients.Client.Admits reads as "any
// subject in this registry may authorize through it". So a DEFAULT ” here would
// file every write that forgot the column as administered — a credential
// nobody granted, created by omission.
func TestSchema_OwnerColumnHasNoDefault(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			line := columnLine(t, d, "belongs_to_user")

			test.StrContains(t, line, "NOT NULL")
			test.StrNotContains(t, line, "DEFAULT")

			// And no REFERENCES, exactly as password_reset_tokens carries none:
			// adopting this registry does not mean adopting identity.
			test.StrNotContains(t, line, "REFERENCES")
		})
	}
}

// TestSchema_CreatedAtDefaults is why the store never supplies the column and
// reads it back inside the transaction that wrote the row.
func TestSchema_CreatedAtDefaults(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			line := columnLine(t, d, "created_at")

			test.StrContains(t, line, "NOT NULL")
			test.StrContains(t, line, "DEFAULT")
		})
	}
}

// TestSchema_TheTwoRevisionTimesAreNullable is what lets a registration nobody
// has touched report no update rather than an update in 1970.
func TestSchema_TheTwoRevisionTimesAreNullable(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, column := range []string{"last_updated_at", "archived_at"} {
				line := columnLine(t, d, column)

				test.StrNotContains(t, line, "NOT NULL",
					test.Sprintf("%s must be nullable", column))
				test.StrNotContains(t, line, "DEFAULT",
					test.Sprintf("%s must not default", column))
			}
		})
	}
}

// TestSchema_ClientIDIsGloballyUniqueAndNotPartial is the index the
// authorization server's lookup rests on, and both halves of its shape are
// load-bearing.
//
// Globally unique rather than unique per registry, because the lookup that reads
// this column has no scope to pass — it is what resolves one — and an identifier
// taken twice in two registries would make that read ambiguous.
//
// Not partial, because the lookup has to find an archived registration in order
// to refuse it by name, and because re-issuing a retired client_id would
// re-attribute every token ever minted under it.
func TestSchema_ClientIDIsGloballyUniqueAndNotPartial(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			stmts, err := Statements(d, "")
			must.NoError(t, err)

			seen := false

			for _, stmt := range stmts {
				if !strings.Contains(stmt, tableName+"_client_id_uniq") {
					continue
				}

				seen = true

				// MySQL declares it inline on the table, where a WHERE could not
				// appear and the surrounding statement names every other column.
				// The other two declare it as an index of its own, which is
				// where partiality and a second key column would be spelled.
				if !strings.HasPrefix(strings.TrimSpace(stmt), "CREATE UNIQUE INDEX") {
					continue
				}

				test.StrNotContains(t, stmt, "WHERE",
					test.Sprintf("%s made the client_id uniqueness partial", d))
				test.StrNotContains(t, stmt, "scope",
					test.Sprintf("%s scoped the client_id uniqueness", d))
			}

			test.True(t, seen, test.Sprintf("%s declares no uniqueness over client_id", d))
		})
	}
}

// TestSchema_BothPagesHaveAnIndex pins the two reads this table serves, which
// are two questions rather than one with an argument.
//
// The self-service page gets its own index rather than riding a prefix of the
// administered one, because that one orders by id immediately after the scope
// and this read walks the cursor across one owner's rows.
func TestSchema_BothPagesHaveAnIndex(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			for _, index := range []string{"_scope_idx", "_owner_idx"} {
				test.StrContains(t, joined, tableName+index,
					test.Sprintf("%s does not create %s%s", d, tableName, index))
			}
		})
	}
}

// TestSchemaFiles_MatchTheMigrations is the regeneration gate for the committed
// schema files unison's config names, living beside them: each must be exactly
// what the migrations render for its dialect, at the empty prefix. A hand-edit
// to one leaves sqlc analyzing DDL no database runs, which is the
// checked-versus-executed gap in its other direction.
func TestSchemaFiles_MatchTheMigrations(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			committed, err := os.ReadFile(filepath.Join("schema", string(d)+".sql"))
			must.NoError(t, err)

			rendered, err := SQL(d, "")
			must.NoError(t, err)

			test.EqOp(t, rendered+"\n", string(committed),
				test.Sprintf("run `make unison` and commit schema/%s.sql", d))
		})
	}
}

// columnLine returns the DDL line declaring the named column, matched on the
// whole first token rather than on a prefix — "scope" and "scopes" are two
// columns with different obligations, and a prefix match would let an assertion
// about one silently be answered by the other.
func columnLine(t *testing.T, d dialect.Dialect, column string) string {
	t.Helper()

	stmts, err := Statements(d, "")
	must.NoError(t, err)

	for _, stmt := range stmts {
		for line := range strings.SplitSeq(stmt, "\n") {
			trimmed := strings.TrimSpace(line)

			name, _, found := strings.Cut(trimmed, " ")
			if found && name == column {
				return trimmed
			}
		}
	}

	t.Fatalf("%s declares no column %q", d, column)

	return ""
}

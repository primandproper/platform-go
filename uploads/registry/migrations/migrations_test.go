package migrations

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// allDialects is what every rendering assertion runs against: a schema that is
// right on two of three is the failure mode this package exists to prevent.
var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// tableNames is every table this schema creates, unprefixed.
var tableNames = []string{"uploads_objects"}

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders every table in every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, "")
			must.NoError(t, err)
			must.SliceNotEmpty(t, stmts)

			joined := strings.Join(stmts, "\n")
			for _, table := range tableNames {
				test.StrContains(t, joined, table, test.Sprintf("%s is missing %s", d, table))
			}

			for _, stmt := range stmts {
				test.False(t, strings.Contains(stmt, "{{"),
					test.Sprintf("%s left a placeholder: %s", d, stmt))
			}
		}
	})

	T.Run("applies the prefix to every identifier", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, "ddb")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")
			for _, table := range tableNames {
				test.StrContains(t, joined, "ddb_"+table)
			}

			// An unprefixed name left behind would create a table in the shared
			// namespace this prefix exists to avoid.
			for _, table := range tableNames {
				test.False(t, strings.Contains(joined, " "+table+" "),
					test.Sprintf("%s left %s unprefixed", d, table))
			}
		}
	})

	T.Run("rejects a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		// The prefix is vetted against every identifier it renders, not against
		// a pattern, so one that is legal alone and produces an illegal index
		// name fails here rather than at a consumer's first migration.
		must.Error(t, ValidatePrefix("has space"))
		must.Error(t, ValidatePrefix("trailing_"))
		must.Error(t, ValidatePrefix(strings.Repeat("x", 200)))

		must.NoError(t, ValidatePrefix(""))
		must.NoError(t, ValidatePrefix("ddb"))

		for _, d := range allDialects {
			_, err := Statements(d, "has space")
			must.Error(t, err)
		}
	})

	T.Run("SQL is the statements rejoined", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, "ddb")
			must.NoError(t, err)

			body, err := SQL(d, "ddb")
			must.NoError(t, err)

			for _, stmt := range stmts {
				test.StrContains(t, body, strings.TrimSpace(stmt))
			}
		}
	})

	T.Run("refuses an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := Statements(dialect.Dialect("oracle"), "")
		must.Error(t, err)
	})
}

func TestTables(T *testing.T) {
	T.Parallel()

	T.Run("is every table the schema creates", func(t *testing.T) {
		t.Parallel()

		// Against the same hand-written list every other assertion in this file
		// runs against, sorted: complete is the property this list is exported
		// for, so it is pinned rather than derived from the thing it reads.
		want := slices.Clone(tableNames)
		slices.Sort(want)

		names, err := Tables("")
		must.NoError(t, err)
		test.Eq(t, want, names)
	})

	T.Run("renders at the prefix", func(t *testing.T) {
		t.Parallel()

		names, err := Tables("ddb")
		must.NoError(t, err)
		must.SliceLen(t, len(tableNames), names)

		for _, name := range names {
			test.StrHasPrefix(t, "ddb_uploads_", name)
		}
	})

	T.Run("names what the DDL creates, at the same prefix", func(t *testing.T) {
		t.Parallel()

		// The list a consumer truncates by has to agree with the statements
		// that created the tables, and agreeing at the empty prefix is not the
		// same as agreeing at theirs.
		for _, d := range allDialects {
			stmts, err := Statements(d, "ddb")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			names, err := Tables("ddb")
			must.NoError(t, err)

			for _, name := range names {
				test.StrContains(t, joined, "CREATE TABLE IF NOT EXISTS "+name+" ",
					test.Sprintf("%s does not create %s", d, name))
			}
		}
	})

	T.Run("rejects a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		// The names are interpolated into whatever the caller builds out of
		// them, so an unvetted prefix would leave this the one door into the
		// package that hands back an identifier nothing checked.
		for _, prefix := range []string{"has space", "trailing_", strings.Repeat("x", 200)} {
			_, err := Tables(prefix)
			test.Error(t, err, test.Sprintf("prefix %q", prefix))
		}
	})
}

func TestSchema_ScopeColumnHasNoDefault(T *testing.T) {
	T.Parallel()

	// The empty string is tenancy.Global(), not the absence of a scope. A
	// column that supplied it for a write which did not name one would hand the
	// global scope to whoever forgot the column — the mistake tenancy.Scope
	// exists to make unspellable in Go, enforced here for a writer that did not
	// come through SQLStore.
	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			stmts, err := Statements(d, "")
			must.NoError(t, err)

			for _, stmt := range stmts {
				for line := range strings.SplitSeq(stmt, "\n") {
					trimmed := strings.TrimSpace(line)
					if !strings.HasPrefix(trimmed, "scope ") {
						continue
					}

					test.StrContains(t, trimmed, "NOT NULL")
					test.StrNotContains(t, trimmed, "DEFAULT")
				}
			}
		})
	}
}

func TestSchema_ConventionTriple(T *testing.T) {
	T.Parallel()

	// Every table storing consumer rows carries created_at NOT NULL with a
	// dialect-appropriate default, last_updated_at, and archived_at. The
	// default is the half that fails at runtime rather than at generate: a
	// create that does not supply the column and a column with nothing to fall
	// back on is a not-null violation on the first write.
	defaults := map[dialect.Dialect]string{
		dialect.Postgres: "created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()",
		dialect.MySQL:    "created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)",
		dialect.SQLite:   "created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP",
	}

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, defaults[d])
			test.StrContains(t, joined, "last_updated_at")
			test.StrContains(t, joined, "archived_at")

			// updated_at has two spellings in this module's history and only
			// one of them gets an update window out of querygen.
			test.False(t, strings.Contains(joined, " updated_at"),
				test.Sprintf("%s spells the last-mutation stamp updated_at", d))
		})
	}
}

func TestSchema_UniquenessCoversArchivedRows(T *testing.T) {
	T.Parallel()

	// Archival here is metadata-only and the bytes stay in the bucket, so
	// freeing a key on archive would let a second row claim a key whose object
	// belongs to the first. The uniqueness is therefore unconditional in every
	// dialect — no partial clause on the two that have them.
	for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			stmts, err := Statements(d, "")
			must.NoError(t, err)

			var seen bool

			for _, stmt := range stmts {
				if !strings.Contains(stmt, "CREATE UNIQUE INDEX") {
					continue
				}

				seen = true

				test.StrNotContains(t, stmt, "WHERE",
					test.Sprintf("unique index is partial: %s", stmt))
			}

			test.True(t, seen, test.Sprintf("%s creates no unique index", d))
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

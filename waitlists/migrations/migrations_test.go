package migrations

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// allDialects is what every rendering assertion runs against: a schema that is
// right on two of three is the failure mode this package exists to prevent.
var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// tableNames is every table this schema creates, unprefixed.
var tableNames = []string{
	"waitlists",
	"waitlist_signups",
}

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
			test.StrHasPrefix(t, "ddb_waitlist", name)
		}
	})

	T.Run("names what the DDL creates, at the same prefix", func(t *testing.T) {
		t.Parallel()

		// The list a consumer truncates by has to agree with the statements that
		// created the tables, and agreeing at the empty prefix is not the same as
		// agreeing at theirs.
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

	// The empty string is tenancy.Global(), not the absence of a scope. A column
	// that supplied it for a write which did not name one would hand the global
	// scope to whoever forgot the column — the mistake tenancy.Scope exists to
	// make unspellable in Go, enforced here for a writer that did not come
	// through SQLStore.
	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			forEachColumn(t, d, "scope ", func(line string) {
				test.StrContains(t, line, "NOT NULL")
				test.StrNotContains(t, line, "DEFAULT")
			})
		})
	}
}

func TestSchema_ClosesAtIsNotNullable(T *testing.T) {
	T.Parallel()

	// The one shape in this schema worth arguing about, pinned so that the
	// argument has to be had again to change it. A nullable closing time turns
	// the read this package exists to serve into a disjunction over the column
	// it pages by; a list that never closes is archived instead. See the
	// package documentation.
	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			var seen bool

			forEachColumn(t, d, "closes_at ", func(line string) {
				seen = true

				test.StrContains(t, line, "NOT NULL")
				test.StrNotContains(t, line, "DEFAULT")
			})

			test.True(t, seen, test.Sprintf("%s has no closes_at column", d))
		})
	}
}

func TestSchema_ContactColumnsCarryTheWithdrawal(T *testing.T) {
	T.Parallel()

	// The pair the whole package is shaped around. contact defaults to the empty
	// string because that is what a withdrawal leaves behind; contact_digest has
	// no default at all, because a row without one is a signup nothing can ever
	// suppress.
	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			var seenContact, seenDigest bool

			forEachColumn(t, d, "contact ", func(line string) {
				seenContact = true

				test.StrContains(t, line, "NOT NULL")
				test.StrContains(t, line, "DEFAULT ''")
			})

			forEachColumn(t, d, "contact_digest ", func(line string) {
				seenDigest = true

				test.StrContains(t, line, "NOT NULL")
				test.StrNotContains(t, line, "DEFAULT")
			})

			test.True(t, seenContact, test.Sprintf("%s has no contact column", d))
			test.True(t, seenDigest, test.Sprintf("%s has no contact_digest column", d))
		})
	}
}

func TestSchema_ContactUniquenessCoversWithdrawnRows(T *testing.T) {
	T.Parallel()

	// The suppression's storage half. A partial index would free the key the
	// moment somebody withdrew, and the next form submission would insert a
	// second row — the unsubscribe failing silently. So the constraint is
	// unconditional in every dialect: no partial clause on the two that have
	// them, and one unique index in total on each.
	for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			stmts, err := Statements(d, "")
			must.NoError(t, err)

			var unique int

			for _, stmt := range stmts {
				if !strings.Contains(stmt, "CREATE UNIQUE INDEX") {
					continue
				}

				unique++

				test.StrContains(t, stmt, "contact_digest")
				test.StrNotContains(t, stmt, "WHERE",
					test.Sprintf("unique index is partial: %s", stmt))
			}

			test.EqOp(t, 1, unique, test.Sprintf("%s", d))
		})
	}

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		// MySQL carries the same constraint as a table-level UNIQUE KEY, since
		// it has no partial index to have left off in the first place.
		stmts, err := Statements(dialect.MySQL, "")
		must.NoError(t, err)

		joined := strings.Join(stmts, "\n")
		test.StrContains(t, joined, "UNIQUE KEY waitlist_signups_contact_uniq (scope, waitlist_id, contact_digest)")
	})
}

func TestSchema_SignupsCascade(T *testing.T) {
	T.Parallel()

	// The signups reference the list they hang off with ON DELETE CASCADE. A
	// deletion that left them behind would leave rows referring to a list that
	// no longer says when it closed or what it was for.
	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")
			test.EqOp(t, 1, strings.Count(joined, "ON DELETE CASCADE"))
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

// forEachColumn hands visit every DDL line declaring a column whose name starts
// the line, so an assertion about one column's nullability is written once
// rather than once per dialect's spelling of its type.
func forEachColumn(t *testing.T, d dialect.Dialect, prefix string, visit func(line string)) {
	t.Helper()

	stmts, err := Statements(d, "")
	must.NoError(t, err)

	for _, stmt := range stmts {
		for line := range strings.SplitSeq(stmt, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, prefix) {
				visit(trimmed)
			}
		}
	}
}

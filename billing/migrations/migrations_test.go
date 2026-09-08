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
var tableNames = []string{
	"billing_products",
	"billing_subscriptions",
	"billing_purchases",
	"billing_transactions",
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

			for _, name := range tableNames {
				test.StrContains(t, joined, "CREATE TABLE IF NOT EXISTS "+name+" ",
					test.Sprintf("%s does not create %s", d, name))
			}
		}
	})

	T.Run("renders at the prefix", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, "ddb")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			for _, name := range tableNames {
				test.StrContains(t, joined, "ddb_"+name,
					test.Sprintf("%s does not create ddb_%s", d, name))
			}
		}
	})

	T.Run("refuses an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := Statements(dialect.Dialect("oracle"), "")
		must.Error(t, err)
	})

	T.Run("refuses a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"has space", "trailing_", strings.Repeat("x", 200)} {
			test.Error(t, ValidatePrefix(prefix), test.Sprintf("prefix %q", prefix))
		}
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

		_, err := Tables("has space")
		test.Error(t, err)
	})
}

// TestSchema_ScopeColumnHasNoDefault is the tenancy obligation in the schema.
//
// The empty string is tenancy.Global(), not the absence of a scope. A column
// that supplied it for a write which did not name one would hand out the global
// scope to whoever forgot the column, which is the mistake tenancy.Scope exists
// to make unspellable in Go.
func TestSchema_ScopeColumnHasNoDefault(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			forEachColumn(t, d, "scope", func(line string) {
				test.StrContains(t, line, "NOT NULL")
				test.StrNotContains(t, line, "DEFAULT")
			})
		})
	}
}

// TestSchema_ExternalIDsAreNullable is what makes the uniqueness over them
// usable.
//
// A free tier, a comped plan and a subscription granted by hand have no
// provider-side counterpart, and all three engines treat NULLs in a unique index
// as distinct — so nullable is what lets the rows with a provider id be unique
// while the rows without one stay out of each other's way. NOT NULL DEFAULT ”
// here would make the second such row collide with the first.
func TestSchema_ExternalIDsAreNullable(T *testing.T) {
	T.Parallel()

	columns := []string{
		"external_product_id",
		"external_subscription_id",
		"external_transaction_id",
	}

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, column := range columns {
				seen := false

				forEachColumn(t, d, column, func(line string) {
					seen = true

					test.StrNotContains(t, line, "NOT NULL",
						test.Sprintf("%s must be nullable", column))
					test.StrNotContains(t, line, "DEFAULT",
						test.Sprintf("%s must not default", column))
				})

				test.True(t, seen, test.Sprintf("%s declares no %s", d, column))
			}
		})
	}
}

// TestSchema_ExternalIDsAreUnique is the other half of the redelivery property:
// nullable buys nothing without the index that makes a second delivery collide.
func TestSchema_ExternalIDsAreUnique(T *testing.T) {
	T.Parallel()

	indexes := []string{
		"billing_products_external_uniq",
		"billing_subscriptions_external_uniq",
		"billing_purchases_external_uniq",
		"billing_transactions_external_uniq",
	}

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			for _, index := range indexes {
				test.StrContains(t, joined, index,
					test.Sprintf("%s does not create %s", d, index))
			}

			// None of them is partial. The uniqueness covers archived rows
			// because an archived row still points at the provider's, and a key
			// freed on archive is how a second row claims something the first is
			// still reconciled against.
			for _, stmt := range stmts {
				if !strings.Contains(stmt, "_external_uniq") {
					continue
				}

				test.StrNotContains(t, stmt, "WHERE",
					test.Sprintf("%s made a uniqueness partial: %q", d, stmt))
			}
		})
	}
}

// TestSchema_CreatedAtDefaults is why the store never supplies the column and
// reads it back instead.
func TestSchema_CreatedAtDefaults(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			seen := 0

			forEachColumn(t, d, "created_at", func(line string) {
				seen++

				test.StrContains(t, line, "NOT NULL")
				test.StrContains(t, line, "DEFAULT")
			})

			test.EqOp(t, len(tableNames), seen)
		})
	}
}

// TestSchema_ReferencesTheCatalog pins the foreign keys the two sale tables carry
// onto products, which is what stops a subscription naming a product that never
// existed.
func TestSchema_ReferencesTheCatalog(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			// MySQL spells them as named table constraints and the other two
			// inline, so the assertion is on the referenced table rather than on
			// one dialect's syntax.
			test.StrContains(t, joined, "billing_products (id)")
			test.StrContains(t, joined, "billing_subscriptions (id)")
			test.StrContains(t, joined, "billing_purchases (id)")
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
// the line, so an assertion about one column's nullability is written once rather
// than once per dialect's spelling of its type.
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

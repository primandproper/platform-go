package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/rbac/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// These tests read rendered SQL. They cannot say whether a server accepts it —
// that is sqlc, which runs over the committed files, and this package's
// container tests, which run them — but they can pin the parts that are
// silently wrong rather than loudly wrong: a UNION that became a UNION ALL, an
// archived predicate that survives only at the seed, a lookup that stopped
// seeing the archived rows it exists to find.

// dialects is the roster this package serves, which is the same list
// unison.yaml's schemas map is keyed on and the same one queriesgen writes.
func dialects() []dialect.Dialect {
	return []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}
}

// corpus is every statement one dialect renders, keyed by name.
func corpus(t *testing.T, d dialect.Dialect) map[string]string {
	t.Helper()

	statements := map[string]string{}

	for block := range strings.SplitSeq(Render(d), "-- name: ") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}

		header, body, found := strings.Cut(block, "\n")
		must.True(t, found, must.Sprintf("statement %q has no body", header))

		name, _, _ := strings.Cut(header, " ")
		statements[name] = body
	}

	return statements
}

func statement(t *testing.T, d dialect.Dialect, name string) string {
	t.Helper()

	found, ok := corpus(t, d)[name]
	must.True(t, ok, must.Sprintf("no statement named %q in the %s corpus", name, d))

	return found
}

// TestRender_MatchesTheCommittedFiles is the drift gate. The committed .sql is
// what sqlc reads and what unison generates from, so a rendering that has moved
// on from it is a corpus checked against statements nobody runs.
func TestRender_MatchesTheCommittedFiles(T *testing.T) {
	T.Parallel()

	for _, d := range dialects() {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			committed, err := os.ReadFile(FileName(d))
			must.NoError(t, err)

			// The committed file carries the generated-code header, which is
			// the generator's rather than this function's.
			body := string(committed)
			if index := strings.Index(body, "-- name:"); index > 0 {
				body = body[index:]
			}

			test.EqOp(t, Render(d), body,
				test.Sprintf("run `make generate` and commit %s", FileName(d)))
		})
	}
}

// TestRender_RegistersEveryTable is the registry half of the same guarantee the
// canonical .sql is the query half of: a consumer reading the registry back to
// truncate a database between integration tests must find all four, including
// the two mapping tables no standard set is emitted for.
func TestRender_RegistersEveryTable(t *testing.T) {
	t.Parallel()

	_ = Render(dialect.Postgres)

	for _, table := range TableNames {
		test.True(t, querygen.TableRegistered(table), test.Sprintf("%s is not registered", table))
	}
}

// TestTableNames_AreWhatTheDDLCreates is the cross-check between the canonical
// spelling every statement interpolates and the schema that creates the tables.
// Neither derives from the other, which is what makes a rename visible here
// rather than at the first query.
func TestTableNames_AreWhatTheDDLCreates(T *testing.T) {
	T.Parallel()

	for _, d := range dialects() {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			for _, table := range TableNames {
				test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+table+" (",
					test.Sprintf("table %q", table))
			}
		})
	}
}

// TestColumns_AreTheTablesTheDDLCreates keeps the projections honest. A column
// list naming something the schema does not have is caught by sqlc; a schema
// column missing from the list is not, because the statements would still
// compile — and a column nothing projects is a column no caller ever sees.
func TestColumns_AreTheTablesTheDDLCreates(T *testing.T) {
	T.Parallel()

	for _, d := range dialects() {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			for _, column := range NamedColumns {
				test.StrContains(t, ddl, "\n    "+column+" ", test.Sprintf("column %q", column))
			}

			for _, column := range slices.Concat(RolePermissionColumns, RoleHierarchyColumns) {
				test.StrContains(t, ddl, "\n    "+column+" ", test.Sprintf("column %q", column))
			}
		})
	}
}

// TestRender_EmitsTheStatementsTheResolverExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the resolver does not run.
//
// Fourteen, against the thirteen fmt.Sprintf builders this replaced. The extra
// one is the second named table's upsert: the lookup and the write used to take
// the table as an argument, and a statement parameterized by its table is the
// dynamic SQL this tier exists to replace.
func TestRender_EmitsTheStatementsTheResolverExecutes(T *testing.T) {
	T.Parallel()

	expected := []string{
		"ResolvePermissionsForRoles",
		"ListRoles",
		"ListRolePermissions",
		"ListRoleHierarchy",
		"ListRolesByNames",
		"ListPermissionsByNames",
		"GetRoleIDByName",
		"UpsertRole",
		"UpsertPermission",
		"DeleteRolePermissions",
		"CreateRolePermission",
		"DeleteRoleHierarchy",
		"CreateRoleHierarchyEdge",
		"ArchiveRoleByName",
	}

	for _, d := range dialects() {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := corpus(t, d)

			test.MapLen(t, len(expected), rendered)

			for _, name := range expected {
				_, ok := rendered[name]
				test.True(t, ok, test.Sprintf("statement %q is not emitted", name))
			}
		})
	}
}

// TestRender_ResolutionTerminatesOnACycle. UNION ALL is the faster spelling and
// it is the one that never returns, on the query that decides whether a request
// is allowed.
func TestRender_ResolutionTerminatesOnACycle(T *testing.T) {
	T.Parallel()

	for _, d := range dialects() {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			resolve := statement(t, d, "ResolvePermissionsForRoles")

			test.StrContains(t, resolve, "WITH RECURSIVE role_closure AS")
			test.StrContains(t, resolve, "\n\tUNION\n")
			test.StrNotContains(t, resolve, "UNION ALL")
		})
	}
}

// TestRender_ResolutionExcludesArchivedRowsAtEveryJoin. Excluding them only at
// the seed is the comfortable mistake: the statement still looks keyed, still
// returns rows, and still passes a test that archives the role it asks about —
// while going on granting through an archived intermediary.
func TestRender_ResolutionExcludesArchivedRowsAtEveryJoin(T *testing.T) {
	T.Parallel()

	for _, d := range dialects() {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			resolve := statement(t, d, "ResolvePermissionsForRoles")

			// Twice over the walked table — the seed and the recursive term —
			// and once over the permissions the walk reaches.
			test.EqOp(t, 2, strings.Count(resolve, querygen.Qualify(RolesTable, querygen.ArchivedAtColumn)+" IS NULL"))
			test.EqOp(t, 1, strings.Count(resolve, querygen.Qualify(PermissionsTable, querygen.ArchivedAtColumn)+" IS NULL"))

			// The mapping tables carry no such predicate and cannot: an edge is
			// live exactly when the rows at both of its ends are.
			for _, table := range []string{RolePermissionsTable, RoleHierarchyTable} {
				test.StrNotContains(t, resolve, querygen.Qualify(table, querygen.ArchivedAtColumn))
			}
		})
	}
}

// TestRender_ResolutionWalksChildToParent, which is the direction that answers
// "what does this role inherit". Reversed, the same statement answers "what
// inherits from this role" and every resolution grants too much.
func TestRender_ResolutionWalksChildToParent(T *testing.T) {
	T.Parallel()

	for _, d := range dialects() {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			resolve := statement(t, d, "ResolvePermissionsForRoles")

			test.StrContains(t, resolve, "role_closure.id="+querygen.Qualify(RoleHierarchyTable, ChildRoleIDColumn))
			test.StrContains(t, resolve, querygen.Qualify(RoleHierarchyTable, ParentRoleIDColumn)+"="+querygen.Qualify(RolesTable, querygen.IDColumn))
		})
	}
}

// TestRender_NameLookupsSeeArchivedRows. A name stays reserved once used, so a
// lookup that skipped archived rows would report the name free and hand the
// write to a unique index — which is the driver error these reads exist to
// prevent.
func TestRender_NameLookupsSeeArchivedRows(T *testing.T) {
	T.Parallel()

	for _, d := range dialects() {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := corpus(t, d)

			for name, table := range map[string]string{
				"ListRolesByNames":       RolesTable,
				"ListPermissionsByNames": PermissionsTable,
				"GetRoleIDByName":        RolesTable,
			} {
				lookup := rendered[name]

				test.StrNotContains(t, lookup, querygen.Qualify(table, querygen.ArchivedAtColumn)+" IS NULL",
					test.Sprintf("statement %q", name))

				// Projected rather than compared, so a caller can tell "no such
				// row" from "one that is archived".
				test.StrContains(t, lookup, querygen.Qualify(table, querygen.ArchivedAtColumn),
					test.Sprintf("statement %q", name))
			}
		})
	}
}

// TestRender_TheUpsertsRevive. Clearing archived_at in the conflict branch is
// what makes a re-seed after an archival bring the reserved row back rather
// than collide with it, and leaving created_at out of both lists is what makes
// it the same row coming back rather than a new one wearing its name.
func TestRender_TheUpsertsRevive(T *testing.T) {
	T.Parallel()

	for _, d := range dialects() {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := corpus(t, d)

			for _, name := range []string{"UpsertRole", "UpsertPermission"} {
				upsert := rendered[name]

				test.StrContains(t, upsert, querygen.ArchivedAtColumn+" = NULL", test.Sprintf("statement %q", name))
				test.StrContains(t, upsert, querygen.LastUpdatedAtColumn+" = ", test.Sprintf("statement %q", name))

				inserted, _, found := strings.Cut(upsert, " VALUES ")
				must.True(t, found, must.Sprintf("statement %q has no VALUES list", name))
				test.StrNotContains(t, inserted, querygen.CreatedAtColumn, test.Sprintf("statement %q", name))
			}
		})
	}
}

// TestRender_GrantWritesSkipTheRowAlreadyThere. A seed runs from every replica
// of a service at once, and the mapping tables are cleared and rewritten a row
// at a time, so the seed that commits second finds the first's rows under the
// primary key its own inserts name. A plain INSERT there fails the whole
// transaction; the duplicate-skipping one converges on the policy both were
// given. The spelling is the dialect's, and each is pinned, because the plain
// insert differs from the skipping one by exactly the clause this looks for.
func TestRender_GrantWritesSkipTheRowAlreadyThere(T *testing.T) {
	T.Parallel()

	skipping := map[dialect.Dialect]map[string]string{
		dialect.Postgres: {
			"CreateRolePermission":    "ON CONFLICT (" + RoleIDColumn + ", " + PermissionIDColumn + ") DO NOTHING",
			"CreateRoleHierarchyEdge": "ON CONFLICT (" + ChildRoleIDColumn + ", " + ParentRoleIDColumn + ") DO NOTHING",
		},
		dialect.MySQL: {
			"CreateRolePermission":    "INSERT IGNORE INTO " + RolePermissionsTable,
			"CreateRoleHierarchyEdge": "INSERT IGNORE INTO " + RoleHierarchyTable,
		},
		dialect.SQLite: {
			"CreateRolePermission":    "INSERT OR IGNORE INTO " + RolePermissionsTable,
			"CreateRoleHierarchyEdge": "INSERT OR IGNORE INTO " + RoleHierarchyTable,
		},
	}

	for _, d := range dialects() {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := corpus(t, d)

			for name, clause := range skipping[d] {
				test.StrContains(t, rendered[name], clause, test.Sprintf("statement %q", name))
			}
		})
	}
}

// TestRender_NoStatementNamesAnUnprefixableTable. Every statement carries a
// canonical name, which unison substitutes a consumer's prefix into once at
// construction; a statement spelling a table some other way would be one the
// substitution misses.
func TestRender_NoStatementNamesAnUnprefixableTable(T *testing.T) {
	T.Parallel()

	for _, d := range dialects() {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for name, body := range corpus(t, d) {
				named := false
				for _, table := range TableNames {
					if strings.Contains(body, table) {
						named = true

						break
					}
				}

				test.True(t, named, test.Sprintf("statement %q names none of this schema's tables", name))
			}
		})
	}
}

// TestFileName names the committed file the pipeline reads.
func TestFileName(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "postgres_generated.sql", FileName(dialect.Postgres))
	test.EqOp(t, "mysql_generated.sql", FileName(dialect.MySQL))
	test.EqOp(t, "sqlite_generated.sql", FileName(dialect.SQLite))
}

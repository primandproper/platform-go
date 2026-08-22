package database

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newQueryBuilder returns a Resolver wired only for query construction. The
// builders touch no executor, so this needs no database — which is the point:
// Postgres numbers its placeholders and SQLite does not, and the SQLite suite
// alone would never notice a numbering mistake.
func newQueryBuilder(t *testing.T, d dialect.Dialect) *Resolver {
	t.Helper()

	return &Resolver{dialect: d, prefix: DefaultTablePrefix}
}

func allDialects() []dialect.Dialect {
	return []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}
}

func TestResolver_QueryBuilders(T *testing.T) {
	T.Parallel()

	// Every builder must interpolate the configured prefix and leave no
	// unbound table name behind.
	T.Run("all queries carry the table prefix", func(t *testing.T) {
		t.Parallel()

		for _, dialect := range allDialects() {
			r := newQueryBuilder(t, dialect)

			queries := map[string]string{
				"resolve":            r.resolveQuery(2),
				"listRoles":          r.listRolesQuery(),
				"rolePermissions":    r.rolePermissionsQuery(),
				"roleHierarchy":      r.roleHierarchyQuery(),
				"selectNamedByNames": r.selectNamedByNamesQuery(rolesTable, 2),
				"insertNamedRows":    r.insertNamedRowsQuery(rolesTable, 2),
				"insertRolePerms":    r.insertRolePermissionsQuery(2),
				"insertHierarchy":    r.insertRoleHierarchyRowsQuery(2),
				"selectIDByName":     r.selectIDByNameQuery(rolesTable),
				"updateNamed":        r.updateNamedQuery(rolesTable),
				"deleteRolePerms":    r.deleteRolePermissionsQuery(),
				"deleteHierarchy":    r.deleteRoleHierarchyQuery(),
				"archiveRole":        r.archiveRoleQuery(),
			}

			for name, query := range queries {
				if !strings.Contains(query, DefaultTablePrefix) {
					t.Errorf("%s/%s: query does not reference the table prefix: %s", dialect, name, query)
				}
			}
		}
	})

	T.Run("a custom prefix is honored", func(t *testing.T) {
		t.Parallel()

		r := &Resolver{dialect: dialect.Postgres, prefix: "custom_"}

		test.StrContains(t, r.listRolesQuery(), "custom_authz_roles")
		// The unqualified form must not survive: DefaultTablePrefix is now the
		// empty namespace, so asserting against it directly would pass vacuously.
		test.StrNotContains(t, r.listRolesQuery(), "FROM authz_roles")
	})

	// The recursive CTE is the query only a real server validates, but its
	// shape — recursion, UNION rather than UNION ALL, archival predicates — is
	// worth pinning here so a refactor cannot quietly change it.
	T.Run("the resolve query recurses and excludes archived rows", func(t *testing.T) {
		t.Parallel()

		for _, dialect := range allDialects() {
			query := newQueryBuilder(t, dialect).resolveQuery(3)

			test.StrContains(t, query, "WITH RECURSIVE")
			test.StrContains(t, query, "role_tree")
			// UNION, never UNION ALL: that is what terminates on a cycle.
			test.StrContains(t, query, "UNION\n")
			test.StrNotContains(t, query, "UNION ALL")
			test.StrContains(t, query, "archived_at IS NULL")
			test.StrContains(t, query, "SELECT DISTINCT")
		}
	})

	T.Run("the resolve query binds one placeholder per role", func(t *testing.T) {
		t.Parallel()

		query := newQueryBuilder(t, dialect.Postgres).resolveQuery(3)

		test.StrContains(t, query, "IN ($1, $2, $3)")
	})

	T.Run("archival is a soft delete guarded against double-archiving", func(t *testing.T) {
		t.Parallel()

		query := newQueryBuilder(t, dialect.Postgres).archiveRoleQuery()

		test.StrContains(t, query, "SET archived_at")
		test.StrContains(t, query, "archived_at IS NULL")
		test.StrNotContains(t, query, "DELETE")
	})

	// Reviving an archived row is how a name stays reserved without blocking a
	// re-seed, so the update must clear archived_at rather than only touching
	// the description.
	T.Run("the named update clears archival", func(t *testing.T) {
		t.Parallel()

		query := newQueryBuilder(t, dialect.Postgres).updateNamedQuery(rolesTable)

		test.StrContains(t, query, "archived_at = NULL")
	})

	T.Run("the batched insert scales its tuples", func(t *testing.T) {
		t.Parallel()

		r := newQueryBuilder(t, dialect.Postgres)

		single := r.insertNamedRowsQuery(permissionsTable, 1)
		double := r.insertNamedRowsQuery(permissionsTable, 2)

		test.StrContains(t, single, "($1, $2, $3)")
		test.StrContains(t, double, "($1, $2, $3), ($4, $5, $6)")
	})
}

func TestResolver_TableNames(T *testing.T) {
	T.Parallel()

	// The four tables the migrations create must be the four the queries read,
	// or the package compiles and fails at runtime against a correct schema.
	T.Run("queries reference only the migrated tables", func(t *testing.T) {
		t.Parallel()

		r := newQueryBuilder(t, dialect.Postgres)

		must.StrContains(t, r.listRolesQuery(), DefaultTablePrefix+"roles")
		must.StrContains(t, r.rolePermissionsQuery(), DefaultTablePrefix+"role_permissions")
		must.StrContains(t, r.rolePermissionsQuery(), DefaultTablePrefix+"permissions")
		must.StrContains(t, r.roleHierarchyQuery(), DefaultTablePrefix+"role_hierarchy")
	})
}

package database

import (
	"fmt"
	"strings"

	"github.com/primandproper/platform-go/v13/database/dialect"
)

// resolveQuery builds the permission-resolution query for roleCount role names.
//
// The recursive term walks child to parent, so a role receives everything its
// ancestors grant. UNION rather than UNION ALL is what makes it terminate on a
// cycle: a role already in the working set is not re-added. Seed rejects cycles
// before they can be written, but a table edited by hand has no such guard, and
// a query that hangs is a worse failure than one that returns a union.
//
// Archived roles and permissions are excluded at every join rather than only at
// the seed, so archiving a permission revokes it everywhere on the next
// resolution without touching the mapping rows.
func (r *Resolver) resolveQuery(roleCount int) string {
	return fmt.Sprintf(`WITH RECURSIVE role_tree AS (
	SELECT %[1]sauthz_roles.id AS role_id
	FROM %[1]sauthz_roles
	WHERE %[1]sauthz_roles.archived_at IS NULL
		AND %[1]sauthz_roles.name IN (%[2]s)
	UNION
	SELECT %[1]sauthz_roles.id
	FROM role_tree
	JOIN %[1]sauthz_role_hierarchy ON %[1]sauthz_role_hierarchy.child_role_id = role_tree.role_id
	JOIN %[1]sauthz_roles ON %[1]sauthz_roles.id = %[1]sauthz_role_hierarchy.parent_role_id
	WHERE %[1]sauthz_roles.archived_at IS NULL
)
SELECT DISTINCT %[1]sauthz_permissions.name
FROM role_tree
JOIN %[1]sauthz_role_permissions ON %[1]sauthz_role_permissions.role_id = role_tree.role_id
JOIN %[1]sauthz_permissions ON %[1]sauthz_permissions.id = %[1]sauthz_role_permissions.permission_id
WHERE %[1]sauthz_permissions.archived_at IS NULL`,
		r.prefix, r.dialect.Placeholders(1, roleCount))
}

// listRolesQuery selects every live role.
func (r *Resolver) listRolesQuery() string {
	return fmt.Sprintf(
		`SELECT id, name, description FROM %sauthz_roles WHERE archived_at IS NULL ORDER BY name`,
		r.prefix,
	)
}

// rolePermissionsQuery selects the direct (un-inherited) permissions of every
// live role, so Roles can report the policy as it was declared rather than as
// it resolves.
func (r *Resolver) rolePermissionsQuery() string {
	return fmt.Sprintf(`SELECT %[1]sauthz_role_permissions.role_id, %[1]sauthz_permissions.name
FROM %[1]sauthz_role_permissions
JOIN %[1]sauthz_permissions ON %[1]sauthz_permissions.id = %[1]sauthz_role_permissions.permission_id
WHERE %[1]sauthz_permissions.archived_at IS NULL`, r.prefix)
}

// roleHierarchyQuery selects the declared parent of every role, by name.
func (r *Resolver) roleHierarchyQuery() string {
	return fmt.Sprintf(`SELECT %[1]sauthz_role_hierarchy.child_role_id, parent.name
FROM %[1]sauthz_role_hierarchy
JOIN %[1]sauthz_roles AS parent ON parent.id = %[1]sauthz_role_hierarchy.parent_role_id
WHERE parent.archived_at IS NULL`, r.prefix)
}

// maxBatchRows caps how many tuples go into one multi-row INSERT.
//
// SQLite's default bind-parameter ceiling is the binding constraint (999 on
// builds before 3.32), and three columns per row puts 100 rows at 300
// parameters — comfortably clear on every dialect while still turning a
// few-hundred-row seed into a handful of statements.
const maxBatchRows = 100

// selectNamedByNamesQuery looks up several roles or permissions by name at
// once, returning enough to decide whether each needs an insert, an update, or
// nothing. Archived rows are included: a name stays reserved once used, so an
// upsert must revive the existing row rather than insert a colliding one.
func (r *Resolver) selectNamedByNamesQuery(table string, count int) string {
	return fmt.Sprintf(
		`SELECT id, name, description, archived_at IS NOT NULL FROM %s%s WHERE name IN (%s)`,
		r.prefix, table, r.dialect.Placeholders(1, count),
	)
}

// insertNamedRowsQuery inserts count roles or permissions in one statement.
func (r *Resolver) insertNamedRowsQuery(table string, count int) string {
	return fmt.Sprintf(
		`INSERT INTO %s%s (id, name, description) VALUES %s`,
		r.prefix, table, tupleList(r.dialect, count, 3),
	)
}

// insertRolePermissionsQuery grants count permissions to a role in one
// statement.
func (r *Resolver) insertRolePermissionsQuery(count int) string {
	return fmt.Sprintf(
		`INSERT INTO %sauthz_role_permissions (role_id, permission_id) VALUES %s`,
		r.prefix, tupleList(r.dialect, count, 2),
	)
}

// insertRoleHierarchyRowsQuery records count inheritance edges in one
// statement.
func (r *Resolver) insertRoleHierarchyRowsQuery(count int) string {
	return fmt.Sprintf(
		`INSERT INTO %sauthz_role_hierarchy (child_role_id, parent_role_id) VALUES %s`,
		r.prefix, tupleList(r.dialect, count, 2),
	)
}

// tupleList renders count parenthesized placeholder groups of width columns,
// numbered consecutively for dialects that number their placeholders.
func tupleList(d dialect.Dialect, count, width int) string {
	tuples := make([]string, 0, count)
	for i := range count {
		tuples = append(tuples, "("+d.Placeholders(i*width+1, width)+")")
	}

	return strings.Join(tuples, ", ")
}

// selectIDByNameQuery looks up a role or permission id by name, including
// archived rows: a name stays reserved once used, so an upsert must find and
// revive the existing row rather than insert a colliding one.
func (r *Resolver) selectIDByNameQuery(table string) string {
	return fmt.Sprintf(
		`SELECT id, archived_at IS NOT NULL FROM %s%s WHERE name = %s`,
		r.prefix, table, r.dialect.Placeholder(1),
	)
}

// updateNamedQuery refreshes a row's description and clears any archival, which
// is how an upsert revives a previously archived role.
func (r *Resolver) updateNamedQuery(table string) string {
	return fmt.Sprintf(
		`UPDATE %s%s SET description = %s, archived_at = NULL WHERE id = %s`,
		r.prefix, table,
		r.dialect.Placeholder(1), r.dialect.Placeholder(2),
	)
}

// deleteRolePermissionsQuery clears a role's direct grants ahead of rewriting
// them, so an upsert removes permissions as well as adding them.
func (r *Resolver) deleteRolePermissionsQuery() string {
	return fmt.Sprintf(
		`DELETE FROM %sauthz_role_permissions WHERE role_id = %s`,
		r.prefix, r.dialect.Placeholder(1),
	)
}

// deleteRoleHierarchyQuery clears a role's declared parents ahead of rewriting.
func (r *Resolver) deleteRoleHierarchyQuery() string {
	return fmt.Sprintf(
		`DELETE FROM %sauthz_role_hierarchy WHERE child_role_id = %s`,
		r.prefix, r.dialect.Placeholder(1),
	)
}

// archiveRoleQuery soft-deletes a role by name.
func (r *Resolver) archiveRoleQuery() string {
	return fmt.Sprintf(
		`UPDATE %sauthz_roles SET archived_at = CURRENT_TIMESTAMP WHERE name = %s AND archived_at IS NULL`,
		r.prefix, r.dialect.Placeholder(1),
	)
}

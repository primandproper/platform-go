package queries

import (
	"slices"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// The four tables this package owns, at their canonical unprefixed spelling —
// what the emitted .sql names, and what a consumer's own prefix is rendered
// onto.
const (
	RolesTable           = "authz_roles"
	PermissionsTable     = "authz_permissions"
	RolePermissionsTable = "authz_role_permissions"
	RoleHierarchyTable   = "authz_role_hierarchy"
)

// TableNames is every table the authorization schema owns, in the order the DDL
// creates them.
//
// Four, and only the two named ones get a column list worth the name. The
// mapping tables are still tables with rows in them, and the registry a
// consumer reads back to truncate a database has to be fed by the table
// existing rather than by something choosing to emit a standard set for it.
var TableNames = []string{
	RolesTable,
	PermissionsTable,
	RolePermissionsTable,
	RoleHierarchyTable,
}

// The columns the two named tables carry beyond the convention triple. They are
// spelled here because the corpus and the store both name them, and a name
// spelled in two places is a name that can differ in one.
const (
	// NameColumn is the identifier a role or a permission is known by, and the
	// natural key every write here converges on. It is unique across live and
	// archived rows alike — see [NamedColumns].
	NameColumn = "name"
	// DescriptionColumn is the prose an operator reads. It is the one column an
	// upsert carries over onto a row that is already there.
	DescriptionColumn = "description"
)

// The mapping tables' columns. Each is a foreign key, and each pair is its
// table's whole primary key.
const (
	// RoleIDColumn and PermissionIDColumn are the grant: this role holds this
	// permission.
	RoleIDColumn       = "role_id"
	PermissionIDColumn = "permission_id"
	// ChildRoleIDColumn and ParentRoleIDColumn are the inheritance edge, in the
	// direction resolution walks it: a child receives what its parent grants.
	ChildRoleIDColumn  = "child_role_id"
	ParentRoleIDColumn = "parent_role_id"
)

// The arguments the statements bind beyond a column's own name.
const (
	// NamesArg is the set of role or permission names a batched lookup answers
	// for, and the set of role names a resolution starts from.
	NamesArg = "names"
)

// The prefixes the two junction reads alias their joined table's columns with.
//
// Both reads join a named table onto a mapping table that already carries a
// column ending in _id, so an unaliased projection would answer with two
// columns of the same name and a row type whose field names depend on which
// table the SELECT happened to list first. See querygen.Junction.Prefix.
const (
	permissionPrefix = "permission"
	parentPrefix     = "parent"
)

// NamedColumns is every column of authz_roles and authz_permissions, in the
// order each read projects them.
//
// The two tables are the same shape, which is not a coincidence worth
// factoring away: a role and a permission are both "a name an operator gave
// something, with prose beside it", and the statements over them are the same
// statements under two names. Sharing the list is what keeps a column added to
// one from being forgotten on the other.
//
// The convention triple is all three columns, and each is load-bearing here.
// created_at is when the policy first named this thing; last_updated_at is
// stamped by every upsert that changed something; archived_at is the soft
// delete that makes resolution stop finding a role without freeing its name —
// see rbac's own comment on why the name stays reserved.
var NamedColumns = []string{
	querygen.IDColumn,
	NameColumn,
	DescriptionColumn,
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	querygen.ArchivedAtColumn,
}

// RolePermissionColumns and RoleHierarchyColumns are the mapping tables in
// full, which is also each one's primary key.
//
// Neither carries any of the convention triple, by design and by assertion —
// internal/schemaconvention exempts them as mapping rows rewritten wholesale
// with their role. Nothing lists, filters or soft-deletes an edge on its own:
// revoking one deletes the row, and archiving either endpoint already hides
// every edge that names it. So the statements over them are the id-less child
// shapes — an insert, a delete keyed on the parent, and a read through a join —
// rather than the standard set.
var (
	RolePermissionColumns = []string{RoleIDColumn, PermissionIDColumn}
	RoleHierarchyColumns  = []string{ChildRoleIDColumn, ParentRoleIDColumn}
)

// LookupProjection is what the two name-keyed reads answer with: the id the
// caller came for, and whether the row it found is archived.
//
// archived_at is projected rather than compared, which is the difference
// between "there is no such role" and "there is one and it is archived". The
// caller is an upsert, and those are different next moves: mint an id, or
// revive the row that already holds the name.
var LookupProjection = []string{querygen.IDColumn, NameColumn, DescriptionColumn, querygen.ArchivedAtColumn}

// Render returns the canonical sqlc input for one dialect: the resolution, the
// three reads that report the policy as declared, the name lookups an upsert
// begins with, and the writes.
//
// It is what rbac/internal/queriesgen writes to the .sql
// files beside this one and what CI regenerates to check the committed copies
// still match. Those files are sqlc-gen-unison's input, so what the resolver
// executes is this text exactly, with the consumer's table prefix substituted
// for {{prefix}} once at construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// Every table this schema owns, not the ones a statement below happens to
	// name first: a consumer reading the registry back to truncate a database
	// between integration tests needs all four.
	querygen.RegisterTable(TableNames...)

	rendered := []*querygen.Query{resolution(g)}

	rendered = append(rendered, declaredPolicyReads(g)...)
	rendered = append(rendered, nameLookups(g)...)
	rendered = append(rendered, namedWrites(g)...)
	rendered = append(rendered, grantWrites(g)...)
	rendered = append(rendered, g.ArchiveQuery("ArchiveRoleByName", RolesTable, columnsExcept(querygen.IDColumn),
		querygen.Match{Column: NameColumn}))

	return querygen.RenderFile(rendered)
}

// resolution is the question this package exists to answer: everything the
// named roles grant, inheritance expanded.
//
// It is a closure read, which is the shape a policy with inheritance has no way
// around — the depth is what an operator declared rather than something a
// statement could be written for. The two properties that make it correct are
// the shape's rather than this call's, and querygen.Generator.ClosureQuery is
// where both are argued: UNION rather than UNION ALL, so a hierarchy somebody
// edited into a cycle terminates the walk instead of hanging the request that
// asked; and the archived predicate at every join rather than only at the seed,
// so archiving a permission revokes it everywhere on the next resolution and
// archiving an intermediate role stops the walk at it.
//
// The seed is a bound set because a principal holds several roles and the
// answer is the union of what they grant. It is the resolver's only read of the
// hot path, which is why it is one statement rather than a walk: a level per
// round trip, on the query that decides whether a request is allowed.
func resolution(g *querygen.Generator) *querygen.Query {
	return g.ClosureQuery("ResolvePermissionsForRoles", RolesTable, NamedColumns,
		&querygen.Closure{
			Alias:      "role_closure",
			Walk:       querygen.Edge{Table: RoleHierarchyTable, From: ChildRoleIDColumn, To: ParentRoleIDColumn},
			Reach:      querygen.Edge{Table: RolePermissionsTable, From: RoleIDColumn, To: PermissionIDColumn},
			Table:      PermissionsTable,
			Columns:    NamedColumns,
			Projection: []string{NameColumn},
		},
		querygen.SetKey{Column: NameColumn, Arg: NamesArg})
}

// declaredPolicyReads is the policy as it was written down rather than as it
// resolves: every live role, the permissions each one holds directly, and the
// parent each one names.
//
// Three unpaged reads rather than a page each. The catalog is what an operator
// typed — a handful of roles with a handful of permissions apiece — and the
// caller is either an administrative listing or the validation an upsert runs
// against the policy already stored, both of which want the whole of it. A
// paged read here would be a cursor walk over a table that fits in one answer.
//
// They are three statements rather than one join because the answer is two
// one-to-many relationships hanging off the same rows. Joined into a single
// read, a role with four permissions and two parents comes back eight times and
// the caller de-duplicates what the server multiplied.
//
// The two junction reads project one column of the mapping table and the whole
// of the table joined onto it. The mapping side needs only the id that groups
// the rows — the other half of the pair is the join's own predicate — and
// leaving it out of the column list is what keeps the projection from
// answering with two columns called permission_id. What the joined side
// contributes beyond the name it is read for is the archived predicate, which
// is why its full column list is handed over rather than the one column
// anything reads: an archived permission has to stop appearing in the declared
// policy exactly as it stops appearing in a resolution.
func declaredPolicyReads(g *querygen.Generator) []*querygen.Query {
	return []*querygen.Query{
		g.JunctionListAllQuery("ListRoles", RolesTable, NamedColumns, nil,
			[]querygen.Order{{Column: NameColumn}}),

		g.JunctionListAllQuery("ListRolePermissions", RolePermissionsTable, []string{RoleIDColumn},
			&querygen.Junction{
				Table:    PermissionsTable,
				Column:   querygen.IDColumn,
				OnColumn: PermissionIDColumn,
				Prefix:   permissionPrefix,
				Columns:  NamedColumns,
			},
			[]querygen.Order{{Column: RoleIDColumn}}),

		g.JunctionListAllQuery("ListRoleHierarchy", RoleHierarchyTable, []string{ChildRoleIDColumn},
			&querygen.Junction{
				Table:    RolesTable,
				Column:   querygen.IDColumn,
				OnColumn: ParentRoleIDColumn,
				Prefix:   parentPrefix,
				Columns:  NamedColumns,
			},
			[]querygen.Order{{Column: ChildRoleIDColumn}}),
	}
}

// nameLookups is how a write finds the row a name already belongs to: the
// batched form a seed runs once per table, and the single form a parent lookup
// runs.
//
// All three include archived rows, and that is the point of them rather than an
// oversight. A name stays reserved once used — archiving a role must not free
// its name for a new role that would inherit every assignment still naming it —
// so a lookup that skipped archived rows would report the name free and hand
// the write to a unique index. The archived predicate is left off by handing
// over a column list without archived_at, which is how every statement in this
// module says it wants one, and the column is projected instead so the caller
// can tell "no such row" from "one that is archived".
//
// The batched pair keys on a whole set at once because a seed resolves every
// role or permission in a policy before it writes any of them. One statement
// per name would be a few hundred round trips inside a single transaction,
// which is long enough to matter for lock hold time even at deploy.
func nameLookups(g *querygen.Generator) []*querygen.Query {
	byNames := querygen.SetKey{Column: NameColumn, Arg: NamesArg}
	lookup := querygen.Read{Projection: LookupProjection}

	return []*querygen.Query{
		g.SetReadQuery("ListRolesByNames", RolesTable, columnsExcept(querygen.ArchivedAtColumn), lookup, byNames),
		g.SetReadQuery("ListPermissionsByNames", PermissionsTable, columnsExcept(querygen.ArchivedAtColumn), lookup, byNames),

		g.ReadQuery("GetRoleIDByName", RolesTable, nil,
			querygen.Read{Projection: []string{querygen.IDColumn, querygen.ArchivedAtColumn}},
			querygen.Match{Column: NameColumn}),
	}
}

// namedWrites is the upsert, once per named table.
//
// It converges on the name rather than inserting, because the name is unique
// across live and archived rows alike: a policy re-seeded after a role was
// archived has to revive the row that already holds the name rather than insert
// a colliding one. The conflict branch is querygen's — the description carried
// over from the incoming row, archived_at cleared, last_updated_at stamped —
// and clearing archived_at there is exactly what makes a re-seed a revival.
//
// The id is supplied rather than generated, and a caller that found an existing
// row supplies the id it found. That is not decoration: MySQL's ON DUPLICATE
// KEY UPDATE fires on whichever unique key was violated, so an upsert carrying
// a fresh id for a name already taken converges on the name's row and leaves
// the caller holding an id no row has. The store looks the ids up first for
// that reason, mints one only for a name it did not find, and reads the minted
// names back once written — a concurrent seed can take the name between the
// lookup and the write, and this statement converges on that seed's row.
//
// created_at is in neither list. A revived role keeps the creation time it was
// first written with, which is what makes it the same role coming back rather
// than a new one wearing its name.
func namedWrites(g *querygen.Generator) []*querygen.Query {
	byName := querygen.Match{Column: NameColumn}

	insertColumns := querygen.ForInsert(NamedColumns)
	updateColumns := querygen.ForUpdate(NamedColumns)

	return []*querygen.Query{
		g.UpsertQuery("UpsertRole", RolesTable, NamedColumns, insertColumns, updateColumns, nil, byName),
		g.UpsertQuery("UpsertPermission", PermissionsTable, NamedColumns, insertColumns, updateColumns, nil, byName),
	}
}

// grantWrites is how a role's direct grants and declared parents are replaced:
// clear the set, then write it back a row at a time.
//
// Clear-then-rewrite rather than diff. It makes an upsert remove grants as well
// as add them, which a caller re-running a seed after deleting a permission
// from a role's list is entitled to expect, and it is two statement shapes
// instead of the read-compute-write a diff needs.
//
// The rewrite skips a row that is already there rather than inserting
// unconditionally, because the writer of a mapping row is not the only writer
// there can be. A seed runs at deploy, from every replica of the service at
// once — the doc's own wiring example puts it beside the migration, and a
// migrator's advisory lock serializes the schema and not the seed that follows
// it. Two seeds of one policy each clear a role's grants and write them back;
// whichever commits second, on Postgres, finds the first's rows already under
// the primary key its own inserts name, and a plain INSERT there fails the
// whole transaction with a duplicate-key error that looks like a deploy flake.
// Skipping the duplicate is what the constraint permits: a grant that is
// already written is the grant this seed wanted, and the DELETE before it has
// already revoked whatever this seed did not. So the concurrent case converges
// on the policy both were given instead of failing one of them, and the
// annotation is the shape's :execrows, which the resolver has no reason to
// read — a skipped grant and a written one are the same grant.
//
// One insert per row rather than a multi-row VALUES list. The multi-row form's
// arity is the caller's cardinality, so it has no static text — nothing for
// sqlc to check and nothing for querygen to emit — and what replaces it costs a
// round trip per grant, inside the transaction the role's own write already
// opened, at the cardinalities a role's permission list actually has. It also
// retires the batch ceiling the assembled form needed, which existed only
// because SQLite counts bind parameters.
func grantWrites(g *querygen.Generator) []*querygen.Query {
	return []*querygen.Query{
		g.DeleteQuery("DeleteRolePermissions", RolePermissionsTable, RolePermissionColumns,
			querygen.Match{Column: RoleIDColumn}),
		g.InsertIgnoreQuery("CreateRolePermission", RolePermissionsTable, RolePermissionColumns, nil,
			querygen.Match{Column: RoleIDColumn}, querygen.Match{Column: PermissionIDColumn}),

		g.DeleteQuery("DeleteRoleHierarchy", RoleHierarchyTable, RoleHierarchyColumns,
			querygen.Match{Column: ChildRoleIDColumn}),
		g.InsertIgnoreQuery("CreateRoleHierarchyEdge", RoleHierarchyTable, RoleHierarchyColumns, nil,
			querygen.Match{Column: ChildRoleIDColumn}, querygen.Match{Column: ParentRoleIDColumn}),
	}
}

// columnsExcept returns [NamedColumns] without the named columns, in projection
// order.
//
// It is how a statement here says it wants one of the derived predicates left
// off: querygen renders the id predicate and the archived predicate from the
// column list it is handed, so a read that must see archived rows is a read
// rendered from a list without archived_at, and one keyed on the name rather
// than the id is rendered from a list without the id. What a statement projects
// is a separate list, so leaving a column out here does not take it out of the
// answer.
func columnsExcept(excluded ...string) []string {
	kept := make([]string, 0, len(NamedColumns))

	for _, column := range NamedColumns {
		if !slices.Contains(excluded, column) {
			kept = append(kept, column)
		}
	}

	return kept
}

// FileName is the file one dialect's rendered queries are committed to.
//
// The _generated suffix is in the path rather than only in the header comment,
// because a path is what a reviewer sees in a diff, what CI's glob selects, and
// what a reader scanning this directory reads first — and these are the files
// whose answer to "this line is wrong" is to edit something else.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}

package database

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/primandproper/platform-go/v13/authorization"
	"github.com/primandproper/platform-go/v13/authorization/database/migrations"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// serviceName names the Resolver's logger, spans, and metrics.
const serviceName = "authorization_database"

// DefaultTablePrefix is the namespace the policy tables carry when none is
// configured, which is none — rendering authz_roles, authz_permissions, and
// the two join tables.
//
// The authz_ segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders
// ddb_authz_roles, for a database shared between applications. A namespace must
// not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// Component-qualified table names, appended to the caller's namespace. They
// carry the authz_ segment because the schema does: a table always says which
// package created it, so authz_roles is legible in a shared database without
// consulting this module.
const (
	rolesTable       = "authz_roles"
	permissionsTable = "authz_permissions"
)

var _ authorization.PolicyResolver = (*Resolver)(nil)

// Resolver resolves role names against policy stored in SQL tables.
//
// Use it when roles themselves must be editable data — when an operator has to
// define a new role, or change what an existing one grants, without shipping a
// release. If the roles are fixed at build time, authorization/static answers
// the same questions with no database.
//
// Resolution is one query. It is nonetheless worth wrapping in
// authorization/cached: policy changes rarely and there are usually a handful
// of roles, so a cache keyed by role names has a hit rate near one and is
// shared across every principal, rather than per-principal.
type Resolver struct {
	db   database.SQLQueryExecutor
	o11y observability.Observer

	resolutionsCounter metrics.Int64Counter
	errorsCounter      metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read r.o11y.Logger() for the logger this resolver actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	metricsProvider metrics.Provider
	tracerProvider  tracing.Provider
	dialect         dialect.Dialect
	// prefix is the caller's namespace with its separator already appended —
	// "ddb_" for a namespace of "ddb", and empty for none. Every query in
	// queries.go interpolates it immediately before a component-qualified table
	// name (%[1]sauthz_roles), so resolving the separator once here keeps that
	// concatenation out of thirteen format strings.
	prefix string
}

// Config configures a Resolver.
type Config struct {
	// Dialect selects the SQL emitted. Required.
	Dialect dialect.Dialect `env:"DIALECT" json:"dialect,omitempty" yaml:"dialect,omitempty"`
	// TablePrefix is the namespace prepended to every policy table name. Empty
	// renders the schema's own names (authz_roles); set it to share a database
	// between applications, which renders e.g. ddb_authz_roles. It must not end
	// in '_' — the separator is supplied for you.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

// NewResolver builds a Resolver. The executor is used for reads; writes take
// the caller's executor per call so that a policy change commits with whatever
// else its transaction did.
func NewResolver(cfg *Config, db database.SQLQueryExecutor, opts ...Option) (*Resolver, error) {
	if cfg == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "config")
	}
	if db == nil {
		return nil, ErrNilExecutor
	}
	if !cfg.Dialect.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "authorization dialect %q", cfg.Dialect)
	}

	prefix := cfg.TablePrefix
	if prefix == "" {
		prefix = DefaultTablePrefix
	}
	if !ddl.ValidNamespace(prefix) {
		return nil, platformerrors.Wrapf(ErrInvalidTablePrefix, "prefix %q", prefix)
	}

	if err := migrations.ValidatePrefix(prefix); err != nil {
		return nil, err
	}

	// The separator is appended once, here; see the field's comment.
	r := &Resolver{db: db, dialect: cfg.Dialect, prefix: ddl.Qualify(prefix)}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	r.o11y = observability.NewObserver(serviceName, r.logger, r.tracerProvider)

	mp := metrics.EnsureMetricsProvider(r.metricsProvider)

	var err error
	if r.resolutionsCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_resolutions", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating policy resolutions counter")
	}
	if r.errorsCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_errors", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating policy resolution errors counter")
	}

	return r, nil
}

// PermissionsForRoles resolves the named roles, expanding inheritance in SQL.
func (r *Resolver) PermissionsForRoles(ctx context.Context, roles ...string) (*authorization.PermissionSet, error) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	if len(roles) == 0 {
		return authorization.NewPermissionSet(), nil
	}

	op.Set(keys.AuthorizationRolesKey, roles)

	args := make([]any, 0, len(roles))
	for _, name := range roles {
		args = append(args, name)
	}

	names, err := scanStrings(ctx, r.db, r.resolveQuery(len(roles)), args)
	if err != nil {
		r.errorsCounter.Add(ctx, 1)

		return nil, op.Error(err, "resolving permissions for roles")
	}

	r.resolutionsCounter.Add(ctx, 1)

	perms := make([]authorization.Permission, len(names))
	for i, n := range names {
		perms[i] = authorization.Permission(n)
	}

	return authorization.NewPermissionSet(perms...), nil
}

// Roles returns the policy as declared: each role with its direct permissions
// and its declared parents, not its resolved closure.
func (r *Resolver) Roles(ctx context.Context) ([]authorization.Role, error) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	roles, err := r.rolesWith(ctx, r.db)
	if err != nil {
		return nil, op.Error(err, "listing roles")
	}

	return roles, nil
}

// rolesWith reads the declared policy through the given executor.
//
// Taking an executor rather than always using r.db is what lets UpsertRole read
// the existing policy from inside the caller's transaction. Reading through
// r.db there would need a second connection while the caller's transaction
// holds one — which deadlocks outright against a pool of one, and reads
// pre-transaction state against a larger pool, so the validation would be
// checking a policy that is no longer current.
func (r *Resolver) rolesWith(ctx context.Context, q database.SQLQueryExecutor) ([]authorization.Role, error) {
	type roleRow struct {
		id, name, description string
	}

	rows, err := q.QueryContext(ctx, r.listRolesQuery())
	if err != nil {
		return nil, err
	}

	var roleRows []roleRow
	if err = scanRows(rows, func() error {
		var rr roleRow
		if scanErr := rows.Scan(&rr.id, &rr.name, &rr.description); scanErr != nil {
			return scanErr
		}
		roleRows = append(roleRows, rr)

		return nil
	}); err != nil {
		return nil, platformerrors.Wrap(err, "scanning roles")
	}

	permsByRoleID, err := r.pairsByID(ctx, q, r.rolePermissionsQuery())
	if err != nil {
		return nil, platformerrors.Wrap(err, "loading role permissions")
	}

	parentsByRoleID, err := r.pairsByID(ctx, q, r.roleHierarchyQuery())
	if err != nil {
		return nil, platformerrors.Wrap(err, "loading role hierarchy")
	}

	out := make([]authorization.Role, 0, len(roleRows))
	for i := range roleRows {
		rr := &roleRows[i]
		permNames := permsByRoleID[rr.id]
		slices.Sort(permNames)

		perms := make([]authorization.Permission, len(permNames))
		for i, n := range permNames {
			perms[i] = authorization.Permission(n)
		}

		parents := parentsByRoleID[rr.id]
		slices.Sort(parents)

		out = append(out, authorization.Role{
			Name:        rr.name,
			Description: rr.description,
			Permissions: perms,
			Inherits:    parents,
		})
	}

	return out, nil
}

// pairsByID runs a two-column (id, value) query and groups values by id.
func (r *Resolver) pairsByID(
	ctx context.Context,
	q database.SQLQueryExecutor,
	query string,
) (map[string][]string, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}

	out := map[string][]string{}
	if err = scanRows(rows, func() error {
		var id, value string
		if scanErr := rows.Scan(&id, &value); scanErr != nil {
			return scanErr
		}
		out[id] = append(out[id], value)

		return nil
	}); err != nil {
		return nil, err
	}

	return out, nil
}

// Seed writes roles into the policy tables, using the caller's executor so the
// whole policy lands in one transaction or not at all.
//
// It is the counterpart to handing the same []authorization.Role to
// authorization/static: one declaration, either compiled in or written to the
// database. That is what keeps a code-side policy and a database-side policy
// from drifting, which is the failure this backend would otherwise invite.
//
// Seed is idempotent. It upserts by name, rewrites each named role's direct
// permissions and parents, and leaves roles it was not given alone — so it can
// be run on every deploy without clobbering roles an operator added.
func (r *Resolver) Seed(ctx context.Context, q database.SQLQueryExecutor, roles ...authorization.Role) error {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		return op.Error(ErrNilExecutor, "seeding authorization policy")
	}

	// Validated before anything is written, so a malformed policy cannot be
	// half-applied. The same check runs in static.NewResolver, which is what
	// makes a policy rejected in one backend rejected in the other.
	if err := authorization.ValidateRoles(roles...); err != nil {
		return op.Error(err, "validating roles")
	}

	op.Set(keys.AuthorizationRoleCountKey, len(roles))

	wanted := make(map[string]string, len(roles))
	for i := range roles {
		wanted[roles[i].Name] = roles[i].Description
	}

	roleIDs, err := r.resolveNamedIDs(ctx, q, rolesTable, wanted)
	if err != nil {
		return op.Error(err, "upserting roles")
	}

	for i := range roles {
		if err = r.writeRoleGrants(ctx, q, roleIDs, &roles[i]); err != nil {
			return op.Error(err, "writing grants for role %q", roles[i].Name)
		}
	}

	return nil
}

// UpsertRole writes a single role. It validates the role against the policy
// already in the database, so a parent that does not exist — or an inheritance
// cycle the new edge would close — is rejected rather than written.
//
// signature; a pointer here would make the two ways of writing policy differ
// for no benefit at a call rate of one per administrative action.
//
//nolint:gocritic // hugeParam: Role is taken by value to match Seed's variadic
func (r *Resolver) UpsertRole(ctx context.Context, q database.SQLQueryExecutor, role authorization.Role) error {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		return op.Error(ErrNilExecutor, "upserting authorization role")
	}

	// Read through the caller's executor, not r.db: this runs inside their
	// transaction, so it must see what that transaction has already written and
	// must not reach for a second connection the pool may not have.
	existing, err := r.rolesWith(ctx, q)
	if err != nil {
		return op.Error(err, "loading existing roles")
	}

	merged := make([]authorization.Role, 0, len(existing)+1)
	for i := range existing {
		if existing[i].Name != role.Name {
			merged = append(merged, existing[i])
		}
	}
	merged = append(merged, role)

	if err = authorization.ValidateRoles(merged...); err != nil {
		return op.Error(err, "validating role %q against existing policy", role.Name)
	}

	roleIDs := make(map[string]string, len(merged))
	for i := range existing {
		e := &existing[i]
		if e.Name == role.Name {
			continue
		}
		id, lookupErr := r.lookupRoleID(ctx, q, e.Name)
		if lookupErr != nil {
			return op.Error(lookupErr, "looking up role %q", e.Name)
		}
		roleIDs[e.Name] = id
	}

	ids, err := r.resolveNamedIDs(ctx, q, rolesTable, map[string]string{role.Name: role.Description})
	if err != nil {
		return op.Error(err, "upserting role %q", role.Name)
	}
	roleIDs[role.Name] = ids[role.Name]

	if err = r.writeRoleGrants(ctx, q, roleIDs, &role); err != nil {
		return op.Error(err, "writing grants for role %q", role.Name)
	}

	return nil
}

// ArchiveRole soft-deletes a role.
//
// Archival rather than deletion, and the name stays reserved: a principal may
// still hold an assignment naming this role, and resolution simply stops
// finding it — the assignment decays to granting nothing. Freeing the name for
// reuse would instead re-grant whatever the new role holds to everyone who
// still carried the old assignment.
func (r *Resolver) ArchiveRole(ctx context.Context, q database.SQLQueryExecutor, name string) error {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		return op.Error(ErrNilExecutor, "archiving authorization role")
	}

	op.Set(keys.NameKey, name)

	if _, err := q.ExecContext(ctx, r.archiveRoleQuery(), name); err != nil {
		return op.Error(err, "archiving role %q", name)
	}

	return nil
}

// writeRoleGrants replaces a role's direct permissions and declared parents.
func (r *Resolver) writeRoleGrants(
	ctx context.Context,
	q database.SQLQueryExecutor,
	roleIDs map[string]string,
	role *authorization.Role,
) error {
	roleID := roleIDs[role.Name]

	// Clear-then-rewrite rather than diff: it makes an upsert remove grants as
	// well as add them, which a caller re-running Seed after deleting a
	// permission from a role's list is entitled to expect.
	if _, err := q.ExecContext(ctx, r.deleteRolePermissionsQuery(), roleID); err != nil {
		return platformerrors.Wrap(err, "clearing role permissions")
	}

	perms := authorization.NewPermissionSet(role.Permissions...).Slice()
	if len(perms) > 0 {
		wanted := make(map[string]string, len(perms))
		for _, perm := range perms {
			wanted[string(perm)] = ""
		}

		permIDs, err := r.resolveNamedIDs(ctx, q, permissionsTable, wanted)
		if err != nil {
			return platformerrors.Wrap(err, "upserting permissions")
		}

		pairs := make([][]any, 0, len(perms))
		for _, perm := range perms {
			pairs = append(pairs, []any{roleID, permIDs[string(perm)]})
		}

		if err = r.insertPairs(ctx, q, pairs, r.insertRolePermissionsQuery); err != nil {
			return platformerrors.Wrap(err, "granting permissions")
		}
	}

	if _, err := q.ExecContext(ctx, r.deleteRoleHierarchyQuery(), roleID); err != nil {
		return platformerrors.Wrap(err, "clearing role hierarchy")
	}

	if len(role.Inherits) > 0 {
		pairs := make([][]any, 0, len(role.Inherits))
		for _, parent := range role.Inherits {
			parentID, ok := roleIDs[parent]
			if !ok {
				var err error
				if parentID, err = r.lookupRoleID(ctx, q, parent); err != nil {
					return platformerrors.Wrapf(err, "looking up parent role %q", parent)
				}
			}
			pairs = append(pairs, []any{roleID, parentID})
		}

		if err := r.insertPairs(ctx, q, pairs, r.insertRoleHierarchyRowsQuery); err != nil {
			return platformerrors.Wrap(err, "recording inheritance")
		}
	}

	return nil
}

// resolveNamedIDs upserts a batch of roles or permissions by name and returns
// their ids.
//
// It is three statements regardless of batch size — one lookup, one multi-row
// insert of the missing, one update per row that actually changed — rather than
// two per name. Seeding a policy with a few hundred permissions is otherwise a
// few hundred round trips inside a single transaction, which is long enough to
// matter for lock hold time even though it only runs at deploy.
//
// Portability is preserved: no ON CONFLICT, no RETURNING, nothing dialect
// specific.
func (r *Resolver) resolveNamedIDs(
	ctx context.Context,
	q database.SQLQueryExecutor,
	table string,
	wanted map[string]string,
) (map[string]string, error) {
	names := slices.Sorted(maps.Keys(wanted))
	if slices.Contains(names, "") {
		return nil, platformerrors.Wrapf(platformerrors.ErrEmptyInputProvided, "%s name", table)
	}

	ids := make(map[string]string, len(names))

	type existingRow struct {
		id, description string
		archived        bool
	}
	existing := make(map[string]existingRow, len(names))

	for chunk := range slices.Chunk(names, maxBatchRows) {
		args := make([]any, 0, len(chunk))
		for _, name := range chunk {
			args = append(args, name)
		}

		rows, err := q.QueryContext(ctx, r.selectNamedByNamesQuery(table, len(chunk)), args...)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "looking up %s by name", table)
		}

		if err = scanRows(rows, func() error {
			var (
				name string
				row  existingRow
			)
			if scanErr := rows.Scan(&row.id, &name, &row.description, &row.archived); scanErr != nil {
				return scanErr
			}
			existing[name] = row

			return nil
		}); err != nil {
			return nil, platformerrors.Wrapf(err, "scanning %s rows", table)
		}
	}

	var missing []string
	for _, name := range names {
		row, found := existing[name]
		if !found {
			missing = append(missing, name)

			continue
		}

		ids[name] = row.id

		// Updated only when something actually differs. Rewriting every row on
		// every seed would churn the table and its indexes for no change, and
		// would make an audit trail on these tables useless.
		if row.description != wanted[name] || row.archived {
			if _, err := q.ExecContext(ctx, r.updateNamedQuery(table), wanted[name], row.id); err != nil {
				return nil, platformerrors.Wrapf(err, "refreshing %s %q", table, name)
			}
		}
	}

	for chunk := range slices.Chunk(missing, maxBatchRows) {
		args := make([]any, 0, len(chunk)*3)
		for _, name := range chunk {
			id := identifiers.New()
			ids[name] = id
			args = append(args, id, name, wanted[name])
		}

		if _, err := q.ExecContext(ctx, r.insertNamedRowsQuery(table, len(chunk)), args...); err != nil {
			return nil, platformerrors.Wrapf(err, "inserting %s rows", table)
		}
	}

	return ids, nil
}

// insertPairs writes two-column rows in chunks, using the supplied query
// builder.
func (r *Resolver) insertPairs(
	ctx context.Context,
	q database.SQLQueryExecutor,
	pairs [][]any,
	query func(count int) string,
) error {
	for chunk := range slices.Chunk(pairs, maxBatchRows) {
		args := make([]any, 0, len(chunk)*2)
		for _, pair := range chunk {
			args = append(args, pair...)
		}

		if _, err := q.ExecContext(ctx, query(len(chunk)), args...); err != nil {
			return err
		}
	}

	return nil
}

// lookupRoleID finds a live or archived role's id by name. Permissions are
// only ever resolved in bulk, through resolveNamedIDs, so this is deliberately
// specific to roles rather than taking a table.
func (r *Resolver) lookupRoleID(ctx context.Context, q database.SQLQueryExecutor, name string) (string, error) {
	var (
		id       string
		archived bool
	)
	if err := q.QueryRowContext(ctx, r.selectIDByNameQuery(rolesTable), name).Scan(&id, &archived); err != nil {
		return "", platformerrors.Wrapf(err, "looking up %s %q", rolesTable, name)
	}

	return id, nil
}

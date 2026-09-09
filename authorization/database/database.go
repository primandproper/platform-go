package database

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/primandproper/platform-go/v14/authorization/database/internal/authorizationdb"
	"github.com/primandproper/platform-go/v14/authorization/database/migrations"

	"github.com/primandproper/primitives-go/authorization"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/keys"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
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
	q    authorizationdb.Querier
	o11y observability.Observer

	resolutionsCounter metrics.Int64Counter
	errorsCounter      metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read r.o11y.Logger() for the logger this resolver actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	metricsProvider metrics.Provider
	tracerProvider  tracing.Provider
	prefix          string
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

	r := &Resolver{db: db, prefix: prefix}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	// The generated querier, instantiated once the prefix is settled and the
	// dialect is known — the only two things the generated statements do not
	// already carry. What executes is what sqlc analyzed, with one marker
	// substitution; see authorization/database/internal/authorizationdb.
	qd, err := querierDialect(cfg.Dialect)
	if err != nil {
		return nil, err
	}

	q, err := authorizationdb.New(qd, ddl.Qualify(prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the authorization querier")
	}

	r.q = q

	r.o11y = observability.NewObserver(serviceName, r.logger, r.tracerProvider)

	mp := metrics.EnsureMetricsProvider(r.metricsProvider)

	if r.resolutionsCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_resolutions", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating policy resolutions counter")
	}
	if r.errorsCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_errors", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating policy resolution errors counter")
	}

	return r, nil
}

// TablePrefix returns the namespace this resolver's tables carry, for a caller
// that needs the rendered names — a maintenance TRUNCATE, a schema audit. Pass
// it to migrations.Statements.
func (r *Resolver) TablePrefix() string { return r.prefix }

// querierDialect maps this module's dialect names onto the generated package's.
// The set is closed on both sides — NewResolver has already rejected anything
// d.Valid() declines — so the default arm is reachable only when this module
// learns a dialect the generated package was not generated for. That is a
// construction failure like any other, and it names the dialect, rather than
// panicking or leaning on authorizationdb.New refusing the empty string.
func querierDialect(d dialect.Dialect) (authorizationdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return authorizationdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return authorizationdb.DialectMySQL, nil
	case dialect.SQLite:
		return authorizationdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated authorization queries for dialect %q", d)
	}
}

// PermissionsForRoles resolves the named roles, expanding inheritance in SQL.
func (r *Resolver) PermissionsForRoles(ctx context.Context, roles ...string) (*authorization.PermissionSet, error) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	if len(roles) == 0 {
		return authorization.NewPermissionSet(), nil
	}

	op.Set(keys.AuthorizationRolesKey, roles)

	rows, err := r.q.ResolvePermissionsForRoles(ctx, r.db, authorizationdb.ResolvePermissionsForRolesParams{Names: roles})
	if err != nil {
		r.errorsCounter.Add(ctx, 1)

		return nil, op.Error(err, "resolving permissions for roles")
	}

	r.resolutionsCounter.Add(ctx, 1)

	perms := make([]authorization.Permission, len(rows))
	for i := range rows {
		perms[i] = authorization.Permission(rows[i].Name)
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
//
// Three reads rather than one join: the answer is two one-to-many relationships
// hanging off the same rows, so a join would return a role with four
// permissions and two parents eight times over and leave the de-duplication
// here anyway.
func (r *Resolver) rolesWith(ctx context.Context, q database.SQLQueryExecutor) ([]authorization.Role, error) {
	roleRows, err := r.q.ListRoles(ctx, q)
	if err != nil {
		return nil, platformerrors.Wrap(err, "listing roles")
	}

	permissionRows, err := r.q.ListRolePermissions(ctx, q)
	if err != nil {
		return nil, platformerrors.Wrap(err, "loading role permissions")
	}

	permsByRoleID := map[string][]string{}
	for i := range permissionRows {
		row := &permissionRows[i]
		permsByRoleID[row.RoleID] = append(permsByRoleID[row.RoleID], row.PermissionName)
	}

	parentRows, err := r.q.ListRoleHierarchy(ctx, q)
	if err != nil {
		return nil, platformerrors.Wrap(err, "loading role hierarchy")
	}

	parentsByRoleID := map[string][]string{}
	for i := range parentRows {
		row := &parentRows[i]
		parentsByRoleID[row.ChildRoleID] = append(parentsByRoleID[row.ChildRoleID], row.ParentName)
	}

	out := make([]authorization.Role, 0, len(roleRows))
	for i := range roleRows {
		rr := &roleRows[i]

		permNames := permsByRoleID[rr.ID]
		slices.Sort(permNames)

		perms := make([]authorization.Permission, len(permNames))
		for j, n := range permNames {
			perms[j] = authorization.Permission(n)
		}

		parents := parentsByRoleID[rr.ID]
		slices.Sort(parents)

		out = append(out, authorization.Role{
			Name:        rr.Name,
			Description: rr.Description,
			Permissions: perms,
			Inherits:    parents,
		})
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
//
// It is also safe to run from several processes at once, which is what "on
// every deploy" means for a service with more than one replica: each replica
// runs its migrations and then its seed, the migrator's advisory lock covers
// the schema and not the seed, and so every replica seeds the same policy at
// the same moment. Two seeds of one policy converge on it rather than failing
// one of them. The named writes converge on the name and a writer that lost
// the race for a new name reads back the id the name actually got; the grant
// writes skip a row the other seed already wrote instead of colliding with it
// on the primary key. What a loser sees is a transaction that blocks on the
// winner's row locks for the length of the winner's seed and then commits the
// same policy, not a duplicate-key error that looks like a deploy flake.
//
// The engine can still refuse one of two writers where its own locking rules
// leave it no other answer — SQLite admits one writer at a time and reports
// the second as busy, and MySQL's default isolation can declare a deadlock
// between two seeds of a role that had no grants stored yet — and what it
// reports is an error on a transaction that wrote nothing, for the caller to
// retry, never a policy half of which landed.
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

	roleIDs, err := r.resolveNamedIDs(ctx, q, r.roleTable(), wanted)
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

	ids, err := r.resolveNamedIDs(ctx, q, r.roleTable(), map[string]string{role.Name: role.Description})
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
//
// The row count is deliberately unread. Archiving a role that is already
// archived, or one that was never there, is a no-op rather than an error: the
// caller asked for the role to grant nothing and it grants nothing.
func (r *Resolver) ArchiveRole(ctx context.Context, q database.SQLQueryExecutor, name string) error {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		return op.Error(ErrNilExecutor, "archiving authorization role")
	}

	op.Set(keys.NameKey, name)

	if _, err := r.q.ArchiveRoleByName(ctx, q, authorizationdb.ArchiveRoleByNameParams{Name: name}); err != nil {
		return op.Error(err, "archiving role %q", name)
	}

	return nil
}

// writeRoleGrants replaces a role's direct permissions and declared parents.
//
// Clear-then-rewrite rather than diff: it makes an upsert remove grants as well
// as add them, which a caller re-running Seed after deleting a permission from
// a role's list is entitled to expect.
//
// The rewrite is one statement per grant. The multi-row VALUES list it replaces
// had no static text — its arity was the caller's cardinality — so there was
// nothing for sqlc to check and nothing the corpus could hold; what it costs
// instead is a round trip per grant, inside the transaction the caller already
// opened, at the cardinalities a role's permission list actually has.
//
// Each grant is written with the duplicate-skipping insert rather than the
// plain one, and the count it answers with is deliberately unread. The row it
// skips is a row another seed of the same policy wrote and committed between
// this one's DELETE and its INSERT — the concurrent case Seed documents — and
// a grant already there is the grant this seed came to write. A plain insert
// there is a primary-key collision that fails the whole transaction.
func (r *Resolver) writeRoleGrants(
	ctx context.Context,
	q database.SQLQueryExecutor,
	roleIDs map[string]string,
	role *authorization.Role,
) error {
	roleID := roleIDs[role.Name]

	if _, err := r.q.DeleteRolePermissions(ctx, q, authorizationdb.DeleteRolePermissionsParams{RoleID: roleID}); err != nil {
		return platformerrors.Wrap(err, "clearing role permissions")
	}

	perms := authorization.NewPermissionSet(role.Permissions...).Slice()
	if len(perms) > 0 {
		wanted := make(map[string]string, len(perms))
		for _, perm := range perms {
			wanted[string(perm)] = ""
		}

		permIDs, err := r.resolveNamedIDs(ctx, q, r.permissionTable(), wanted)
		if err != nil {
			return platformerrors.Wrap(err, "upserting permissions")
		}

		for _, perm := range perms {
			if _, err = r.q.CreateRolePermission(ctx, q, authorizationdb.CreateRolePermissionParams{
				RoleID:       roleID,
				PermissionID: permIDs[string(perm)],
			}); err != nil {
				return platformerrors.Wrapf(err, "granting permission %q", perm)
			}
		}
	}

	if _, err := r.q.DeleteRoleHierarchy(ctx, q, authorizationdb.DeleteRoleHierarchyParams{ChildRoleID: roleID}); err != nil {
		return platformerrors.Wrap(err, "clearing role hierarchy")
	}

	for _, parent := range role.Inherits {
		parentID, ok := roleIDs[parent]
		if !ok {
			var err error
			if parentID, err = r.lookupRoleID(ctx, q, parent); err != nil {
				return platformerrors.Wrapf(err, "looking up parent role %q", parent)
			}
		}

		if _, err := r.q.CreateRoleHierarchyEdge(ctx, q, authorizationdb.CreateRoleHierarchyEdgeParams{
			ChildRoleID:  roleID,
			ParentRoleID: parentID,
		}); err != nil {
			return platformerrors.Wrapf(err, "recording inheritance from %q", parent)
		}
	}

	return nil
}

// namedRow is what a lookup of one of the two named tables answers with, in the
// one shape both of them return it in.
//
// The generated row types are the same fields under two names — a role and a
// permission are both "a name an operator gave something, with prose beside it"
// — and reconciling a wanted policy against what is stored is one piece of
// reasoning rather than two, so it reads one type rather than being written out
// once per table.
type namedRow struct {
	id, description string
	archived        bool
}

// namedTable is one of the two tables whose rows are a name with prose beside
// it, as resolveNamedIDs needs it: what to call it in an error, how to look a
// batch of names up, and how to write one back.
//
// It is a struct of closures rather than an interface because the tables differ
// only in which generated method answers for them. What is shared is the part
// that can be got wrong twice — when a row is written, which id is bound, and
// what an archived row means — and that lives in resolveNamedIDs alone.
type namedTable struct {
	lookup func(ctx context.Context, q database.SQLQueryExecutor, names []string) (map[string]namedRow, error)
	upsert func(ctx context.Context, q database.SQLQueryExecutor, id, name, description string) error
	name   string
}

// roleTable and permissionTable bind the generated methods for each of the two.
func (r *Resolver) roleTable() namedTable {
	return namedTable{
		name: "role",
		lookup: func(ctx context.Context, q database.SQLQueryExecutor, names []string) (map[string]namedRow, error) {
			rows, err := r.q.ListRolesByNames(ctx, q, authorizationdb.ListRolesByNamesParams{Names: names})
			if err != nil {
				return nil, err
			}

			found := make(map[string]namedRow, len(rows))
			for i := range rows {
				row := &rows[i]
				found[row.Name] = namedRow{id: row.ID, description: row.Description, archived: row.ArchivedAt != nil}
			}

			return found, nil
		},
		upsert: func(ctx context.Context, q database.SQLQueryExecutor, id, name, description string) error {
			return r.q.UpsertRole(ctx, q, authorizationdb.UpsertRoleParams{ID: id, Name: name, Description: description})
		},
	}
}

func (r *Resolver) permissionTable() namedTable {
	return namedTable{
		name: "permission",
		lookup: func(ctx context.Context, q database.SQLQueryExecutor, names []string) (map[string]namedRow, error) {
			rows, err := r.q.ListPermissionsByNames(ctx, q, authorizationdb.ListPermissionsByNamesParams{Names: names})
			if err != nil {
				return nil, err
			}

			found := make(map[string]namedRow, len(rows))
			for i := range rows {
				row := &rows[i]
				found[row.Name] = namedRow{id: row.ID, description: row.Description, archived: row.ArchivedAt != nil}
			}

			return found, nil
		},
		upsert: func(ctx context.Context, q database.SQLQueryExecutor, id, name, description string) error {
			return r.q.UpsertPermission(ctx, q, authorizationdb.UpsertPermissionParams{ID: id, Name: name, Description: description})
		},
	}
}

// resolveNamedIDs writes a batch of roles or permissions by name and returns
// their ids.
//
// One lookup for the whole batch, then a write for each row that is actually
// missing or actually different. Seeding a policy with a few hundred
// permissions is otherwise a lookup and a write per name inside a single
// transaction, which is long enough to matter for lock hold time even though it
// only runs at deploy.
//
// The lookup is what supplies the id, and it has to: the write converges on the
// name, and only Postgres could return the id of the row it converged on. So an
// existing name is written back under the id it already has — binding a fresh
// one would leave this holding an id no row carries, since MySQL's ON DUPLICATE
// KEY UPDATE resolves the collision on whichever unique key it hit — and only a
// name nothing was found for is minted an id.
//
// A minted id is provisional until the write lands, and the names that were
// minted one are looked up a second time once they have. Between the lookup and
// the write another seed of the same policy can insert the same name and
// commit — the concurrent case Seed documents — and this writer's upsert then
// converges on that row, under that row's id, exactly as it would on any name
// already taken. Without the second lookup this writer goes on holding the id
// it minted, which no row carries, and every grant it writes for that role is
// a foreign-key violation. The second lookup costs one batched read, only on a
// seed that actually created something, and answers with whichever id the name
// ended up under: this writer's own where it won, the other's where it lost.
// Each dialect makes that row visible to it. SQLite admits one writer at a
// time, so the row is this writer's own; Postgres at its default isolation
// gives every statement a fresh snapshot, and at a stricter one the converging
// write is itself the serialization failure, so the second lookup never runs
// against a stale view; and on MySQL the converging write stamps
// last_updated_at onto a row that had none, so the row is one this transaction
// modified and its snapshot shows it. A name still missing after its own write
// is therefore not a race but a broken invariant, and reported as one rather
// than left to fail as a foreign key.
//
// A row is written only when something actually differs. Rewriting every row on
// every seed would churn the table and its indexes for no change, and would make
// an audit trail on these tables useless.
func (r *Resolver) resolveNamedIDs(
	ctx context.Context,
	q database.SQLQueryExecutor,
	table namedTable,
	wanted map[string]string,
) (map[string]string, error) {
	names := slices.Sorted(maps.Keys(wanted))
	if slices.Contains(names, "") {
		return nil, platformerrors.Wrapf(platformerrors.ErrEmptyInputProvided, "%s name", table.name)
	}

	existing, err := table.lookup(ctx, q, names)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "looking up %ss by name", table.name)
	}

	ids := make(map[string]string, len(names))

	var minted []string

	for _, name := range names {
		row, found := existing[name]
		if found {
			ids[name] = row.id

			// An archived row is revived rather than left alone: the name stays
			// reserved once used, so a re-seed after an archival has to bring
			// the row back rather than insert a colliding one.
			if row.description == wanted[name] && !row.archived {
				continue
			}
		} else {
			ids[name] = identifiers.New()
			minted = append(minted, name)
		}

		if err = table.upsert(ctx, q, ids[name], name, wanted[name]); err != nil {
			return nil, platformerrors.Wrapf(err, "writing %s %q", table.name, name)
		}
	}

	if len(minted) == 0 {
		return ids, nil
	}

	landed, err := table.lookup(ctx, q, minted)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "reading back the %ss written by name", table.name)
	}

	for _, name := range minted {
		row, found := landed[name]
		if !found {
			return nil, platformerrors.Wrapf(ErrWrittenNameMissing, "%s %q", table.name, name)
		}

		ids[name] = row.id
	}

	return ids, nil
}

// lookupRoleID finds a live or archived role's id by name. Permissions are
// only ever resolved in bulk, through resolveNamedIDs, so this is deliberately
// specific to roles rather than taking a table.
func (r *Resolver) lookupRoleID(ctx context.Context, q database.SQLQueryExecutor, name string) (string, error) {
	row, err := r.q.GetRoleIDByName(ctx, q, authorizationdb.GetRoleIDByNameParams{Name: name})
	if err != nil {
		return "", platformerrors.Wrapf(err, "looking up role %q", name)
	}

	return row.ID, nil
}

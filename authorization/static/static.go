// Package static provides an authorization.PolicyResolver whose policy is
// fixed at construction.
//
// This is the default backend, and the one to reach for until something forces
// otherwise: it needs no database, no migrations, and no configuration, and it
// resolves without I/O. Policy lives wherever the caller declares its roles —
// as Go constants, or as YAML loaded into a config struct.
//
// Graduate to authorization/database when roles themselves must become editable
// data: when an operator has to define a new role, or change what an existing
// one grants, without shipping a release. Reassigning a principal's roles does
// not require it — role assignments belong to the consumer either way.
package static

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/primandproper/platform-go/v13/authorization"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
)

var _ authorization.PolicyResolver = (*Resolver)(nil)

// Resolver resolves role names against a policy fixed at construction.
type Resolver struct {
	logger   logging.Logger
	expanded map[string]*authorization.PermissionSet
	memo     sync.Map
	roles    []authorization.Role
}

// Option configures a Resolver.
type Option func(*Resolver)

// WithLogger attaches a logger, which the Resolver uses once at construction to
// report a policy that will deny everything, and never again — resolution
// itself logs nothing.
func WithLogger(logger logging.Logger) Option {
	return func(r *Resolver) {
		r.logger = logger
	}
}

// NewResolver builds a Resolver over roles, expanding inheritance once.
//
// It returns an error for a malformed policy — an unnamed role, a duplicate, a
// parent that is not defined, or an inheritance cycle — so that a policy
// mistake fails at startup rather than as a puzzling denial later.
//
// Zero roles is valid and produces a resolver that denies everything. That is
// deliberate: the zero-value configuration has to build, or the default
// provider would not be usable without setup. It says so at info level, because
// a service that denies every request is more likely to be a missing
// configuration than an intent.
func NewResolver(roles []authorization.Role, opts ...Option) (*Resolver, error) {
	expanded, err := authorization.ExpandInheritance(roles...)
	if err != nil {
		return nil, err
	}

	r := &Resolver{
		expanded: expanded,
		roles:    slices.Clone(roles),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	r.logger = logging.EnsureLogger(r.logger).WithName("authorization_static")
	if len(roles) == 0 {
		r.logger.Info("static policy resolver constructed with no roles; all authorization checks will deny")
	}

	return r, nil
}

// PermissionsForRoles returns the union of the effective permissions of the
// named roles. It never returns an error: the policy is already validated and
// resolution touches nothing outside this process.
func (r *Resolver) PermissionsForRoles(_ context.Context, roles ...string) (*authorization.PermissionSet, error) {
	return r.permissionsForRoles(roles...), nil
}

// permissionsForRoles is the error-free core, so that callers inside this
// package — and benchmarks — need not discard a nil error.
func (r *Resolver) permissionsForRoles(roles ...string) *authorization.PermissionSet {
	switch len(roles) {
	case 0:
		return authorization.NewPermissionSet()
	case 1:
		// The overwhelmingly common case, and it needs neither a union nor a
		// memo entry: the expansion map already holds exactly this answer.
		if set, ok := r.expanded[roles[0]]; ok {
			return set
		}

		return authorization.NewPermissionSet()
	}

	// Unknown roles contribute nothing rather than erroring, so a principal
	// still assigned a role the policy has since dropped loses that authority
	// instead of losing the ability to make requests.
	//
	// They are also filtered out *before* the memo key is built. The role names
	// come from the caller — in most deployments, from a token an attacker can
	// influence — and the memo is unbounded and lives for the process's lifetime,
	// so keying on the raw list is a memory-growth primitive: request N distinct
	// junk role names, get N permanent entries. Filtering first bounds the key
	// space by the policy's own role set.
	known := make([]string, 0, len(roles))
	sets := make([]*authorization.PermissionSet, 0, len(roles))

	for _, name := range roles {
		if set, ok := r.expanded[name]; ok {
			known = append(known, name)
			sets = append(sets, set)
		}
	}

	key, err := memoKey(known)
	if err != nil {
		// A NUL in a role name would make two different role lists collide on one
		// key — and a collision here hands one principal another's permissions.
		// Refuse to memoize rather than answer from a poisoned entry.
		return authorization.NewPermissionSet().Union(sets...)
	}

	if got, ok := r.memo.Load(key); ok {
		if set, isSet := got.(*authorization.PermissionSet); isSet {
			return set
		}
	}

	union := authorization.NewPermissionSet().Union(sets...)
	r.memo.Store(key, union)

	return union
}

// Roles returns every role in the policy. The returned value shares nothing
// with the Resolver's state, so a caller cannot edit the live policy through
// it.
func (r *Resolver) Roles(context.Context) ([]authorization.Role, error) {
	out := make([]authorization.Role, 0, len(r.roles))
	for i := range r.roles {
		clone := r.roles[i]
		clone.Permissions = slices.Clone(r.roles[i].Permissions)
		clone.Inherits = slices.Clone(r.roles[i].Inherits)
		out = append(out, clone)
	}

	return out, nil
}

// memoKey builds a stable key for a set of role names. It sorts a copy so that
// the same roles in a different order hit the same entry, and joins on NUL
// because a role name cannot usefully contain one.
//
// That "cannot" is checked rather than assumed: a name containing a NUL would
// let {"a\x00b"} and {"a", "b"} produce the same key, and a collision here
// grants one principal another's permissions.
func memoKey(roles []string) (string, error) {
	sorted := slices.Clone(roles)
	slices.Sort(sorted)

	for _, name := range sorted {
		if strings.ContainsRune(name, 0) {
			return "", platformerrors.Newf("role name %q contains a NUL byte", name)
		}
	}

	return strings.Join(sorted, "\x00"), nil
}

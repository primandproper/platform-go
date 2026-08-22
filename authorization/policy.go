package authorization

import (
	"context"
	"maps"
	"slices"

	"github.com/primandproper/platform-go/v13/errors"
)

// Role is a named grant of permissions, optionally inheriting from other roles.
//
// The same []Role value seeds either backend: authorization/static compiles it
// in, and authorization/database.Seed writes it to tables. That is what makes
// the two interchangeable, and it is the fix for the failure mode where a
// code-side role table and a database seed drift apart because nothing checks
// them against each other.
type Role struct {
	// Name identifies the role. It is the string a principal's role assignments
	// refer to, and it must be unique within a policy.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	// Description is human-facing documentation, surfaced by Roles for admin
	// tooling. It has no effect on resolution.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// Permissions are the permissions this role grants directly, before
	// inheritance is applied.
	Permissions []Permission `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	// Inherits names the roles this role inherits from. Inheritance is
	// transitive: a role receives the permissions of its parents, its parents'
	// parents, and so on. It is not an ordering — a role may inherit from
	// several, and the result is their union.
	Inherits []string `json:"inherits,omitempty" yaml:"inherits,omitempty"`
}

// PolicyResolver answers "what can these roles do".
//
// This is the only fallible, context-taking part of the package, and the only
// one with more than one implementation — which is the whole design. Resolving
// policy may hit a database; checking a permission never does. Callers resolve
// once when they build a session and check many times per request against the
// resulting Grants.
//
// Implementations must be safe for concurrent use.
type PolicyResolver interface {
	// PermissionsForRoles returns the effective permissions of the named roles
	// with inheritance expanded, which is the union of what each role grants.
	// Unknown role names contribute nothing rather than erroring: a policy that
	// no longer defines a role a principal is still assigned must fail closed,
	// not fail the request. Use Roles to detect that case deliberately.
	//
	// Calling it with no roles returns an empty set, not an error.
	PermissionsForRoles(ctx context.Context, roles ...string) (*PermissionSet, error)

	// Roles returns every role the policy defines, for introspection and admin
	// tooling. The order is unspecified.
	Roles(ctx context.Context) ([]Role, error)
}

// PolicyInvalidator is the optional half of a PolicyResolver that memoizes.
//
// Only authorization/cached implements it; the other backends hold nothing to
// drop. It is declared here, apart from PolicyResolver, so that a caller wired
// through authorizationcfg can reach invalidation without knowing which
// concrete type it was handed — whether a cache sits in the chain is a
// configuration decision, and the position of the cached decorator inside the
// returned resolver is an implementation detail:
//
//	if inv, ok := resolver.(authorization.PolicyInvalidator); ok {
//		inv.InvalidateAll()
//	}
//
// The process that edits policy is exactly the one that needs this, and it is
// the one least likely to know how its resolver was assembled.
type PolicyInvalidator interface {
	// Invalidate drops the memoized resolution for an exact set of roles.
	Invalidate(ctx context.Context, roles ...string) error

	// InvalidateAll makes every resolution this instance memoized unreachable.
	// It is process-local: other replicas wait out their TTL.
	InvalidateAll()
}

var (
	// ErrEmptyRoleName indicates a role was declared without a name.
	ErrEmptyRoleName = errors.New("role name is empty")
	// ErrDuplicateRole indicates the same role name was declared twice.
	ErrDuplicateRole = errors.New("duplicate role name")
	// ErrUnknownParentRole indicates a role inherits from a role that is not defined.
	ErrUnknownParentRole = errors.New("role inherits from an unknown role")
	// ErrInheritanceCycle indicates role inheritance forms a cycle.
	ErrInheritanceCycle = errors.New("role inheritance cycle")
	// ErrSelfInheritance indicates a role names itself in its Inherits list.
	ErrSelfInheritance = errors.New("role inherits from itself")
)

// ValidateRoles reports whether roles form a well-formed policy: every role
// named, no duplicates, every parent defined, and no inheritance cycles.
//
// Both backends call it, so a policy rejected in a static build is rejected on
// its way into the database too.
func ValidateRoles(roles ...Role) error {
	// Indexed rather than ranged by value throughout: Role carries two slices
	// and two strings, so copying one per iteration is measurable on a policy
	// of any size.
	byName := make(map[string]Role, len(roles))
	for i := range roles {
		r := &roles[i]
		if r.Name == "" {
			return ErrEmptyRoleName
		}
		if _, exists := byName[r.Name]; exists {
			return errors.Wrapf(ErrDuplicateRole, "role %q", r.Name)
		}
		byName[r.Name] = *r
	}

	for i := range roles {
		r := &roles[i]
		for _, parent := range r.Inherits {
			if parent == r.Name {
				return errors.Wrapf(ErrSelfInheritance, "role %q", r.Name)
			}
			if _, ok := byName[parent]; !ok {
				return errors.Wrapf(ErrUnknownParentRole, "role %q inherits %q", r.Name, parent)
			}
		}
	}

	return detectCycles(byName)
}

// ExpandInheritance resolves every role to its effective permission set, with
// inheritance applied transitively. It validates first, so a malformed policy
// is rejected here rather than producing a partially-expanded result.
//
// It is the reference semantics for inheritance. authorization/static uses it
// directly; authorization/database expands in SQL instead, and a test asserts
// the two agree — which is the only way to know the recursive CTE and this
// function mean the same thing.
func ExpandInheritance(roles ...Role) (map[string]*PermissionSet, error) {
	if err := ValidateRoles(roles...); err != nil {
		return nil, err
	}

	byName := make(map[string]Role, len(roles))
	for i := range roles {
		byName[roles[i].Name] = roles[i]
	}

	expanded := make(map[string]*PermissionSet, len(roles))

	var resolve func(name string) *PermissionSet
	resolve = func(name string) *PermissionSet {
		if got, ok := expanded[name]; ok {
			return got
		}

		role := byName[name]
		set := NewPermissionSet(role.Permissions...)

		// Marked before recursing so a cycle would terminate here. Cycles are
		// already rejected by ValidateRoles; this keeps the function total
		// rather than relying on that from a distance.
		expanded[name] = set

		for _, parent := range role.Inherits {
			set = set.Union(resolve(parent))
			expanded[name] = set
		}

		return set
	}

	for i := range roles {
		resolve(roles[i].Name)
	}

	return expanded, nil
}

// detectCycles walks the inheritance graph depth-first, reporting the first
// cycle it finds.
func detectCycles(byName map[string]Role) error {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)

	state := make(map[string]int, len(byName))

	var walk func(name string, path []string) error
	walk = func(name string, path []string) error {
		switch state[name] {
		case visiting:
			return errors.Wrapf(ErrInheritanceCycle, "%s", append(path, name))
		case visited:
			return nil
		}

		state[name] = visiting
		path = append(path, name)
		for _, parent := range byName[name].Inherits {
			if err := walk(parent, path); err != nil {
				return err
			}
		}
		state[name] = visited

		return nil
	}

	// Sorted so that a policy with more than one cycle reports the same one
	// every run; an error that moves between runs is miserable to act on.
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		if err := walk(name, nil); err != nil {
			return err
		}
	}

	return nil
}

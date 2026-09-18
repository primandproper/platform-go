package rbac

import (
	"errors"
	"testing"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testPolicy is testRoles as the single declared value a consumer holds.
func testPolicy() Policy {
	return Policy{Roles: testRoles()}
}

// seedPolicy writes a policy through a transaction, the way the package
// documentation's two call sites both do.
func seedPolicy(t *testing.T, r *Resolver, client database.Client, policy Policy) {
	t.Helper()

	must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
		return r.SeedPolicy(t.Context(), q, policy)
	}))
}

// roleIDs reads the id every named role currently carries, which is what makes
// "the same rows" assertable rather than only "the same policy": an idempotent
// seed converges on the row that is already there instead of replacing it.
func roleIDs(t *testing.T, r *Resolver, client database.Client, policy Policy) map[string]string {
	t.Helper()

	out := make(map[string]string, len(policy.Roles))
	for i := range policy.Roles {
		id, err := r.lookupRoleID(t.Context(), client.Reader(), policy.Roles[i].Name)
		must.NoError(t, err)
		out[policy.Roles[i].Name] = id
	}

	return out
}

func TestResolver_SeedPolicy(T *testing.T) {
	T.Parallel()

	// The acceptance the ruling asks for: running it twice leaves the same
	// rows. Both halves are asserted — the policy that reads back and the role
	// rows it reads back from — because a seed that dropped and rewrote every
	// role on each run would satisfy the first and none of what idempotence is
	// for.
	T.Run("is idempotent", func(t *testing.T) {
		t.Parallel()

		policy := testPolicy()

		r, client := newTestResolver(t)
		seedPolicy(t, r, client, policy)

		first, err := r.Roles(t.Context())
		must.NoError(t, err)
		firstIDs := roleIDs(t, r, client, policy)

		seedPolicy(t, r, client, policy)

		second, err := r.Roles(t.Context())
		must.NoError(t, err)

		test.Eq(t, first, second)
		test.Eq(t, firstIDs, roleIDs(t, r, client, policy))
	})

	T.Run("writes the declared grants", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seedPolicy(t, r, client, testPolicy())

		set, err := r.PermissionsForRoles(t.Context(), "service_admin")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead, permWrite, permDelete)))
	})

	// The declaration is the whole policy, so the value a consumer hands over
	// has to mean what the same roles mean through Seed.
	T.Run("agrees with seeding the same roles", func(t *testing.T) {
		t.Parallel()

		viaPolicy, policyClient := newTestResolver(t)
		seedPolicy(t, viaPolicy, policyClient, testPolicy())

		viaSeed, seedClient := newTestResolver(t)
		seed(t, viaSeed, seedClient, testRoles()...)

		fromPolicy, err := viaPolicy.Roles(t.Context())
		must.NoError(t, err)

		fromSeed, err := viaSeed.Roles(t.Context())
		must.NoError(t, err)

		test.Eq(t, fromSeed, fromPolicy)
	})

	T.Run("validates before writing anything", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)

		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			return r.SeedPolicy(t.Context(), q, Policy{Roles: []authorization.Role{
				{Name: "a", Inherits: []string{"b"}},
				{Name: "b", Inherits: []string{"a"}},
			}})
		})

		test.True(t, errors.Is(err, authorization.ErrInheritanceCycle))

		roles, err := r.Roles(t.Context())
		must.NoError(t, err)
		test.SliceEmpty(t, roles)
	})

	T.Run("rejects a nil executor", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestResolver(t)

		test.True(t, errors.Is(r.SeedPolicy(t.Context(), nil, testPolicy()), ErrNilExecutor))
	})

	T.Run("a policy declaring nothing is a no-op", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seedPolicy(t, r, client, Policy{})

		roles, err := r.Roles(t.Context())
		must.NoError(t, err)
		test.SliceEmpty(t, roles)
	})

	// Same clear-then-rewrite Seed documents, reached through the declaration:
	// a release that drops a permission from a role's list is a release that
	// revokes it.
	T.Run("revokes a grant the new declaration drops", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)

		seedPolicy(t, r, client, Policy{Roles: []authorization.Role{
			{Name: "member", Permissions: []authorization.Permission{permRead, permWrite}},
		}})
		seedPolicy(t, r, client, Policy{Roles: []authorization.Role{
			{Name: "member", Permissions: []authorization.Permission{permRead}},
		}})

		set, err := r.PermissionsForRoles(t.Context(), "member")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead)))
	})
}

func TestPolicy_Validate(T *testing.T) {
	T.Parallel()

	T.Run("accepts a well-formed policy", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, testPolicy().Validate())
	})

	T.Run("accepts a policy declaring nothing", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, Policy{}.Validate())
	})

	// The point of exporting it: a consumer's own test catches this, rather
	// than the deploy that first runs the seed.
	T.Run("rejects what SeedPolicy would reject", func(t *testing.T) {
		t.Parallel()

		policy := Policy{Roles: []authorization.Role{
			{Name: "a", Inherits: []string{"b"}},
			{Name: "b", Inherits: []string{"a"}},
		}}

		test.True(t, errors.Is(policy.Validate(), authorization.ErrInheritanceCycle))

		r, client := newTestResolver(t)
		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			return r.SeedPolicy(t.Context(), q, policy)
		})
		test.True(t, errors.Is(err, authorization.ErrInheritanceCycle))
	})

	T.Run("rejects an unnamed role", func(t *testing.T) {
		t.Parallel()

		policy := Policy{Roles: []authorization.Role{{Permissions: []authorization.Permission{permRead}}}}

		test.True(t, errors.Is(policy.Validate(), authorization.ErrEmptyRoleName))
	})
}

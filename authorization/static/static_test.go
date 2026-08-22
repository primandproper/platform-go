package static

import (
	"errors"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v13/authorization"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const (
	permRead   authorization.Permission = "read.things"
	permWrite  authorization.Permission = "write.things"
	permDelete authorization.Permission = "delete.things"
)

func testRoles() []authorization.Role {
	return []authorization.Role{
		{Name: "member", Description: "a member", Permissions: []authorization.Permission{permRead}},
		{Name: "admin", Permissions: []authorization.Permission{permWrite}, Inherits: []string{"member"}},
		{Name: "auditor", Permissions: []authorization.Permission{permDelete}},
	}
}

func newTestResolver(t *testing.T) *Resolver {
	t.Helper()

	r, err := NewResolver(testRoles())
	must.NoError(t, err)

	return r
}

func TestNewResolver(T *testing.T) {
	T.Parallel()

	// The default provider has to build from a zero-value config, or "most
	// accessible implementation is the default" is not true in practice.
	T.Run("no roles is valid and denies everything", func(t *testing.T) {
		t.Parallel()

		r, err := NewResolver(nil)
		must.NoError(t, err)

		set, err := r.PermissionsForRoles(t.Context(), "anything")
		must.NoError(t, err)

		test.True(t, set.IsEmpty())
	})

	T.Run("rejects a malformed policy", func(t *testing.T) {
		t.Parallel()

		r, err := NewResolver([]authorization.Role{
			{Name: "a", Inherits: []string{"b"}},
			{Name: "b", Inherits: []string{"a"}},
		})

		test.Nil(t, r)
		test.True(t, errors.Is(err, authorization.ErrInheritanceCycle))
	})

	T.Run("does not retain the caller's slice", func(t *testing.T) {
		t.Parallel()

		roles := testRoles()
		r, err := NewResolver(roles)
		must.NoError(t, err)

		roles[0].Name = "tampered"

		got, err := r.Roles(t.Context())
		must.NoError(t, err)

		names := make([]string, 0, len(got))
		for _, role := range got {
			names = append(names, role.Name)
		}
		test.SliceContains(t, names, "member")
		test.SliceNotContains(t, names, "tampered")
	})
}

func TestResolver_PermissionsForRoles(T *testing.T) {
	T.Parallel()

	T.Run("expands inheritance", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t)

		set, err := r.PermissionsForRoles(t.Context(), "admin")
		must.NoError(t, err)

		test.True(t, set.HasAll(permRead, permWrite))
		test.False(t, set.Has(permDelete))
	})

	T.Run("unions several roles", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t)

		set, err := r.PermissionsForRoles(t.Context(), "member", "auditor")
		must.NoError(t, err)

		test.True(t, set.HasAll(permRead, permDelete))
		test.False(t, set.Has(permWrite))
	})

	T.Run("role order does not matter", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t)

		forward, err := r.PermissionsForRoles(t.Context(), "member", "auditor")
		must.NoError(t, err)

		backward, err := r.PermissionsForRoles(t.Context(), "auditor", "member")
		must.NoError(t, err)

		test.True(t, forward.Equal(backward))
	})

	// Fail closed rather than fail loud: a principal still assigned a role the
	// policy has dropped should lose that authority, not lose the ability to
	// make requests.
	T.Run("unknown roles contribute nothing", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t)

		set, err := r.PermissionsForRoles(t.Context(), "member", "ghost")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead)))
	})

	T.Run("only unknown roles yields an empty set", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t)

		set, err := r.PermissionsForRoles(t.Context(), "ghost")
		must.NoError(t, err)

		test.True(t, set.IsEmpty())
	})

	T.Run("no roles yields an empty set", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t)

		set, err := r.PermissionsForRoles(t.Context())
		must.NoError(t, err)

		test.True(t, set.IsEmpty())
	})

	T.Run("memoized results are consistent under concurrency", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t)
		want := authorization.NewPermissionSet(permRead, permWrite, permDelete)

		var wg sync.WaitGroup
		for range 32 {
			wg.Go(func() {
				set, err := r.PermissionsForRoles(t.Context(), "admin", "auditor")
				if err != nil || !set.Equal(want) {
					t.Error("concurrent resolution disagreed")
				}
			})
		}
		wg.Wait()
	})
}

func TestResolver_Roles(T *testing.T) {
	T.Parallel()

	T.Run("reports the declared policy", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t)

		got, err := r.Roles(t.Context())
		must.NoError(t, err)

		test.SliceLen(t, 3, got)

		byName := map[string]authorization.Role{}
		for _, role := range got {
			byName[role.Name] = role
		}

		test.EqOp(t, "a member", byName["member"].Description)
		test.Eq(t, []string{"member"}, byName["admin"].Inherits)
		test.Eq(t, []authorization.Permission{permWrite}, byName["admin"].Permissions)
	})

	// Roles hands out the policy; it must not hand out a handle to it.
	T.Run("returned roles do not alias resolver state", func(t *testing.T) {
		t.Parallel()

		r := newTestResolver(t)

		first, err := r.Roles(t.Context())
		must.NoError(t, err)

		for i := range first {
			if len(first[i].Permissions) > 0 {
				first[i].Permissions[0] = "tampered"
			}
			if len(first[i].Inherits) > 0 {
				first[i].Inherits[0] = "tampered"
			}
		}

		second, err := r.Roles(t.Context())
		must.NoError(t, err)

		for _, role := range second {
			test.SliceNotContains(t, role.Permissions, authorization.Permission("tampered"))
			test.SliceNotContains(t, role.Inherits, "tampered")
		}
	})
}

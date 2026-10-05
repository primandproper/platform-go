package grpc_test

import (
	"context"
	"testing"

	identitygrpc "github.com/primandproper/platform-go/v15/identity/grpc"
	"github.com/primandproper/platform-go/v15/identity/identitypb"
	"github.com/primandproper/platform-go/v15/rbac"

	"github.com/primandproper/primitives-go/v2/authorization"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The two resolvers a deployment is expected to hand over, as they are.
var (
	_ identitygrpc.PermissionResolver = (*rbac.Resolver)(nil)
	_ identitygrpc.PermissionResolver = authorization.PolicyResolver(nil)
)

// rolePolicy is a role policy small enough to read: each role grants what its
// entry lists, and an unknown role grants nothing.
type rolePolicy map[string][]authorization.Permission

func (p rolePolicy) PermissionsForRoles(_ context.Context, roles ...string) (*authorization.PermissionSet, error) {
	var granted []authorization.Permission
	for _, role := range roles {
		granted = append(granted, p[role]...)
	}

	return authorization.NewPermissionSet(granted...), nil
}

// failingPolicy is a role policy whose store is down.
type failingPolicy struct{}

func (failingPolicy) PermissionsForRoles(context.Context, ...string) (*authorization.PermissionSet, error) {
	return nil, platformerrors.New("the role table is unreachable")
}

var testRolePolicy = rolePolicy{
	"operator": {"ops.users.read"},
	"owner":    {"account.manage", "account.read"},
	"member":   {"account.read"},
}

// principalPermissions reads GetPrincipal as caller, and fails the test when
// the field is absent.
func principalPermissions(t *testing.T, h *harness, caller *sessionPrincipal, activeAccountID string) []string {
	t.Helper()

	request := &identitypb.GetPrincipalRequest{}
	if activeAccountID != "" {
		request.ActiveAccountId = &activeAccountID
	}

	response, err := h.client.GetPrincipal(h.as(caller), request)
	must.NoError(t, err)
	must.NotNil(t, response.GetPermissions(),
		must.Sprint("a server built with a resolver answered with no permissions field"))

	return response.GetPermissions().GetPermissions()
}

func TestGetPrincipalPermissions(T *testing.T) {
	T.Parallel()

	T.Run("service-role and membership permissions are unioned", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, identitygrpc.WithPermissionResolver(testRolePolicy))
		registration := h.seedAccount(t, testScope, "somebody")

		caller := &sessionPrincipal{
			userID: registration.User.ID, scope: testScope,
			serviceRoles: []string{"operator"},
		}

		test.Eq(t, []string{"account.manage", "account.read", "ops.users.read"},
			principalPermissions(t, h, caller, ""))
	})

	T.Run("a named account other than the session's gets that account's permissions", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, identitygrpc.WithPermissionResolver(testRolePolicy))
		mine := h.seedAccount(t, testScope, "somebody")
		theirs := h.seedAccount(t, testScope, "neighbor")
		h.seedMembership(t, testScope, mine.User.ID, theirs.Account.ID, "member")

		// The session is against the caller's own account, where they are the
		// owner; the request asks about the one they are a member of.
		caller := &sessionPrincipal{
			userID: mine.User.ID, scope: testScope, activeAccountID: mine.Account.ID,
		}

		// The control: the session's own account answers with the owner's.
		test.Eq(t, []string{"account.manage", "account.read"}, principalPermissions(t, h, caller, ""))

		test.Eq(t, []string{"account.read"}, principalPermissions(t, h, caller, theirs.Account.ID),
			test.Sprint("the named account was answered with the session account's permissions"))
	})

	T.Run("a caller with no membership gets service-role permissions only", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, identitygrpc.WithPermissionResolver(testRolePolicy))
		user := h.seedUser(t, testScope, "nobody")

		caller := &sessionPrincipal{
			userID: user.ID, scope: testScope,
			serviceRoles: []string{"operator"},
		}

		test.Eq(t, []string{"ops.users.read"}, principalPermissions(t, h, caller, ""))
	})

	T.Run("a caller who may do nothing gets an empty field rather than none", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, identitygrpc.WithPermissionResolver(testRolePolicy))
		user := h.seedUser(t, testScope, "nobody")

		caller := &sessionPrincipal{userID: user.ID, scope: testScope}

		test.SliceEmpty(t, principalPermissions(t, h, caller, ""))
	})

	T.Run("the service half follows the session's door, not the directory", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, identitygrpc.WithPermissionResolver(testRolePolicy))
		registration := h.seedAccount(t, testScope, "administrator")

		_, err := h.client.SetUserServiceRoles(h.ctx(), &identitypb.SetUserServiceRolesRequest{
			UserId: registration.User.ID,
			Roles:  []string{"operator"},
		})
		must.NoError(t, err)

		principal := testPrincipal{userID: registration.User.ID, scope: testScope}

		// signin/grpc's extractor leaves an ordinary-door session none of the
		// service roles the directory holds, and an administrative one all of
		// them.
		ordinary := &sessionPrincipal{testPrincipal: principal}
		administrative := &sessionPrincipal{testPrincipal: principal, serviceRoles: []string{"operator"}}

		test.Eq(t, []string{"account.manage", "account.read"}, principalPermissions(t, h, ordinary, ""),
			test.Sprint("an ordinary-door session was told it holds its user's operator permissions"))
		test.Eq(t, []string{"account.manage", "account.read", "ops.users.read"}, principalPermissions(t, h, administrative, ""))
	})

	T.Run("a principal carrying no identity contributes no service roles", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, identitygrpc.WithPermissionResolver(testRolePolicy))
		registration := h.seedAccount(t, testScope, "administrator")

		_, err := h.client.SetUserServiceRoles(h.ctx(), &identitypb.SetUserServiceRolesRequest{
			UserId: registration.User.ID,
			Roles:  []string{"operator"},
		})
		must.NoError(t, err)

		response, err := h.client.GetPrincipal(
			h.as(&testPrincipal{userID: registration.User.ID, scope: testScope}), &identitypb.GetPrincipalRequest{})
		must.NoError(t, err)

		test.Eq(t, []string{"account.manage", "account.read"}, response.GetPermissions().GetPermissions())
	})

	T.Run("no resolver leaves the field absent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		registration := h.seedAccount(t, testScope, "somebody")

		caller := &sessionPrincipal{
			userID: registration.User.ID, scope: testScope,
			serviceRoles: []string{"operator"},
		}

		response, err := h.client.GetPrincipal(h.as(caller), &identitypb.GetPrincipalRequest{})
		must.NoError(t, err)

		test.Nil(t, response.GetPermissions())
	})

	T.Run("a nil resolver is ignored", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, identitygrpc.WithPermissionResolver(nil))
		registration := h.seedAccount(t, testScope, "somebody")

		response, err := h.client.GetPrincipal(
			h.as(&testPrincipal{userID: registration.User.ID, scope: testScope}), &identitypb.GetPrincipalRequest{})
		must.NoError(t, err)

		test.Nil(t, response.GetPermissions())
	})

	T.Run("a resolver that fails fails the read", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, identitygrpc.WithPermissionResolver(failingPolicy{}))
		registration := h.seedAccount(t, testScope, "somebody")

		_, err := h.client.GetPrincipal(
			h.as(&testPrincipal{userID: registration.User.ID, scope: testScope}), &identitypb.GetPrincipalRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.Internal, status.Code(err))
	})
}

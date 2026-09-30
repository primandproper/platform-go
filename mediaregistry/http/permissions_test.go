package http

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"path"
	"testing"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzhttp "github.com/primandproper/primitives-go/v2/authorization/http"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// enforcerGranting is an Enforcer whose every caller holds exactly perms.
func enforcerGranting(t *testing.T, perms ...authorization.Permission) *authzhttp.Enforcer {
	t.Helper()

	enforcer, err := authzhttp.NewEnforcer(func(context.Context) (authorization.Grants, bool) {
		return authorization.NewGrants(authorization.NewPermissionSet(perms...)), true
	})
	must.NoError(t, err)

	return enforcer
}

// TestPermissions_coverEveryRoute is the coverage test authorization/http asks
// for in place of a fail-closed table: every route this surface mounts either
// requires a permission or is reached on the caller's own standing, and never
// both.
func TestPermissions_coverEveryRoute(T *testing.T) {
	T.Parallel()

	for _, basePath := range []string{BasePath, "/elsewhere/media"} {
		T.Run(basePath, func(t *testing.T) {
			t.Parallel()

			handler := newHandler(t, storeReturning(testObject()), newObjects(), WithBasePath(basePath))
			route := handler.Mount(router(t))
			key := route.Method + " " + route.Path

			perms, guarded := handler.Permissions()[key]
			test.True(t, guarded, test.Sprintf("%s is mounted and requires no permission", key))
			test.SliceNotEmpty(t, perms)
			test.SliceNotContains(t, handler.OwnStandingRoutes(), key)
			test.MapLen(t, 1, handler.Permissions())
			test.SliceEmpty(t, handler.OwnStandingRoutes(), test.Sprint("this surface has no route reached on the caller's own standing"))

			if basePath == BasePath {
				test.Eq(t, Permissions(), handler.Permissions())
			}
		})
	}
}

func TestHandler_permissions(T *testing.T) {
	T.Parallel()

	owner := Caller{Scope: tenancy.Of(testTenant), PrincipalID: testOwnerID}

	T.Run("a caller without the permission is refused before the row is read", func(t *testing.T) {
		t.Parallel()

		store, manager := storeReturning(testObject()), newRangingObjects()
		res := get(t, mount(t, store, manager, owner, WithEnforcer(enforcerGranting(t))), "made-up", nil)

		// 403 for an identifier nothing holds: a surface that read first would
		// answer 404, and the reservation would be unobservable.
		test.EqOp(t, nethttp.StatusForbidden, res.Code)
		test.SliceEmpty(t, store.GetObjectCalls())
		test.EqOp(t, int64(0), manager.opens.Load())
	})

	T.Run("the permission lets the owner through to their object", func(t *testing.T) {
		t.Parallel()

		res := get(t, mount(t, storeReturning(testObject()), newRangingObjects(), owner,
			WithEnforcer(enforcerGranting(t, PermissionReadObjects))), testObjectID, nil)

		test.EqOp(t, nethttp.StatusOK, res.Code)
	})

	T.Run("the permission widens nothing the entitlement declines", func(t *testing.T) {
		t.Parallel()

		stranger := Caller{Scope: tenancy.Of(testTenant), PrincipalID: testOtherOwner}
		res := get(t, mount(t, storeReturning(testObject()), newRangingObjects(), stranger,
			WithEnforcer(enforcerGranting(t, PermissionReadObjects))), testObjectID, nil)

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
	})

	T.Run("without an enforcer the route is refused", func(t *testing.T) {
		t.Parallel()

		store := storeReturning(testObject())

		handler, err := New(store, readerOnlyClient(), newRangingObjects(), WithCallerResolver(resolverFromContext))
		must.NoError(t, err)

		res := get(t, mountHandler(t, handler, owner), testObjectID, nil)

		test.EqOp(t, nethttp.StatusForbidden, res.Code)
		test.SliceEmpty(t, store.GetObjectCalls())
	})

	T.Run("a request with nobody on it is refused by the handler rather than the grant", func(t *testing.T) {
		t.Parallel()

		store := storeReturning(testObject())
		handler := newHandler(t, store, newRangingObjects(), WithEnforcer(enforcerGranting(t)))

		r := router(t)
		handler.Mount(r)
		must.NoError(t, r.Err())

		// No caller on the request at all, so the resolver fails and the
		// handler answers in its words — before it reads anything.
		res := httptest.NewRecorder()
		r.Handler().ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet,
			path.Join(BasePath, testObjectID), nethttp.NoBody))

		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
		test.SliceEmpty(t, store.GetObjectCalls())
	})
}

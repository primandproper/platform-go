package http

import (
	"context"
	"maps"
	nethttp "net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/operations"
	operationsmock "github.com/primandproper/platform-go/v15/operations/mock"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzhttp "github.com/primandproper/primitives-go/v2/authorization/http"
	"github.com/primandproper/primitives-go/v2/encoding"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/routing"
	"github.com/primandproper/primitives-go/v2/routing/backends/chi"
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

// untouchedService is a Service whose every method fails the test, for the
// assertions that a refusal came before anything was read.
func untouchedService(t *testing.T) *operationsmock.ServiceMock {
	t.Helper()

	return &operationsmock.ServiceMock{
		GetFunc: func(context.Context, tenancy.Scope, string) (*operations.Operation, error) {
			t.Error("an operation was read for a caller the route should have refused first")

			return nil, notFound("read")
		},
		ListFunc: func(
			context.Context,
			tenancy.Scope,
			*operations.ListScope,
			*filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[operations.Operation], error) {
			t.Error("operations were listed for a caller the route should have refused first")

			return &filtering.QueryFilteredResult[operations.Operation]{}, nil
		},
		CancelFunc: func(context.Context, tenancy.Scope, string) (*operations.Operation, error) {
			t.Error("an operation was cancelled for a caller the route should have refused first")

			return nil, notFound("cancel")
		},
	}
}

// routeKey is a mounted route as Permissions and OwnStandingRoutes key it.
func routeKey(r *routing.Route) string { return r.Method + " " + r.Path }

// requestFor is a request to route with its parameter filled with id.
func requestFor(t *testing.T, route, id string) *nethttp.Request {
	t.Helper()

	method, pattern, ok := strings.Cut(route, " ")
	must.True(t, ok)

	target := strings.ReplaceAll(pattern, "{"+pathParam+"}", id)

	if method != nethttp.MethodPost {
		return httptest.NewRequestWithContext(t.Context(), method, target, nethttp.NoBody)
	}

	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")

	return req
}

// watching is a Watcher over a store holding one operation, u1's.
func watching(t *testing.T) *operations.Watcher {
	t.Helper()

	watcher, err := operations.NewWatcher(t.Context(), &operations.WatcherConfig{
		Poll:            100 * time.Millisecond,
		MinReadInterval: time.Millisecond,
	}, stubClient{}, newStreamingStore("op1", tenancy.Of("u1")))
	must.NoError(t, err)

	t.Cleanup(func() { _ = watcher.Close() })

	go func() { _ = watcher.Run(t.Context()) }()

	return watcher
}

// TestPermissions_coverEveryRoute is the coverage test authorization/http asks
// for in place of a fail-closed table: every route this surface mounts either
// requires a permission or is reached on the caller's own standing, and never
// both.
func TestPermissions_coverEveryRoute(T *testing.T) {
	T.Parallel()

	for _, basePath := range []string{BasePath, "/elsewhere/ops"} {
		T.Run(basePath, func(t *testing.T) {
			t.Parallel()

			router := routing.New(chi.NewBackend(&chi.Config{ServiceName: "operations-test"}),
				encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

			handlers, err := New(&operationsmock.ServiceMock{}, WithOwnerResolver(GlobalOwner), WithWatcher(watching(t)),
				WithBasePath(basePath))
			must.NoError(t, err)

			mounted := map[string]bool{}
			for _, route := range handlers.Mount(router) {
				mounted[routeKey(route)] = true
			}

			must.NoError(t, router.Err())

			guarded := handlers.Permissions()
			own := handlers.OwnStandingRoutes()

			for route := range mounted {
				_, isGuarded := guarded[route]
				isOwn := slices.Contains(own, route)

				test.True(t, isGuarded != isOwn,
					test.Sprintf("%s must be in exactly one of Permissions and OwnStandingRoutes (guarded=%t, own=%t)", route, isGuarded, isOwn))
			}

			for _, route := range append(slices.Collect(maps.Keys(guarded)), own...) {
				test.True(t, mounted[route], test.Sprintf("%s is declared and not mounted", route))
			}

			for route, perms := range guarded {
				test.SliceNotEmpty(t, perms, test.Sprintf("%s requires no permission, which Require reads as refusing everybody", route))
			}

			if basePath == BasePath {
				test.Eq(t, Permissions(), guarded)
				test.Eq(t, OwnStandingRoutes(), own)
			}
		})
	}
}

func TestHandlers_permissions(T *testing.T) {
	T.Parallel()

	T.Run("a caller holding none of the permissions is refused every guarded route before anything is read", func(t *testing.T) {
		t.Parallel()

		handler := mount(t, untouchedService(t), tenancy.Of("u1"), WithEnforcer(enforcerGranting(t)))

		for route := range Permissions() {
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, requestFor(t, route, "made-up"))

			// 403 for an identifier nothing holds: a surface that read first
			// would answer 404, and the reservation would be unobservable.
			test.EqOp(t, nethttp.StatusForbidden, res.Code, test.Sprintf("%s answered %d", route, res.Code))
		}
	})

	T.Run("each guarded route admits a caller holding its permission", func(t *testing.T) {
		t.Parallel()

		for route, perms := range Permissions() {
			handler := mount(t, serviceReturning(nil), tenancy.Of("u1"), WithEnforcer(enforcerGranting(t, perms...)))

			res := httptest.NewRecorder()
			handler.ServeHTTP(res, requestFor(t, route, "made-up"))

			test.NotEqOp(t, nethttp.StatusForbidden, res.Code, test.Sprintf("%s refused a caller holding %v", route, perms))
		}
	})

	T.Run("the own-standing routes answer a caller holding no permission", func(t *testing.T) {
		t.Parallel()

		op := &operations.Operation{ID: "op1", Owner: tenancy.Of("u1"), State: operations.StateRunning}
		handler := mount(t, serviceReturning(op), tenancy.Of("u1"), WithEnforcer(enforcerGranting(t)))

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, requestFor(t, RouteGet, "op1"))
		test.EqOp(t, nethttp.StatusOK, res.Code)

		// The stream, against an operation this caller does not hold, so the
		// answer is the ownership read's rather than a stream that never ends.
		streamed := mount(t, serviceReturning(op), tenancy.Of("u2"),
			WithEnforcer(enforcerGranting(t)), WithWatcher(watching(t)))

		res = httptest.NewRecorder()
		streamed.ServeHTTP(res, requestFor(t, RouteEvents, "op1"))
		test.EqOp(t, nethttp.StatusNotFound, res.Code)
	})

	T.Run("without an enforcer every guarded route is refused and the own-standing ones still answer", func(t *testing.T) {
		t.Parallel()

		op := &operations.Operation{ID: "op1", Owner: tenancy.Of("u1"), State: operations.StateRunning}

		router := routing.New(chi.NewBackend(&chi.Config{ServiceName: "operations-test"}),
			encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

		handlers, err := New(serviceReturning(op), WithOwnerResolver(resolverFromContext))
		must.NoError(t, err)

		handlers.Mount(router)

		handler := asOwner(tenancy.Of("u1"), router.Handler())

		for route := range Permissions() {
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, requestFor(t, route, "op1"))

			test.EqOp(t, nethttp.StatusForbidden, res.Code, test.Sprintf("%s answered %d with no enforcer", route, res.Code))
		}

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, requestFor(t, RouteGet, "op1"))
		test.EqOp(t, nethttp.StatusOK, res.Code)
	})

	T.Run("a request with nobody on it is refused by the handler rather than the grant", func(t *testing.T) {
		t.Parallel()

		router := routing.New(chi.NewBackend(&chi.Config{ServiceName: "operations-test"}),
			encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

		handlers, err := New(untouchedService(t), WithOwnerResolver(resolverFromContext), WithEnforcer(enforcerGranting(t)))
		must.NoError(t, err)

		handlers.Mount(router)

		for route := range Permissions() {
			res := httptest.NewRecorder()
			router.Handler().ServeHTTP(res, requestFor(t, route, "made-up"))

			test.NotEqOp(t, nethttp.StatusForbidden, res.Code,
				test.Sprintf("%s refused a request with nobody on it as forbidden rather than in the resolver's words", route))
			test.Greater(t, 399, res.Code)
		}
	})
}

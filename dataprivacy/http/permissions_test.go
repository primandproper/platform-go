package http

import (
	"context"
	"maps"
	nethttp "net/http"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	dataprivacymock "github.com/primandproper/platform-go/v14/dataprivacy/mock"
	"github.com/primandproper/platform-go/v14/errormappers"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzhttp "github.com/primandproper/primitives-go/v2/authorization/http"
	"github.com/primandproper/primitives-go/v2/encoding"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
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

// hit issues one request to route, with its parameter filled with id.
func hit(t *testing.T, handler nethttp.Handler, route, id string) int {
	t.Helper()

	method, pattern, ok := strings.Cut(route, " ")
	must.True(t, ok)

	body := ""
	if method == nethttp.MethodPost {
		body = `{"type":"export"}`
	}

	return do(t, handler, method, strings.ReplaceAll(pattern, "{"+pathParam+"}", id), body).Code
}

// untouched reports whether nothing on svc was called.
func untouched(svc *dataprivacymock.ServiceMock) bool {
	return len(svc.SubmitCalls()) == 0 && len(svc.ListCalls()) == 0 && len(svc.GetCalls()) == 0 &&
		len(svc.ConfirmCalls()) == 0 && len(svc.CancelCalls()) == 0
}

// TestPermissions_coverEveryRoute is the coverage test authorization/http asks
// for in place of a fail-closed table: every route this surface mounts either
// requires a permission or is reached on the caller's own standing, and never
// both.
func TestPermissions_coverEveryRoute(t *testing.T) {
	t.Parallel()

	handlers, router := build(t, &dataprivacymock.ServiceMock{})

	mounted := map[string]bool{}
	for _, route := range handlers.Mount(router) {
		mounted[route.Method+" "+route.Path] = true
	}

	must.NoError(t, router.Err())

	guarded := Permissions()
	own := OwnStandingRoutes()

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
}

func TestHandlers_permissions(T *testing.T) {
	T.Parallel()

	subject := dataprivacy.Subject{ID: "u1"}

	T.Run("a caller holding none of the permissions is refused every guarded route before anything is read", func(t *testing.T) {
		t.Parallel()

		svc := &dataprivacymock.ServiceMock{}
		handler := mount(t, svc, subject, WithEnforcer(enforcerGranting(t)))

		for route := range Permissions() {
			// 403 for an identifier nothing holds: a surface that read first
			// would answer 404, and the reservation would be unobservable.
			code := hit(t, handler, route, "made-up")
			test.EqOp(t, nethttp.StatusForbidden, code, test.Sprintf("%s answered %d", route, code))
		}

		test.True(t, untouched(svc), test.Sprint("a refused caller's request reached the service"))
	})

	T.Run("each guarded route admits a caller holding its permission", func(t *testing.T) {
		t.Parallel()

		svc := serviceReturning(nil)
		svc.SubmitFunc = func(_ context.Context, _ tenancy.Scope, s dataprivacy.Subject, _ dataprivacy.RequestType) (*dataprivacy.Request, error) {
			return requestFor("r1", s, dataprivacy.StatusInProgress, ""), nil
		}
		svc.ListFunc = func(
			context.Context,
			*tenancy.Scope,
			dataprivacy.Subject,
			*filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[dataprivacy.Request], error) {
			return &filtering.QueryFilteredResult[dataprivacy.Request]{}, nil
		}
		svc.CancelFunc = func(context.Context, *tenancy.Scope, string) (*dataprivacy.Request, error) {
			return nil, platformerrors.Wrap(dataprivacy.ErrRequestNotFound, "made-up")
		}

		for route, perms := range Permissions() {
			handler := mount(t, svc, subject, WithEnforcer(enforcerGranting(t, perms...)))

			code := hit(t, handler, route, "made-up")
			test.NotEqOp(t, nethttp.StatusForbidden, code, test.Sprintf("%s refused a caller holding %v", route, perms))
		}
	})

	T.Run("the confirmation link answers a caller holding no permission", func(t *testing.T) {
		t.Parallel()

		req := requestFor("r1", subject, dataprivacy.StatusAwaitingConfirmation, "")
		svc := serviceReturning(req)
		svc.ConfirmFunc = func(context.Context, *tenancy.Scope, string) (*dataprivacy.Request, error) {
			confirmed := *req
			confirmed.Status = dataprivacy.StatusInProgress

			return &confirmed, nil
		}

		code := hit(t, mount(t, svc, subject, WithEnforcer(enforcerGranting(t))), RouteConfirm, "r1")

		test.EqOp(t, nethttp.StatusOK, code)
		test.SliceLen(t, 1, svc.ConfirmCalls())
	})

	T.Run("without an enforcer every guarded route is refused and the confirmation link still answers", func(t *testing.T) {
		t.Parallel()

		errormappers.Register()

		svc := serviceReturning(nil)

		handlers, err := New(svc, WithSubjectResolver(subjectFromContext))
		must.NoError(t, err)

		router := routing.New(chi.NewBackend(&chi.Config{ServiceName: "dataprivacy-test"}),
			encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))
		handlers.Mount(router)
		must.NoError(t, router.Err())

		handler := asSubject(subject, router.Handler())

		for route := range Permissions() {
			code := hit(t, handler, route, "made-up")
			test.EqOp(t, nethttp.StatusForbidden, code, test.Sprintf("%s answered %d with no enforcer", route, code))
		}

		// Absent rather than forbidden: the link's route asks no grant, and
		// the request it names is nobody's.
		test.EqOp(t, nethttp.StatusNotFound, hit(t, handler, RouteConfirm, "made-up"))
	})

	T.Run("a request with nobody on it is refused by the handler rather than the grant", func(t *testing.T) {
		t.Parallel()

		svc := &dataprivacymock.ServiceMock{}

		handlers, router := build(t, svc, WithEnforcer(enforcerGranting(t)))
		handlers.Mount(router)
		must.NoError(t, router.Err())

		for route := range Permissions() {
			code := hit(t, router.Handler(), route, "made-up")

			test.NotEqOp(t, nethttp.StatusForbidden, code,
				test.Sprintf("%s refused a request with nobody on it as forbidden rather than in the resolver's words", route))
			test.Greater(t, 399, code)
		}

		test.True(t, untouched(svc))
	})
}

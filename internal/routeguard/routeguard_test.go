package routeguard

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzhttp "github.com/primandproper/primitives-go/v2/authorization/http"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const testPermission authorization.Permission = "routeguard.test"

func enforcerGranting(t *testing.T, perms ...authorization.Permission) *authzhttp.Enforcer {
	t.Helper()

	enforcer, err := authzhttp.NewEnforcer(func(context.Context) (authorization.Grants, bool) {
		return authorization.NewGrants(authorization.NewPermissionSet(perms...)), true
	})
	must.NoError(t, err)

	return enforcer
}

// counting is a resolver that answers caller or err, and counts its calls.
func counting(caller string, err error) (func(context.Context) (string, error), *atomic.Int64) {
	calls := &atomic.Int64{}

	return func(context.Context) (string, error) {
		calls.Add(1)

		return caller, err
	}, calls
}

// serve runs one request through guard's Require in front of a handler that
// asks the guard for the caller, and reports what the handler saw.
func serve(t *testing.T, guard *Guard[string]) (code int, reached bool, caller string, callerErr error) {
	t.Helper()

	handler := guard.Require(testPermission)(nethttp.HandlerFunc(func(res nethttp.ResponseWriter, req *nethttp.Request) {
		reached = true
		caller, callerErr = guard.Caller(req.Context())

		res.WriteHeader(nethttp.StatusNoContent)
	}))

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, "/", nethttp.NoBody))

	return res.Code, reached, caller, callerErr
}

func TestGuard(T *testing.T) {
	T.Parallel()

	T.Run("resolves the caller once for the check and the handler", func(t *testing.T) {
		t.Parallel()

		resolve, calls := counting("u1", nil)
		guard, err := New(enforcerGranting(t, testPermission), resolve, nil)
		must.NoError(t, err)

		code, reached, caller, callerErr := serve(t, guard)

		test.EqOp(t, nethttp.StatusNoContent, code)
		test.True(t, reached)
		test.EqOp(t, "u1", caller)
		test.NoError(t, callerErr)
		test.EqOp(t, int64(1), calls.Load())
	})

	T.Run("refuses a caller without the grant before the handler runs", func(t *testing.T) {
		t.Parallel()

		resolve, _ := counting("u1", nil)
		guard, err := New(enforcerGranting(t), resolve, nil)
		must.NoError(t, err)

		code, reached, _, _ := serve(t, guard)

		test.EqOp(t, nethttp.StatusForbidden, code)
		test.False(t, reached)
	})

	T.Run("refuses every guarded route with no enforcer", func(t *testing.T) {
		t.Parallel()

		resolve, _ := counting("u1", nil)
		guard, err := New(nil, resolve, nil)
		must.NoError(t, err)

		code, reached, _, _ := serve(t, guard)

		test.EqOp(t, nethttp.StatusForbidden, code)
		test.False(t, reached)
	})

	T.Run("hands an unresolved request to the handler with the resolver's error", func(t *testing.T) {
		t.Parallel()

		errNobody := platformerrors.New("nobody on the request")
		resolve, calls := counting("", errNobody)
		guard, err := New(nil, resolve, nil)
		must.NoError(t, err)

		_, reached, _, callerErr := serve(t, guard)

		test.True(t, reached)
		test.ErrorIs(t, callerErr, errNobody)
		test.EqOp(t, int64(1), calls.Load())
	})

	T.Run("resolves afresh on a route with no guard in front of it", func(t *testing.T) {
		t.Parallel()

		resolve, calls := counting("u1", nil)
		guard, err := New(nil, resolve, nil)
		must.NoError(t, err)

		caller, err := guard.Caller(t.Context())
		must.NoError(t, err)

		test.EqOp(t, "u1", caller)
		test.EqOp(t, int64(1), calls.Load())
	})

	T.Run("does not answer from another guard's resolution", func(t *testing.T) {
		t.Parallel()

		outerResolve, _ := counting("outer", nil)
		outer, err := New(enforcerGranting(t, testPermission), outerResolve, nil)
		must.NoError(t, err)

		innerResolve, _ := counting("inner", nil)
		inner, err := New(nil, innerResolve, nil)
		must.NoError(t, err)

		var seen string

		handler := outer.Require(testPermission)(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, req *nethttp.Request) {
			seen, _ = inner.Caller(req.Context())
		}))
		handler.ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, "/", nethttp.NoBody))

		test.EqOp(t, "inner", seen)
	})
}

func TestRebase(T *testing.T) {
	T.Parallel()

	T.Run("moves the collection route onto the base", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "GET /elsewhere/ops", Rebase("GET /operations", "/operations", "/elsewhere/ops"))
	})

	T.Run("joins a member route to the base", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "POST /elsewhere/ops/{id}/cancel",
			Rebase("POST /operations/{id}/cancel", "/operations", "/elsewhere/ops"))
	})

	T.Run("leaves a route at the default base alone", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "GET /operations/{id}", Rebase("GET /operations/{id}", "/operations", "/operations"))
	})
}

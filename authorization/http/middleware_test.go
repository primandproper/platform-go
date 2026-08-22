package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v13/authorization"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	httpx "github.com/primandproper/platform-go/v13/errors/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const (
	permRead  authorization.Permission = "read.things"
	permWrite authorization.Permission = "write.things"
)

func grantsOf(perms ...authorization.Permission) authorization.GrantsExtractor {
	return func(context.Context) (authorization.Grants, bool) {
		return authorization.NewGrants(authorization.NewPermissionSet(perms...)), true
	}
}

func noGrants() authorization.GrantsExtractor {
	return func(context.Context) (authorization.Grants, bool) {
		return authorization.Grants{}, false
	}
}

// serve runs one request through Require and reports whether the handler ran.
func serve(
	t *testing.T,
	extract authorization.GrantsExtractor,
	required []authorization.Permission,
	opts ...Option,
) (called bool, res *httptest.ResponseRecorder) {
	t.Helper()

	e, err := NewEnforcer(extract, opts...)
	must.NoError(t, err)

	handler := e.Require(required...)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/things", http.NoBody))

	return called, res
}

func TestEnforcer_Require(T *testing.T) {
	T.Parallel()

	T.Run("admits a request holding every permission", func(t *testing.T) {
		t.Parallel()

		called, res := serve(t, grantsOf(permRead, permWrite), []authorization.Permission{permRead, permWrite})

		test.True(t, called)
		test.EqOp(t, http.StatusOK, res.Code)
	})

	T.Run("denies a request holding only a subset", func(t *testing.T) {
		t.Parallel()

		called, res := serve(t, grantsOf(permRead), []authorization.Permission{permRead, permWrite})

		test.False(t, called)
		test.EqOp(t, http.StatusForbidden, res.Code)
	})

	T.Run("denies when no grants are available", func(t *testing.T) {
		t.Parallel()

		called, res := serve(t, noGrants(), []authorization.Permission{permRead})

		test.False(t, called)
		test.EqOp(t, http.StatusForbidden, res.Code)
	})

	// Set algebra would make this a vacuous allow. A middleware installed with
	// an empty list is far more likely to be a configuration bug than an intent
	// to admit everyone, and a route needing no authorization simply omits the
	// middleware.
	T.Run("denies when required with no permissions", func(t *testing.T) {
		t.Parallel()

		called, res := serve(t, grantsOf(permRead), nil)

		test.False(t, called)
		test.EqOp(t, http.StatusForbidden, res.Code)
	})

	T.Run("a caller's permission slice cannot alter the requirement", func(t *testing.T) {
		t.Parallel()

		e, err := NewEnforcer(grantsOf(permRead))
		must.NoError(t, err)

		perms := []authorization.Permission{permRead}
		middleware := e.Require(perms...)
		perms[0] = permWrite

		var called bool
		handler := middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/things", http.NoBody))

		test.True(t, called)
	})
}

func TestEnforcer_DenialResponse(T *testing.T) {
	T.Parallel()

	T.Run("emits the platform error envelope", func(t *testing.T) {
		t.Parallel()

		_, res := serve(t, noGrants(), []authorization.Permission{permRead})

		test.EqOp(t, http.StatusForbidden, res.Code)
		test.StrContains(t, res.Header().Get("Content-Type"), "application/json")

		var body httpx.APIResponse[any]
		must.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))

		must.NotNil(t, body.Error)
		test.EqOp(t, httpx.ErrUserIsNotAuthorized, body.Error.Code)
		test.EqOp(t, "permission denied", body.Error.Message)
	})

	// The security assertion: the response must not disclose what was missing.
	T.Run("does not leak the permission taxonomy", func(t *testing.T) {
		t.Parallel()

		_, res := serve(t, grantsOf(permRead), []authorization.Permission{permWrite})

		test.StrNotContains(t, res.Body.String(), string(permWrite))
	})

	T.Run("E110 maps to forbidden", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, http.StatusForbidden, httpx.HTTPStatusForCode(httpx.ErrUserIsNotAuthorized))
	})

	// The registered platform mapper is what gives a handler that returns the
	// sentinel the same response the middleware produces.
	T.Run("the platform mapper maps the sentinel", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := httpx.PlatformMapper.Map(platformerrors.ErrPermissionDenied)

		test.True(t, ok)
		test.EqOp(t, httpx.ErrUserIsNotAuthorized, code)
		test.EqOp(t, "permission denied", msg)
	})

	T.Run("honors a custom deny handler", func(t *testing.T) {
		t.Parallel()

		var seen error
		called, res := serve(t, noGrants(), []authorization.Permission{permRead},
			WithDenyHandler(func(w http.ResponseWriter, _ *http.Request, err error) {
				seen = err
				w.WriteHeader(http.StatusTeapot)
			}),
		)

		test.False(t, called)
		test.EqOp(t, http.StatusTeapot, res.Code)
		test.True(t, errors.Is(seen, platformerrors.ErrPermissionDenied))
	})

	T.Run("a nil deny handler falls back to the default", func(t *testing.T) {
		t.Parallel()

		_, res := serve(t, noGrants(), []authorization.Permission{permRead}, WithDenyHandler(nil))

		test.EqOp(t, http.StatusForbidden, res.Code)
	})
}

func TestEnforcer_AuditOnly(T *testing.T) {
	T.Parallel()

	T.Run("lets a would-be denial through", func(t *testing.T) {
		t.Parallel()

		called, res := serve(t, grantsOf(permRead), []authorization.Permission{permWrite}, WithAuditOnly())

		test.True(t, called)
		test.EqOp(t, http.StatusOK, res.Code)
	})

	T.Run("lets a request with no grants through", func(t *testing.T) {
		t.Parallel()

		called, _ := serve(t, noGrants(), []authorization.Permission{permRead}, WithAuditOnly())

		test.True(t, called)
	})

	T.Run("still admits an allowed request", func(t *testing.T) {
		t.Parallel()

		called, res := serve(t, grantsOf(permRead), []authorization.Permission{permRead}, WithAuditOnly())

		test.True(t, called)
		test.EqOp(t, http.StatusOK, res.Code)
	})
}

func TestNewEnforcer(T *testing.T) {
	T.Parallel()

	T.Run("rejects a nil extractor", func(t *testing.T) {
		t.Parallel()

		_, err := NewEnforcer(nil)

		test.True(t, errors.Is(err, platformerrors.ErrNilInputParameter))
	})

	T.Run("tolerates a nil option", func(t *testing.T) {
		t.Parallel()

		e, err := NewEnforcer(grantsOf(permRead), nil)

		test.NoError(t, err)
		test.NotNil(t, e)
	})
}

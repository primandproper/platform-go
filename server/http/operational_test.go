package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/healthcheck"
	"github.com/primandproper/platform-go/v13/routing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// stubChecker reports whatever it was built to report, which is the only thing
// these tests need a component for.
type stubChecker struct {
	err  error
	name string
}

func (c *stubChecker) Name() string                  { return c.name }
func (c *stubChecker) Check(_ context.Context) error { return c.err }

// serve runs one GET through handler and hands back the recorder.
func serve(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody))

	return res
}

func TestLivenessHandler(T *testing.T) {
	T.Parallel()

	T.Run("reports up without consulting anything", func(t *testing.T) {
		t.Parallel()

		res := serve(t, LivenessHandler(), LivenessPath)

		test.EqOp(t, http.StatusOK, res.Code)
		test.EqOp(t, "application/json", res.Header().Get("Content-Type"))
		test.EqOp(t, `{"status":"up"}`+"\n", res.Body.String())
	})
}

func TestReadinessHandler(T *testing.T) {
	T.Parallel()

	T.Run("every component up is a 200", func(t *testing.T) {
		t.Parallel()

		registry := newHealthRegistry(t)
		registry.Register(&stubChecker{name: "database"})

		res := serve(t, ReadinessHandler(registry), ReadinessPath)

		test.EqOp(t, http.StatusOK, res.Code)

		var result healthcheck.Result
		must.NoError(t, json.Unmarshal(res.Body.Bytes(), &result))

		test.EqOp(t, healthcheck.StatusUp, result.Status)
		test.EqOp(t, healthcheck.StatusUp, result.Components["database"].Status)
	})

	T.Run("one component down is a 503 that names it", func(t *testing.T) {
		t.Parallel()

		// The body is the reason the probe is worth reading: an operator gets
		// which component failed and what it said, not just the status.
		registry := newHealthRegistry(t)
		registry.Register(&stubChecker{name: "database"})
		registry.Register(&stubChecker{name: "message_queue", err: errors.New("no broker")})

		res := serve(t, ReadinessHandler(registry), ReadinessPath)

		test.EqOp(t, http.StatusServiceUnavailable, res.Code)

		var result healthcheck.Result
		must.NoError(t, json.Unmarshal(res.Body.Bytes(), &result))

		test.EqOp(t, healthcheck.StatusDown, result.Status)
		test.EqOp(t, healthcheck.StatusUp, result.Components["database"].Status)
		test.EqOp(t, healthcheck.StatusDown, result.Components["message_queue"].Status)
		test.EqOp(t, "no broker", result.Components["message_queue"].Message)
	})

	T.Run("a nil registry checks nothing and reports ready", func(t *testing.T) {
		t.Parallel()

		res := serve(t, ReadinessHandler(nil), ReadinessPath)

		test.EqOp(t, http.StatusOK, res.Code)
	})
}

func TestVersionHandler(T *testing.T) {
	T.Parallel()

	T.Run("serves the build metadata", func(t *testing.T) {
		t.Parallel()

		res := serve(t, VersionHandler(), VersionPath)

		test.EqOp(t, http.StatusOK, res.Code)
		test.EqOp(t, "application/json", res.Header().Get("Content-Type"))

		// An unstamped test binary reports "unknown" for every field, which is
		// the contract: the endpoint answers with what it has.
		body := map[string]string{}
		must.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
		test.MapContainsKey(t, body, "version")
		test.MapContainsKey(t, body, "commit_hash")
	})
}

func Test_mountOperationalEndpoints(T *testing.T) {
	T.Parallel()

	// routeStatus asks the router what it answers for a path, where 404 means
	// nothing was mounted there.
	routeStatus := func(t *testing.T, router *routing.Router, path string) int {
		t.Helper()

		return serve(t, router.Handler(), path).Code
	}

	T.Run("mounts the probes and the version route", func(t *testing.T) {
		t.Parallel()

		router := testRouter(t)
		mountOperationalEndpoints(router.Backend(), newHealthRegistry(t), true)

		test.EqOp(t, http.StatusOK, routeStatus(t, router, LivenessPath))
		test.EqOp(t, http.StatusOK, routeStatus(t, router, ReadinessPath))
		test.EqOp(t, http.StatusOK, routeStatus(t, router, VersionPath))
	})

	T.Run("mounts nothing when nothing was asked for", func(t *testing.T) {
		t.Parallel()

		// The routes are opt-in precisely so a service that serves its own
		// /healthz does not find it registered twice.
		router := testRouter(t)
		mountOperationalEndpoints(router.Backend(), nil, false)

		test.EqOp(t, http.StatusNotFound, routeStatus(t, router, LivenessPath))
		test.EqOp(t, http.StatusNotFound, routeStatus(t, router, ReadinessPath))
		test.EqOp(t, http.StatusNotFound, routeStatus(t, router, VersionPath))
	})

	T.Run("the probes and the version route are independent", func(t *testing.T) {
		t.Parallel()

		router := testRouter(t)
		mountOperationalEndpoints(router.Backend(), newHealthRegistry(t), false)

		test.EqOp(t, http.StatusOK, routeStatus(t, router, LivenessPath))
		test.EqOp(t, http.StatusNotFound, routeStatus(t, router, VersionPath))
	})

	T.Run("the probes stay out of the OpenAPI document", func(t *testing.T) {
		t.Parallel()

		// The whole reason they are mounted on the backend: a generated client
		// should not have a ReadyzWithResponse method, and /readyz's body is the
		// health registry's shape rather than part of this service's API.
		router := testRouter(t)
		mountOperationalEndpoints(router.Backend(), newHealthRegistry(t), true)

		spec, err := router.MarshalSpec()
		must.NoError(t, err)

		test.StrNotContains(t, string(spec), LivenessPath)
		test.StrNotContains(t, string(spec), ReadinessPath)
		test.StrNotContains(t, string(spec), VersionPath)
	})
}

func TestNewHTTPServer_operationalEndpoints(T *testing.T) {
	T.Parallel()

	T.Run("serves the probes from the registry it was given", func(t *testing.T) {
		t.Parallel()

		registry := newHealthRegistry(t)
		registry.Register(&stubChecker{name: "database", err: errors.New("down")})

		router := testRouter(t)

		_, err := NewHTTPServer(t.Context(), &Config{}, router, WithHealthRegistry(registry))
		must.NoError(t, err)

		res := serve(t, router.Handler(), ReadinessPath)
		test.EqOp(t, http.StatusServiceUnavailable, res.Code)
	})

	T.Run("a nil router is left alone", func(t *testing.T) {
		t.Parallel()

		// Mounting is the only thing NewHTTPServer does to the router it is
		// handed, so a caller that has none must not be the one crash it causes.
		_, err := NewHTTPServer(t.Context(), &Config{}, nil, WithHealthRegistry(newHealthRegistry(t)), WithVersionEndpoint())
		must.NoError(t, err)
	})
}

func Test_isProbePath(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.True(t, isProbePath(LivenessPath))
		test.True(t, isProbePath(ReadinessPath))

		// The version route is not on a timer, so it is not noise.
		test.False(t, isProbePath(VersionPath))
		test.False(t, isProbePath("/api/v1/things"))
	})
}

// newHealthRegistry builds an uninstrumented health registry for a test.
func newHealthRegistry(t *testing.T) *healthcheck.CheckerRegistry {
	t.Helper()

	registry, err := healthcheck.NewRegistry()
	must.NoError(t, err)

	return registry
}

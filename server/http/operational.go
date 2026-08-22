package http

import (
	"encoding/json"
	"net/http"

	"github.com/primandproper/platform-go/v13/healthcheck"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/version"
)

const (
	// LivenessPath answers whether the process is alive. It is deliberately the
	// cheapest handler in the module: a liveness probe that consults a
	// dependency turns that dependency's outage into a restart loop, which is
	// the one response guaranteed not to fix it.
	LivenessPath = "/healthz"

	// ReadinessPath answers whether the process should be sent traffic, which is
	// the question the health registry exists to answer. A component reporting
	// down takes the process out of the load balancer's rotation and leaves it
	// running, so it can recover and rejoin.
	ReadinessPath = "/readyz"

	// VersionPath serves the build metadata the binary was stamped with.
	VersionPath = "/version"
)

// livenessBody is the whole of the liveness answer, written as a constant so
// the handler cannot fail on a marshal and does not allocate per probe.
var livenessBody = []byte(`{"status":"up"}` + "\n")

// mountOperationalEndpoints registers the operational routes on the backend
// rather than through routing's typed registration.
//
// They go on the backend for the same reason the Apple site association file
// does: they are not part of the service's API. A probe endpoint in the OpenAPI
// document is a route every generated client can call and no consumer should,
// and /readyz's body is the health registry's shape rather than anything this
// service designed. Going through the backend still gets them the router's
// middleware — the recovery, the CORS, the logging that already knows to stay
// quiet about probes.
func mountOperationalEndpoints(backend routing.Backend, registry healthcheck.Registry, mountVersion bool, opts ...Option) {
	if registry != nil {
		backend.Handle(http.MethodGet, LivenessPath, LivenessHandler(opts...))
		backend.Handle(http.MethodGet, ReadinessPath, ReadinessHandler(registry, opts...))
	}

	if mountVersion {
		backend.Handle(http.MethodGet, VersionPath, VersionHandler(opts...))
	}
}

// LivenessHandler reports that the process is up, and nothing else.
//
// It is exported so a service that serves its probes from its own mux — one
// that binds a separate operational listener, say — gets the same answers this
// server would have given.
func LivenessHandler(opts ...Option) http.Handler {
	logger := logging.EnsureLogger(newOptions(opts).logger)

	return http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		res.Header().Set("Content-Type", "application/json")
		res.WriteHeader(http.StatusOK)

		if _, err := res.Write(livenessBody); err != nil {
			logger.Error("writing liveness response", err)
		}
	})
}

// ReadinessHandler runs every checker in the registry and reports the
// aggregate: 200 when all of them are up, 503 when any is down. The body is the
// per-component breakdown either way, so an operator reading the probe's
// response learns which component is the reason.
//
// A nil registry checks nothing and therefore always reports ready, which is
// what a registry with no checkers registered would have said too.
//
// The checks run on the request's context, so a probe that gives up disconnects
// the checks with it, and each individual check is bounded by the registry's own
// timeout.
func ReadinessHandler(registry healthcheck.Registry, opts ...Option) http.Handler {
	logger := logging.EnsureLogger(newOptions(opts).logger)

	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		result := healthcheck.Check(req.Context(), registry)

		status := http.StatusOK
		if result.Status != healthcheck.StatusUp {
			status = http.StatusServiceUnavailable
		}

		res.Header().Set("Content-Type", "application/json")
		res.WriteHeader(status)

		if err := json.NewEncoder(res).Encode(result); err != nil {
			logger.Error("writing readiness response", err)
		}
	})
}

// VersionHandler serves the build metadata from the version package, which is
// whatever the binary's -ldflags stamped in and "unknown" for whatever they did
// not.
func VersionHandler(opts ...Option) http.Handler {
	logger := logging.EnsureLogger(newOptions(opts).logger)

	return http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		res.Header().Set("Content-Type", "application/json")
		res.WriteHeader(http.StatusOK)

		if err := version.WriteJSON(res); err != nil {
			logger.Error("writing version response", err)
		}
	})
}

// isProbePath reports whether path is one of the probe endpoints, which are
// requested on a timer forever and are worth neither a span nor a log line.
func isProbePath(path string) bool {
	return path == LivenessPath || path == ReadinessPath
}

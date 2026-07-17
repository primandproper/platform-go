// Package httpmw holds the plain net/http middleware stack shared by the
// non-chi routing backends (stdlib, httprouter, gin). Each of those backends
// exposes an http.Handler for its mux/engine and wraps it with the same
// observability, recovery, CORS, and OpenTelemetry middleware, so the behavior
// lives here once rather than being copied per backend.
//
// The chi backend keeps its own copy of this stack because chi installs
// middleware through chi.Router.Use and instruments with otelchi rather than
// otelhttp; the two are close cousins but not identical.
package httpmw

import (
	"net/http"
	"regexp"
	"time"

	"github.com/primandproper/platform-go/v5/routing"
)

const (
	// MaxTimeout bounds how long any single request may run before the timeout
	// middleware aborts it.
	MaxTimeout = 120 * time.Second
	// MaxCORSAge is the max-age (in seconds) advertised for CORS preflight caching.
	MaxCORSAge = 300
)

// healthCheckPaths are request paths that should not be traced or logged (e.g.
// load balancer probes). It mirrors the chi backend's set so probes are quiet
// regardless of which backend is in use.
var healthCheckPaths = map[string]bool{
	"/_ops_/live":  true,
	"/_ops_/ready": true,
}

// IsHealthCheck reports whether path is an operational health-check endpoint.
func IsHealthCheck(path string) bool {
	return healthCheckPaths[path]
}

// Chain wraps h with mws so that mws[0] is the outermost handler (the first to
// see a request and the last to see the response), matching the order chi
// applies middleware registered via Use.
func Chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		if mws[i] != nil {
			h = mws[i](h)
		}
	}

	return h
}

// Convert adapts routing.Middleware values to plain net/http middleware,
// dropping any nil entries so callers may pass optional middleware without
// guarding each one.
func Convert(in ...routing.Middleware) []func(http.Handler) http.Handler {
	out := make([]func(http.Handler) http.Handler, 0, len(in))
	for _, mw := range in {
		if mw != nil {
			out = append(out, mw)
		}
	}

	return out
}

// pathParamRE matches a single "{name}" path placeholder. The routing layer has
// already stripped any ":token" type annotation before a pattern reaches a
// backend, so only the bare name remains.
var pathParamRE = regexp.MustCompile(`\{([^{}/]+)\}`)

// ColonParams rewrites a "/users/{id}" pattern into the "/users/:id" form used
// by httprouter and gin. chi and the stdlib mux consume "{name}" directly and
// need no conversion.
func ColonParams(pattern string) string {
	return pathParamRE.ReplaceAllString(pattern, ":$1")
}

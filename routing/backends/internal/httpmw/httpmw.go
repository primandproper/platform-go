// Package httpmw holds the net/http middleware stack shared by every routing
// backend. Each backend exposes an http.Handler for its mux or engine and wraps
// it with the same observability, recovery, CORS, and request-ID middleware, so
// the behavior lives here once rather than being copied per backend.
//
// chi consumes it too, through chi.Router.Use, which takes the same plain
// func(http.Handler) http.Handler that Chain composes. What chi does not share
// is the OpenTelemetry step — it instruments with otelchi, which reads chi's
// RouteContext and can therefore name a span after the matched route where the
// generic otelhttp wrapper here can only name it after the method. That one
// difference is why Standard exposes the stack as a slice: chi rebuilds the
// same order around its own otel middleware rather than copying the rest.
//
// Standard itself is for the backends that take otelMiddleware as given
// (stdlib, httprouter, gin).
package httpmw

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/routing"
)

const (
	// MaxTimeout bounds how long any single request may run before the timeout
	// middleware aborts it.
	MaxTimeout = 120 * time.Second
	// MaxCORSAge is the max-age (in seconds) advertised for CORS preflight caching.
	MaxCORSAge = 300
)

// healthCheckPaths are request paths that should not be traced or logged (e.g.
// load balancer probes). Every backend reads this one set, so probes are quiet
// regardless of which backend is in use.
//
// The two /healthz and /readyz entries are server/http's LivenessPath and
// ReadinessPath, spelled out rather than imported: that package is built on
// routing, so the constants cannot travel in this direction.
var healthCheckPaths = map[string]bool{
	"/_ops_/live":  true,
	"/_ops_/ready": true,
	"/healthz":     true,
	"/readyz":      true,
}

// IsHealthCheck reports whether path is an operational health-check endpoint.
func IsHealthCheck(path string) bool {
	return healthCheckPaths[path]
}

const (
	// operationalPathPrefix is the prefix server/http mounts its operational
	// endpoints under — probes, version, and whatever else is scraped rather than
	// requested by a user.
	operationalPathPrefix = "/_ops_/"

	// appleAppSiteAssociationPath is the well-known path iOS fetches. Like the
	// probe paths above it is spelled out rather than imported: it is declared in
	// server/http, which is built on routing, so the constant cannot travel in
	// this direction.
	appleAppSiteAssociationPath = "/.well-known/apple-app-site-association"
)

// IsUntraced reports whether path should produce no server span.
//
// It is a superset of IsHealthCheck: everything under the operational prefix and
// the Apple site-association file, all of which are fetched on a timer by
// something that is not a user and none of which is worth a trace.
//
// The distinction from IsHealthCheck is deliberate — route logging still wants
// to see a request to /_ops_/version, while tracing does not.
func IsUntraced(path string) bool {
	return IsHealthCheck(path) ||
		strings.HasPrefix(path, operationalPathPrefix) ||
		path == appleAppSiteAssociationPath
}

// Chain wraps h with mws so that mws[0] is the outermost handler (the first to
// see a request and the last to see the response), matching the order chi
// applies middleware registered via Use. That is what lets one ordering serve
// both the backends that chain by hand and the one that calls chi.Router.Use.
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

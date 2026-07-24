// Package stdlib provides a routing.Backend built on the standard library's
// net/http.ServeMux. It adds no third-party router dependency: Go's mux already
// supports method-scoped patterns ("GET /users/{id}") and per-request path
// values, which is exactly the shape routing.Backend needs. The shared
// observability, recovery, CORS, and OpenTelemetry middleware stack is applied
// around the mux, matching the chi backend's behavior.
package stdlib

import (
	"net/http"
	"sync"

	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	"github.com/primandproper/platform-go/v6/observability/tracing"
	"github.com/primandproper/platform-go/v6/routing"
	"github.com/primandproper/platform-go/v6/routing/backends/internal/httpmw"
)

var _ routing.Backend = (*backend)(nil)

// backend is a net/http.ServeMux implementation of routing.Backend. Global
// middleware is composed around the mux lazily in Handler; because the mux is
// wrapped by reference, routes registered after the first Handler call are still
// served.
type backend struct {
	built    http.Handler
	mux      *http.ServeMux
	standard []func(http.Handler) http.Handler
	user     []func(http.Handler) http.Handler
	once     sync.Once
}

// NewBackend constructs a net/http-backed routing.Backend with the standard
// middleware and OpenTelemetry stack installed. Pass it to routing.New.
func NewBackend(logger logging.Logger, tracerProvider tracing.TracerProvider, metricProvider metrics.Provider, cfg *Config) routing.Backend {
	tracerProvider = tracing.EnsureTracerProvider(tracerProvider)
	o11y := observability.NewObserver("router", logging.EnsureLogger(logger), tracerProvider)

	return &backend{
		mux: http.NewServeMux(),
		standard: httpmw.Standard(o11y, &httpmw.StackConfig{
			TracerProvider:         tracerProvider,
			MeterProvider:          metrics.EnsureMetricsProvider(metricProvider).MeterProvider(),
			ServiceName:            cfg.ServiceName,
			ValidDomains:           cfg.ValidDomains,
			EnableCORSForLocalhost: cfg.EnableCORSForLocalhost,
			SilenceRouteLogging:    cfg.SilenceRouteLogging,
		}),
	}
}

// Use installs global middleware, applied to every route. It may be called at
// any time before Handler; unlike chi, this backend imposes no ordering
// constraint relative to Handle.
func (b *backend) Use(middleware ...routing.Middleware) {
	b.user = append(b.user, httpmw.Convert(middleware...)...)
}

// Handle registers handler for method at pattern, using net/http's native
// "METHOD /path/{name}" pattern syntax.
func (b *backend) Handle(method, pattern string, handler http.Handler) {
	b.mux.Handle(method+" "+pattern, handler)
}

// PathValue returns the named path parameter, resolved by the ServeMux from the
// matched pattern.
func (b *backend) PathValue(req *http.Request, name string) string {
	return req.PathValue(name)
}

// Handler returns the composed http.Handler: the standard middleware stack and
// any user middleware wrapped around the mux.
func (b *backend) Handler() http.Handler {
	b.once.Do(func() {
		all := make([]func(http.Handler) http.Handler, 0, len(b.standard)+len(b.user))
		all = append(all, b.standard...)
		all = append(all, b.user...)
		b.built = httpmw.Chain(b.mux, all...)
	})

	return b.built
}

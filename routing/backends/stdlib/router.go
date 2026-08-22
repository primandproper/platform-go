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
	"sync/atomic"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/routing/backends/internal/httpmw"
)

var _ routing.Backend = (*Backend)(nil)

// Backend is a net/http.ServeMux implementation of routing.Backend. Global
// middleware is composed around the mux lazily in Handler; because the mux is
// wrapped by reference, routes registered after the first Handler call are still
// served.
//
// It is exported, and returned by NewBackend, so a caller who has chosen the
// standard library mux can depend on that choice rather than on the seam every
// router backend shares.
type Backend struct {
	built    http.Handler
	mux      *http.ServeMux
	standard []func(http.Handler) http.Handler
	user     []func(http.Handler) http.Handler
	once     sync.Once
	sealed   atomic.Bool
}

// NewBackend constructs a net/http-backed routing.Backend with the standard
// middleware and OpenTelemetry stack installed. Pass it to routing.New.
func NewBackend(cfg *Config, opts ...Option) *Backend {
	// A nil config is the zero config, not a panic. The config subpackage
	// dispatches on Provider and hands whichever sub-config happens to be set —
	// which is nil unless the deployment filled that provider's section in, so
	// every backend here got one on a perfectly ordinary configuration.
	if cfg == nil {
		cfg = &Config{}
	}

	o := newOptions(opts)
	tracerProvider := tracing.EnsureTracerProvider(o.tracerProvider)
	o11y := observability.NewObserver("router", logging.EnsureLogger(o.logger), tracerProvider)

	return &Backend{
		mux: http.NewServeMux(),
		standard: httpmw.Standard(o11y, &httpmw.StackConfig{
			TracerProvider:         tracerProvider,
			MeterProvider:          metrics.EnsureMetricsProvider(o.metricsProvider).MeterProvider(),
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
// Use appends middleware to the chain.
//
// It must be called before Handler(). The chain is composed once, on the first
// Handler() call, so middleware added afterwards was silently dropped — the
// server ran without the authentication or rate limiting the caller believed it
// had registered. That is now a panic: a middleware that does not run is not a
// condition a process should serve traffic in.
func (b *Backend) Use(middleware ...routing.Middleware) {
	if b.sealed.Load() {
		panic("routing: Use called after Handler; middleware must be registered before the handler is built")
	}

	b.user = append(b.user, httpmw.Convert(middleware...)...)
}

// Handle registers handler for method at pattern, using net/http's native
// "METHOD /path/{name}" pattern syntax.
func (b *Backend) Handle(method, pattern string, handler http.Handler) {
	b.mux.Handle(method+" "+pattern, handler)
}

// PathValue returns the named path parameter, resolved by the ServeMux from the
// matched pattern.
func (b *Backend) PathValue(req *http.Request, name string) string {
	return req.PathValue(name)
}

// Handler returns the composed http.Handler: the standard middleware stack and
// any user middleware wrapped around the mux.
func (b *Backend) Handler() http.Handler {
	b.sealed.Store(true)

	b.once.Do(func() {
		all := make([]func(http.Handler) http.Handler, 0, len(b.standard)+len(b.user))
		all = append(all, b.standard...)
		all = append(all, b.user...)
		b.built = httpmw.Chain(b.mux, all...)
	})

	return b.built
}

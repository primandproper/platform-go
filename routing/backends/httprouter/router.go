// Package httprouter provides a routing.Backend built on
// julienschmidt/httprouter, a fast radix-tree router. httprouter uses ":name"
// path parameters, so the "/users/{id}" patterns the routing layer produces are
// rewritten to "/users/:id" at registration; path values are read back from the
// request context httprouter populates. The shared observability, recovery,
// CORS, and OpenTelemetry middleware stack is applied around the router,
// matching the chi backend's behavior.
package httprouter

import (
	"net/http"
	"sync"

	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"
	"github.com/primandproper/platform-go/v5/routing"
	"github.com/primandproper/platform-go/v5/routing/backends/internal/httpmw"

	hr "github.com/julienschmidt/httprouter"
)

var _ routing.Backend = (*backend)(nil)

// backend is a julienschmidt/httprouter implementation of routing.Backend.
// Global middleware is composed around the router lazily in Handler; because the
// router is wrapped by reference, routes registered after the first Handler call
// are still served.
type backend struct {
	built    http.Handler
	router   *hr.Router
	standard []func(http.Handler) http.Handler
	user     []func(http.Handler) http.Handler
	once     sync.Once
}

// NewBackend constructs an httprouter-backed routing.Backend with the standard
// middleware and OpenTelemetry stack installed. Pass it to routing.New. Panics
// in handlers propagate to the shared recovery middleware, so no httprouter
// PanicHandler is installed.
func NewBackend(logger logging.Logger, tracerProvider tracing.TracerProvider, metricProvider metrics.Provider, cfg *Config) routing.Backend {
	tracerProvider = tracing.EnsureTracerProvider(tracerProvider)
	o11y := observability.NewObserver("router", logging.EnsureLogger(logger), tracerProvider)

	return &backend{
		router: hr.New(),
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
// any time before Handler.
func (b *backend) Use(middleware ...routing.Middleware) {
	b.user = append(b.user, httpmw.Convert(middleware...)...)
}

// Handle registers handler for method at pattern, rewriting the "{name}"
// placeholders to httprouter's ":name" form.
func (b *backend) Handle(method, pattern string, handler http.Handler) {
	b.router.Handler(method, httpmw.ColonParams(pattern), handler)
}

// PathValue returns the named path parameter from the httprouter params stored
// on the request context.
func (b *backend) PathValue(req *http.Request, name string) string {
	return hr.ParamsFromContext(req.Context()).ByName(name)
}

// Handler returns the composed http.Handler: the standard middleware stack and
// any user middleware wrapped around the router.
func (b *backend) Handler() http.Handler {
	b.once.Do(func() {
		all := make([]func(http.Handler) http.Handler, 0, len(b.standard)+len(b.user))
		all = append(all, b.standard...)
		all = append(all, b.user...)
		b.built = httpmw.Chain(b.router, all...)
	})

	return b.built
}

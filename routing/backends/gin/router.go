// Package gin provides a routing.Backend built on gin-gonic/gin. gin uses
// ":name" path parameters, so the "/users/{id}" patterns the routing layer
// produces are rewritten to "/users/:id" at registration. gin keeps path
// parameters on its own *gin.Context rather than the request context, so each
// registered handler stashes them onto the request context where PathValue can
// read them. The shared observability, recovery, CORS, and OpenTelemetry
// middleware stack is applied around the gin engine (which is itself an
// http.Handler), matching the chi backend's behavior; gin's own logger and
// recovery middleware are intentionally not installed.
package gin

import (
	"context"
	"net/http"
	"sync"

	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	"github.com/primandproper/platform-go/v6/observability/tracing"
	"github.com/primandproper/platform-go/v6/routing"
	"github.com/primandproper/platform-go/v6/routing/backends/internal/httpmw"

	"github.com/gin-gonic/gin"
)

var _ routing.Backend = (*backend)(nil)

// paramsCtxKey is the private context key under which the gin path parameters
// are stored so PathValue can retrieve them from a bare *http.Request.
type paramsCtxKey struct{}

// backend is a gin-gonic/gin implementation of routing.Backend. Global
// middleware is composed around the engine lazily in Handler; because the engine
// is wrapped by reference, routes registered after the first Handler call are
// still served.
type backend struct {
	built    http.Handler
	engine   *gin.Engine
	standard []func(http.Handler) http.Handler
	user     []func(http.Handler) http.Handler
	once     sync.Once
}

// NewBackend constructs a gin-backed routing.Backend with the standard
// middleware and OpenTelemetry stack installed. Pass it to routing.New.
//
// It sets gin to release mode, a process-global setting, to silence gin's
// debug-mode route logging; the platform logging middleware provides request
// logs instead.
func NewBackend(logger logging.Logger, tracerProvider tracing.TracerProvider, metricProvider metrics.Provider, cfg *Config) routing.Backend {
	gin.SetMode(gin.ReleaseMode)

	tracerProvider = tracing.EnsureTracerProvider(tracerProvider)
	o11y := observability.NewObserver("router", logging.EnsureLogger(logger), tracerProvider)

	// gin.New, not gin.Default: recovery and logging come from the shared stack.
	return &backend{
		engine: gin.New(),
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
// placeholders to gin's ":name" form. The gin path parameters are copied onto
// the request context so PathValue can resolve them.
func (b *backend) Handle(method, pattern string, handler http.Handler) {
	b.engine.Handle(method, httpmw.ColonParams(pattern), func(c *gin.Context) {
		req := c.Request
		if len(c.Params) > 0 {
			req = req.WithContext(context.WithValue(req.Context(), paramsCtxKey{}, c.Params))
		}

		handler.ServeHTTP(c.Writer, req)
	})
}

// PathValue returns the named path parameter from the gin params stashed on the
// request context by Handle.
func (b *backend) PathValue(req *http.Request, name string) string {
	if params, ok := req.Context().Value(paramsCtxKey{}).(gin.Params); ok {
		return params.ByName(name)
	}

	return ""
}

// Handler returns the composed http.Handler: the standard middleware stack and
// any user middleware wrapped around the gin engine.
func (b *backend) Handler() http.Handler {
	b.once.Do(func() {
		all := make([]func(http.Handler) http.Handler, 0, len(b.standard)+len(b.user))
		all = append(all, b.standard...)
		all = append(all, b.user...)
		b.built = httpmw.Chain(b.engine, all...)
	})

	return b.built
}

// Package gin provides a routing.Backend built on gin-gonic/gin. gin uses
// ":name" path parameters, so the "/users/{id}" patterns the routing layer
// produces are rewritten to "/users/:id" at registration. gin keeps path
// parameters on its own *gin.Context rather than the request context, so each
// registered handler stashes them onto the request context where PathValue can
// read them. The engine is set to match on the escaped path, so a
// percent-escaped path value survives the round trip; the decoding gin would
// otherwise do for itself is done in PathValue instead. The shared
// observability, recovery, CORS, and OpenTelemetry middleware stack is applied
// around the gin engine (which is itself an http.Handler), matching the chi
// backend's behavior; gin's own logger and recovery middleware are intentionally
// not installed.
package gin

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/routing/backends/internal/httpmw"
	"github.com/primandproper/platform-go/v13/routing/backends/internal/pathvalues"

	"github.com/gin-gonic/gin"
)

var _ routing.Backend = (*Backend)(nil)

// paramsCtxKey is the private context key under which the gin path parameters
// are stored so PathValue can retrieve them from a bare *http.Request.
type paramsCtxKey struct{}

// Backend is a gin-gonic/gin implementation of routing.Backend. Global
// middleware is composed around the engine lazily in Handler; because the engine
// is wrapped by reference, routes registered after the first Handler call are
// still served.
//
// It is exported, and returned by NewBackend, so a caller who has chosen gin can
// depend on that choice rather than on the seam every router backend shares.
type Backend struct {
	built    http.Handler
	engine   *gin.Engine
	standard []func(http.Handler) http.Handler
	user     []func(http.Handler) http.Handler
	once     sync.Once
	sealed   atomic.Bool
}

// NewBackend constructs a gin-backed routing.Backend with the standard
// middleware and OpenTelemetry stack installed. Pass it to routing.New.
//
// It sets gin to release mode, a process-global setting, to silence gin's
// debug-mode route logging; the platform logging middleware provides request
// logs instead.
func NewBackend(cfg *Config, opts ...Option) *Backend {
	// A nil config is the zero config, not a panic. The config subpackage
	// dispatches on Provider and hands whichever sub-config happens to be set —
	// which is nil unless the deployment filled that provider's section in, so
	// every backend here got one on a perfectly ordinary configuration.
	if cfg == nil {
		cfg = &Config{}
	}

	gin.SetMode(gin.ReleaseMode)

	o := newOptions(opts)
	tracerProvider := tracing.EnsureTracerProvider(o.tracerProvider)
	o11y := observability.NewObserver("router", logging.EnsureLogger(o.logger), tracerProvider)

	// gin.New, not gin.Default: recovery and logging come from the shared stack.
	engine := gin.New()

	// Match on the escaped path, so a percent-escaped separator stays inside its
	// segment instead of splitting it and routing to nothing. gin's default is
	// URL.Path, already decoded, which is what makes "/things/a%2Fb" a 404
	// against "/things/:slug".
	//
	// UseEscapedPath and not UseRawPath: the latter only takes effect when
	// URL.RawPath happens to be set, which net/url leaves empty whenever nothing
	// needed escaping, whereas URL.EscapedPath() always yields the escaped form —
	// the same string net/http's ServeMux matches on. UseEscapedPath also
	// overrides UseRawPath, so this is the only knob in play.
	engine.UseEscapedPath = true

	// Decode the captured values in PathValue rather than letting gin do it here:
	// gin unescapes with url.QueryUnescape, which reads a literal '+' in a path
	// segment as a space. A path is not a query, and the other backends leave it
	// alone.
	engine.UnescapePathValues = false

	return &Backend{
		engine: engine,
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
// any time before Handler.
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

// Handle registers handler for method at pattern, rewriting the "{name}"
// placeholders to gin's ":name" form. The gin path parameters are copied onto
// the request context so PathValue can resolve them.
func (b *Backend) Handle(method, pattern string, handler http.Handler) {
	b.engine.Handle(method, httpmw.ColonParams(pattern), func(c *gin.Context) {
		req := c.Request
		if len(c.Params) > 0 {
			req = req.WithContext(context.WithValue(req.Context(), paramsCtxKey{}, c.Params))
		}

		handler.ServeHTTP(c.Writer, req)
	})
}

// PathValue returns the named path parameter from the gin params stashed on the
// request context by Handle, decoded. The engine matches on the escaped path, so
// what it captured is an escaped segment.
func (b *Backend) PathValue(req *http.Request, name string) string {
	if params, ok := req.Context().Value(paramsCtxKey{}).(gin.Params); ok {
		return pathvalues.Decode(params.ByName(name))
	}

	return ""
}

// Handler returns the composed http.Handler: the standard middleware stack and
// any user middleware wrapped around the gin engine.
func (b *Backend) Handler() http.Handler {
	b.sealed.Store(true)

	b.once.Do(func() {
		all := make([]func(http.Handler) http.Handler, 0, len(b.standard)+len(b.user))
		all = append(all, b.standard...)
		all = append(all, b.user...)
		b.built = httpmw.Chain(b.engine, all...)
	})

	return b.built
}

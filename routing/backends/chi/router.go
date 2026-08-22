// Package chi provides a routing.Backend built on go-chi/chi. chi consumes the
// "/users/{id}" placeholder syntax directly, and already matches on the escaped
// request path, so a percent-escaped path value reaches the right route — but it
// stores the matched segment verbatim, so PathValue decodes it before handing it
// back.
//
// Middleware is installed through chi.Router.Use, which takes the same plain
// func(http.Handler) http.Handler the other backends chain by hand, so this
// backend installs internal/httpmw's stack rather than a copy of it. What is
// genuinely chi's own is the OpenTelemetry step: see otelMiddleware.
package chi

import (
	"net/http"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/routing/backends/internal/httpmw"
	"github.com/primandproper/platform-go/v13/routing/backends/internal/pathvalues"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	servertiming "github.com/mitchellh/go-server-timing"
)

var _ routing.Backend = (*Backend)(nil)

// Backend is a chi-based implementation of routing.Backend. It is exported, and
// returned by NewBackend, so a caller who has chosen chi can depend on that
// choice rather than on the seam every router backend shares.
type Backend struct {
	mux chi.Router
}

func buildChiMux(
	o11y observability.Observer,
	metricProvider metrics.Provider,
	cfg *Config,
) chi.Router {
	mux := chi.NewRouter()

	// The order matches httpmw.Standard, with two chi-only additions: the otel
	// step (see otel.go), and CleanPath, which needs chi's own RouteContext and
	// so cannot live in the shared stack.
	stack := []func(http.Handler) http.Handler{
		// RequestID and RealIP must run before the observability middleware so that
		// logs and spans see the request ID and the real client IP, not the proxy's.
		chimiddleware.RequestID,
		chimiddleware.RealIP,
		httpmw.Recovery(o11y),
	}
	stack = append(stack, otelMiddleware(cfg.ServiceName, metricProvider)...)
	stack = append(stack,
		httpmw.Logging(o11y, cfg.SilenceRouteLogging),
		chimiddleware.CleanPath,
		chimiddleware.Timeout(httpmw.MaxTimeout),
		httpmw.CORS(o11y, cfg.ValidDomains, cfg.EnableCORSForLocalhost),
		func(next http.Handler) http.Handler {
			return servertiming.Middleware(next, nil)
		},
	)

	mux.Use(stack...)

	// all middleware must be defined before routes on a mux.

	return mux
}

// NewBackend constructs a chi-backed routing.Backend with the standard middleware
// and OpenTelemetry stack installed. Pass it to routing.New.
func NewBackend(cfg *Config, opts ...Option) *Backend {
	// A nil config is the zero config, not a panic. The config subpackage
	// dispatches on Provider and hands whichever sub-config happens to be set —
	// which is nil unless the deployment filled that provider's section in, so
	// every backend here got one on a perfectly ordinary configuration.
	if cfg == nil {
		cfg = &Config{}
	}

	o := newOptions(opts)
	o11y := observability.NewObserver("router", logging.EnsureLogger(o.logger), tracing.EnsureTracerProvider(o.tracerProvider))

	return &Backend{
		mux: buildChiMux(o11y, metrics.EnsureMetricsProvider(o.metricsProvider), cfg),
	}
}

// Use installs global middleware. It must be called before Handle (chi forbids
// adding middleware once routes are registered).
func (b *Backend) Use(middleware ...routing.Middleware) {
	b.mux.Use(httpmw.Convert(middleware...)...)
}

// Handle registers handler for method at pattern.
func (b *Backend) Handle(method, pattern string, handler http.Handler) {
	b.mux.Method(method, pattern, handler)
}

// PathValue returns the named chi URL parameter from the request, decoded.
//
// chi matches on URL.RawPath whenever it is set, which is the half of the
// contract that keeps an escaped separator inside its segment, but it stores the
// matched segment verbatim and never decodes it — chi.URLParam on its own hands
// a handler "a%2Fb" where the caller wrote "a/b", and a typed parameter then
// fails to bind on text nobody sent.
//
// The decode is conditional on the same RawPath chi decided with. net/url leaves
// RawPath empty when escaping URL.Path reproduces what arrived, and in that case
// chi matched on the decoded path and the segment it captured is already the
// answer. A request for "a%252Fb" is exactly that: the value is "a%2Fb", chi
// hands back "a%2Fb", and decoding it again would quietly turn it into "a/b".
func (b *Backend) PathValue(req *http.Request, name string) string {
	value := chi.URLParam(req, name)
	if req.URL.RawPath == "" {
		return value
	}

	return pathvalues.Decode(value)
}

// Handler returns the underlying chi mux.
func (b *Backend) Handler() http.Handler {
	return b.mux
}

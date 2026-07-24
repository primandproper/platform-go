package httpmw

import (
	"net/http"
	"net/url"
	"slices"

	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/tracing"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	servertiming "github.com/mitchellh/go-server-timing"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/metric"
)

// StackConfig carries the configuration the shared middleware stack needs. Each
// backend maps its own Config onto this so the stack stays backend-agnostic.
type StackConfig struct {
	TracerProvider         tracing.TracerProvider
	MeterProvider          metric.MeterProvider
	ServiceName            string
	ValidDomains           []string
	EnableCORSForLocalhost bool
	SilenceRouteLogging    bool
}

// Standard returns the ordered middleware every non-chi backend installs around
// its mux, matching the chi backend's stack: request ID and real client IP
// first (so logs and spans see them), then recovery, OpenTelemetry
// tracing/metrics, request logging, a request timeout, CORS, and server-timing.
// The slice is ordered outermost-first for Chain.
//
// Unlike the chi backend, path cleaning is not included: chi's CleanPath
// middleware requires chi's own RouteContext in the request context and panics
// under any other mux. Each backend here delegates path normalization to the
// underlying router (net/http.ServeMux redirects unclean paths; httprouter and
// gin normalize via their redirect options).
func Standard(o11y observability.Observer, cfg *StackConfig) []func(http.Handler) http.Handler {
	return []func(http.Handler) http.Handler{
		// RequestID and RealIP must run before the observability middleware so that
		// logs and spans see the request ID and the real client IP, not the proxy's.
		chimiddleware.RequestID,
		chimiddleware.RealIP,
		Recovery(o11y),
		otelMiddleware(cfg),
		Logging(o11y, cfg.SilenceRouteLogging),
		chimiddleware.Timeout(MaxTimeout),
		CORS(o11y, cfg.ValidDomains, cfg.EnableCORSForLocalhost),
		func(next http.Handler) http.Handler {
			return servertiming.Middleware(next, nil)
		},
	}
}

// otelMiddleware wraps a handler with otelhttp so requests are traced and the
// standard HTTP server metrics (duration, in-flight, response size) are
// recorded. Health checks are skipped to avoid probe noise. The span name is
// the HTTP method; per-route naming would require route information the generic
// otelhttp wrapper does not have.
func otelMiddleware(cfg *StackConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(
			next,
			cfg.ServiceName,
			otelhttp.WithTracerProvider(tracing.EnsureTracerProvider(cfg.TracerProvider)),
			otelhttp.WithMeterProvider(cfg.MeterProvider),
			otelhttp.WithFilter(func(r *http.Request) bool {
				// Skip tracing for health checks to avoid noise from load balancers, K8s probes, etc.
				return !IsHealthCheck(r.URL.Path)
			}),
			otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				return r.Method
			}),
		)
	}
}

// CORS builds the shared CORS middleware. Credentialed requests are allowed, so
// non-localhost origins must be https and their host must be in validDomains;
// localhost may use http when enableLocalhost is set. The behavior mirrors the
// chi backend exactly.
func CORS(o11y observability.Observer, validDomains []string, enableLocalhost bool) func(http.Handler) http.Handler {
	return cors.New(cors.Options{
		AllowOriginFunc: func(_ *http.Request, origin string) bool {
			u, err := url.Parse(origin)
			if err != nil {
				return false
			}

			host := u.Hostname()
			isLocalhost := host == "localhost" || host == "127.0.0.1"
			allowedHost := slices.Contains(validDomains, host) || (enableLocalhost && isLocalhost)

			// Since credentials are allowed, require https for non-localhost origins so
			// credentialed requests aren't accepted from a plaintext origin on the same
			// hostname.
			secureScheme := u.Scheme == "https" || (isLocalhost && u.Scheme == "http")

			result := allowedHost && secureScheme
			o11y.Logger().WithValue("origin", origin).WithValue("result", result).Debug("CORS origin check")

			return result
		},
		AllowedMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodPatch,
			http.MethodPut,
			http.MethodDelete,
			http.MethodOptions,
		},
		AllowedHeaders: []string{"*"},
		// nil, not []string{""}, which emitted an empty Access-Control-Expose-Headers.
		ExposedHeaders:   nil,
		AllowCredentials: true,
		MaxAge:           MaxCORSAge,
	}).Handler
}

package chi

import (
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"
	"github.com/primandproper/platform-go/v5/routing"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	servertiming "github.com/mitchellh/go-server-timing"
	"github.com/riandyrn/otelchi"
	otelchimetric "github.com/riandyrn/otelchi/metric"
)

const (
	maxTimeout = 120 * time.Second
	maxCORSAge = 300
)

var _ routing.Backend = (*backend)(nil)

// backend is a chi-based implementation of routing.Backend.
type backend struct {
	mux chi.Router
}

func buildChiMux(
	o11y observability.Observer,
	metricProvider metrics.Provider,
	cfg *Config,
) chi.Router {
	corsHandler := cors.New(cors.Options{
		AllowOriginFunc: func(_ *http.Request, origin string) bool {
			u, err := url.Parse(origin)
			if err != nil {
				return false
			}

			host := u.Hostname()
			isLocalhost := host == "localhost" || host == "127.0.0.1"
			allowedHost := slices.Contains(cfg.ValidDomains, host) || (cfg.EnableCORSForLocalhost && isLocalhost)

			// Since credentials are allowed, require https for non-localhost origins so
			// credentialed requests aren't accepted from a plaintext origin on the same
			// hostname (the previous check compared Hostname() only, ignoring scheme/port).
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
		MaxAge:           maxCORSAge,
	})

	baseCfg := otelchimetric.NewBaseConfig(
		cfg.ServiceName,
		otelchimetric.WithMeterProvider(metricProvider.MeterProvider()),
	)

	mux := chi.NewRouter()
	mux.Use(
		// RequestID and RealIP must run before the observability middleware so that
		// logs and spans see the request ID and the real client IP, not the proxy's.
		chimiddleware.RequestID,
		chimiddleware.RealIP,
		buildRecoveryMiddleware(o11y),
		otelchimetric.NewRequestDurationMillis(baseCfg),
		otelchimetric.NewRequestInFlight(baseCfg),
		otelchimetric.NewResponseSizeBytes(baseCfg),
		otelchi.Middleware(
			cfg.ServiceName,
			otelchi.WithRequestMethodInSpanName(true),
			otelchi.WithTraceResponseHeaders(otelchi.TraceHeaderConfig{
				TraceIDHeader:      "X-Trace-ID",
				TraceSampledHeader: "X-Trace-Sampled",
			}),
			otelchi.WithFilter(func(r *http.Request) bool {
				// Skip tracing for health checks to avoid noise from load balancers, K8s probes, etc.
				return !isHealthCheck(r.URL.Path)
			}),
		),
		buildLoggingMiddleware(o11y, cfg.SilenceRouteLogging),
		chimiddleware.CleanPath,
		chimiddleware.Timeout(maxTimeout),
		corsHandler.Handler,
		func(next http.Handler) http.Handler {
			return servertiming.Middleware(next, nil)
		},
	)

	// all middleware must be defined before routes on a mux.

	return mux
}

// NewBackend constructs a chi-backed routing.Backend with the standard middleware
// and OpenTelemetry stack installed. Pass it to routing.New.
func NewBackend(logger logging.Logger, tracerProvider tracing.TracerProvider, metricProvider metrics.Provider, cfg *Config) routing.Backend {
	o11y := observability.NewObserver("router", logging.EnsureLogger(logger), tracing.EnsureTracerProvider(tracerProvider))

	return &backend{
		mux: buildChiMux(o11y, metrics.EnsureMetricsProvider(metricProvider), cfg),
	}
}

func convertMiddleware(in ...routing.Middleware) []func(http.Handler) http.Handler {
	out := make([]func(http.Handler) http.Handler, 0, len(in))
	for _, x := range in {
		if x != nil {
			out = append(out, x)
		}
	}

	return out
}

// Use installs global middleware. It must be called before Handle (chi forbids
// adding middleware once routes are registered).
func (b *backend) Use(middleware ...routing.Middleware) {
	b.mux.Use(convertMiddleware(middleware...)...)
}

// Handle registers handler for method at pattern.
func (b *backend) Handle(method, pattern string, handler http.Handler) {
	b.mux.Method(method, pattern, handler)
}

// PathValue returns the named chi URL parameter from the request.
func (b *backend) PathValue(req *http.Request, name string) string {
	return chi.URLParam(req, name)
}

// Handler returns the underlying chi mux.
func (b *backend) Handler() http.Handler {
	return b.mux
}

package chi

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"
	"github.com/primandproper/platform-go/v4/routing"

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

var (
	errInvalidMethod = errors.New("invalid method")
)

var _ routing.Router = (*router)(nil)

type router struct {
	router chi.Router
	// we hold onto this to create subrouters with
	cfg            *Config
	o11y           observability.Observer
	tracerProvider tracing.TracerProvider
	metricProvider metrics.Provider
}

func buildChiMux(
	o11y observability.Observer,
	metricProvider metrics.Provider,
	cfg *Config,
) chi.Router {
	corsHandler := cors.New(cors.Options{
		AllowOriginFunc: func(r *http.Request, origin string) bool {
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

func buildRouter(mux chi.Router, l logging.Logger, tracerProvider tracing.TracerProvider, metricProvider metrics.Provider, cfg *Config) *router {
	o11y := observability.NewObserver("router", logging.EnsureLogger(l), tracerProvider)

	if mux == nil {
		o11y.Logger().Debug("starting with a new mux")
		mux = buildChiMux(o11y, metricProvider, cfg)
	}

	r := &router{
		router:         mux,
		o11y:           o11y,
		tracerProvider: tracerProvider,
		metricProvider: metricProvider,
		cfg:            cfg,
	}

	return r
}

func convertMiddleware(in ...routing.Middleware) []func(handler http.Handler) http.Handler {
	out := []func(handler http.Handler) http.Handler{}

	for _, x := range in {
		if x != nil {
			out = append(out, x)
		}
	}

	return out
}

// NewRouter constructs a new router.
func NewRouter(logger logging.Logger, tracerProvider tracing.TracerProvider, metricProvider metrics.Provider, cfg *Config) routing.Router {
	return buildRouter(nil, logger, tracerProvider, metricProvider, cfg)
}

func (r *router) clone() *router {
	return buildRouter(
		r.router,
		r.o11y.Logger(),
		tracing.EnsureTracerProvider(r.tracerProvider),
		metrics.EnsureMetricsProvider(r.metricProvider),
		r.cfg,
	)
}

// WithMiddleware returns a router with certain middleware applied.
func (r *router) WithMiddleware(middleware ...routing.Middleware) routing.Router {
	x := r.clone()

	x.router = x.router.With(convertMiddleware(middleware...)...)

	return x
}

// Routes returns the described routes.
func (r *router) Routes() []*routing.Route {
	output := []*routing.Route{}

	routerWalkFunc := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		output = append(output, &routing.Route{
			Method: method,
			Path:   route,
		})

		return nil
	}

	if err := chi.Walk(r.router, routerWalkFunc); err != nil {
		r.o11y.Logger().Error("logging routes", err)
	}

	return output
}

// Route lets you apply a set of routes to a subrouter with a provided pattern.
func (r *router) Route(pattern string, routeFunction func(r routing.Router)) routing.Router {
	r.router.Route(pattern, func(subrouter chi.Router) {
		routeFunction(buildRouter(
			subrouter,
			r.o11y.Logger(),
			tracing.EnsureTracerProvider(r.tracerProvider),
			metrics.EnsureMetricsProvider(r.metricProvider),
			r.cfg,
		))
	})

	return r
}

// AddRoute adds a route to the router.
func (r *router) AddRoute(method, path string, handler http.HandlerFunc, middleware ...routing.Middleware) error {
	switch strings.TrimSpace(strings.ToUpper(method)) {
	case http.MethodGet:
		r.router.With(convertMiddleware(middleware...)...).Get(path, handler)
	case http.MethodHead:
		r.router.With(convertMiddleware(middleware...)...).Head(path, handler)
	case http.MethodPost:
		r.router.With(convertMiddleware(middleware...)...).Post(path, handler)
	case http.MethodPut:
		r.router.With(convertMiddleware(middleware...)...).Put(path, handler)
	case http.MethodPatch:
		r.router.With(convertMiddleware(middleware...)...).Patch(path, handler)
	case http.MethodDelete:
		r.router.With(convertMiddleware(middleware...)...).Delete(path, handler)
	case http.MethodConnect:
		r.router.With(convertMiddleware(middleware...)...).Connect(path, handler)
	case http.MethodOptions:
		r.router.With(convertMiddleware(middleware...)...).Options(path, handler)
	case http.MethodTrace:
		r.router.With(convertMiddleware(middleware...)...).Trace(path, handler)
	default:
		return errors.Wrapf(errInvalidMethod, "%s", method)
	}

	return nil
}

// Handler our interface by wrapping the underlying router's Handler method.
func (r *router) Handler() http.Handler {
	return r.router
}

// Handle our interface by wrapping the underlying router's Handle method.
func (r *router) Handle(pattern string, handler http.Handler) {
	r.router.Handle(pattern, handler)
}

// HandleFunc satisfies our interface by wrapping the underlying router's HandleFunc method.
func (r *router) HandleFunc(pattern string, handler http.HandlerFunc) {
	r.router.HandleFunc(pattern, handler)
}

// Connect satisfies our interface by wrapping the underlying router's Connect method.
func (r *router) Connect(pattern string, handler http.HandlerFunc) {
	r.router.Connect(pattern, handler)
}

// Delete satisfies our interface by wrapping the underlying router's Delete method.
func (r *router) Delete(pattern string, handler http.HandlerFunc) {
	r.router.Delete(pattern, handler)
}

// Get satisfies our interface by wrapping the underlying router's Get method.
func (r *router) Get(pattern string, handler http.HandlerFunc) {
	r.router.Get(pattern, handler)
}

// Head satisfies our interface by wrapping the underlying router's Head method.
func (r *router) Head(pattern string, handler http.HandlerFunc) {
	r.router.Head(pattern, handler)
}

// Options satisfies our interface by wrapping the underlying router's Options method.
func (r *router) Options(pattern string, handler http.HandlerFunc) {
	r.router.Options(pattern, handler)
}

// Patch satisfies our interface by wrapping the underlying router's Patch method.
func (r *router) Patch(pattern string, handler http.HandlerFunc) {
	r.router.Patch(pattern, handler)
}

// Post satisfies our interface by wrapping the underlying router's Post method.
func (r *router) Post(pattern string, handler http.HandlerFunc) {
	r.router.Post(pattern, handler)
}

// Put satisfies our interface by wrapping the underlying router's Put method.
func (r *router) Put(pattern string, handler http.HandlerFunc) {
	r.router.Put(pattern, handler)
}

// Trace satisfies our interface by wrapping the underlying router's Trace method.
func (r *router) Trace(pattern string, handler http.HandlerFunc) {
	r.router.Trace(pattern, handler)
}

// BuildRouteParamIDFetcher builds a function that fetches a given key from a path with variables added by a router.
func (r *router) BuildRouteParamIDFetcher(logger logging.Logger, key, logDescription string) func(req *http.Request) uint64 {
	return buildRouteParamIDFetcher(logger, key, logDescription)
}

// BuildRouteParamStringIDFetcher builds a function that fetches a given key from a path with variables added by a router.
func (r *router) BuildRouteParamStringIDFetcher(key string) func(req *http.Request) string {
	return func(req *http.Request) string {
		return chi.URLParam(req, key)
	}
}

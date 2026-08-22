package http

import (
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/routing"
)

// DefaultMaxBodySize bounds how much of an unverified request body the
// middleware will read.
//
// One mebibyte, and the number matters: the body has to be buffered whole
// before a signature over it can be checked, which means an unauthenticated
// caller decides how much memory this guard allocates. A cap is the only thing
// standing between that and a request that costs the process more than the
// endpoint behind it would have.
const DefaultMaxBodySize int64 = 1 << 20

type (
	// Option configures the middleware.
	Option func(*config)

	config struct {
		errEncoder routing.ErrorEncoder

		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		maxBodySize int64
	}
)

func newConfig(opts ...Option) *config {
	cfg := &config{maxBodySize: DefaultMaxBodySize}

	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

// WithMaxBodySize overrides DefaultMaxBodySize — how many bytes of an
// unverified body the middleware will buffer in order to check a signature
// over it. A request whose body exceeds it is rejected unverified.
//
// Raise it for an endpoint that legitimately receives large signed payloads,
// and only that endpoint: install the middleware per route with
// routing.WithMiddleware rather than lifting the cap for the whole surface. A
// non-positive size leaves the default in place; there is no unlimited setting,
// because the cap is what makes the buffering safe.
func WithMaxBodySize(size int64) Option {
	return func(c *config) {
		if size > 0 {
			c.maxBodySize = size
		}
	}
}

// WithErrorEncoder renders the rejection the way the service renders every
// other error. Pass the same routing.ErrorEncoder the Router was built with: a
// service that replaced the platform envelope did so because its clients parse
// something else, and a 401 that arrives in a shape they cannot parse is a
// rejection they cannot act on.
//
// Without it the platform APIError envelope is used, which is what the Router
// itself produces for a handler that returned
// requestsigning.ErrInvalidSignature. Both paths run through
// routing.DefaultErrorBody, so the two cannot drift.
func WithErrorEncoder(encoder routing.ErrorEncoder) Option {
	return func(c *config) {
		if encoder != nil {
			c.errEncoder = encoder
		}
	}
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(c *config) { c.logger = logger }
}

// WithTracerProvider attaches a tracer provider.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(c *config) { c.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider, enabling the middleware's
// verified, rejected, and error counters.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(c *config) { c.metricsProvider = metricsProvider }
}

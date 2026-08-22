package http

import (
	"time"

	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/routing"
)

const (
	// RetryAfterHeader is the response header carrying the refusal's retry
	// hint, in whole seconds.
	RetryAfterHeader = "Retry-After"

	// DefaultRetryAfter is the hint sent when the limiter cannot compute one.
	// It is short on purpose: a client that comes back too early is refused
	// again and told a better number, whereas one parked too long has had
	// capacity taken from it that was never actually scarce.
	DefaultRetryAfter = time.Second
)

type (
	// Option configures the middleware.
	Option func(*config)

	config struct {
		errEncoder routing.ErrorEncoder

		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		retryAfter time.Duration
		failClosed bool
	}
)

func newConfig(opts ...Option) *config {
	cfg := &config{retryAfter: DefaultRetryAfter}

	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

// WithErrorEncoder renders the refusal the way the service renders every other
// error. Pass the same routing.ErrorEncoder the Router was built with: a
// service that replaced the platform envelope did so because its clients parse
// something else, and a 429 that arrives in a shape they cannot parse is a
// refusal they cannot act on.
//
// Without it the platform APIError envelope is used, which is what the Router
// itself produces for a handler that returned ratelimiting.ErrRateLimited. Both
// paths run through routing.DefaultErrorBody, so the two cannot drift.
func WithErrorEncoder(encoder routing.ErrorEncoder) Option {
	return func(c *config) {
		if encoder != nil {
			c.errEncoder = encoder
		}
	}
}

// WithRetryAfter sets the hint sent when the limiter volunteers none.
//
// A limiter that implements ratelimiting.RetryHinter answers for itself and
// this value is never used for it; the in-memory and Redis limiters both do.
// It is the fallback for the ones that cannot — and for a limiter that has no
// estimate for the key in front of it.
//
// A non-positive duration leaves DefaultRetryAfter in place. To suppress the
// header entirely for unhinted refusals, use WithoutFallbackRetryAfter.
func WithRetryAfter(after time.Duration) Option {
	return func(c *config) {
		if after > 0 {
			c.retryAfter = after
		}
	}
}

// WithoutFallbackRetryAfter suppresses Retry-After on refusals the limiter
// could not put a number to, rather than sending the fallback.
//
// Reach for it when clients treat the header as authoritative and a guess would
// mislead them more than silence would.
func WithoutFallbackRetryAfter() Option {
	return func(c *config) { c.retryAfter = 0 }
}

// WithFailClosed refuses requests the limiter could not rule on, instead of
// letting them through.
//
// The default is the other way round. A limiter that cannot answer — Redis
// unreachable, a key extractor that failed — is a fault in a guard, not a
// verdict from it, and failing closed turns a dependency's bad minute into a
// total outage of the thing being guarded. The refusals are counted and logged
// at error either way, so an operator sees the fault rather than inferring it
// from traffic.
//
// Use this where the limiter is the only thing standing between an endpoint and
// abuse it cannot absorb — a login route, an expensive unauthenticated
// endpoint — and where refusing everyone is genuinely better than admitting
// everyone.
func WithFailClosed() Option {
	return func(c *config) { c.failClosed = true }
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
// allowed, refused, and error counters.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(c *config) { c.metricsProvider = metricsProvider }
}

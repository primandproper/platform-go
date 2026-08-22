package grpc

import (
	"time"

	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// DefaultRetryAfter is the hint attached when the limiter cannot compute one.
// It matches the HTTP middleware's default, and is short for the same reason:
// a client back too early is refused again and told a better number, whereas
// one parked too long has had capacity taken from it that was never scarce.
const DefaultRetryAfter = time.Second

type (
	// Option configures the interceptor.
	Option func(*config)

	config struct {
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

// WithRetryAfter sets the RetryInfo delay attached when the limiter volunteers
// none. A limiter implementing ratelimiting.RetryHinter answers for itself and
// this is never used for it; the in-memory and Redis limiters both do.
//
// A non-positive duration leaves DefaultRetryAfter in place. To attach nothing
// for unhinted refusals, use WithoutFallbackRetryAfter.
func WithRetryAfter(after time.Duration) Option {
	return func(c *config) {
		if after > 0 {
			c.retryAfter = after
		}
	}
}

// WithoutFallbackRetryAfter omits RetryInfo on refusals the limiter could not
// put a number to, rather than attaching the fallback. gRPC clients that
// retry on RetryInfo treat it as authoritative, and a guess can be worse than
// silence.
func WithoutFallbackRetryAfter() Option {
	return func(c *config) { c.retryAfter = 0 }
}

// WithFailClosed refuses RPCs the limiter could not rule on, instead of letting
// them through. See the HTTP middleware's option of the same name for why the
// default runs the other way.
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

// WithMetricsProvider attaches a metrics provider, enabling the interceptor's
// allowed, refused, and error counters.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(c *config) { c.metricsProvider = metricsProvider }
}

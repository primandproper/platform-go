package memory

import (
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures the in-memory Locker this package constructs. The zero
// configuration works: an absent logger logs nowhere, an absent tracer
// provider traces nowhere, an absent metrics provider records nothing, and an
// absent clock reads the wall clock.
type Option func(*options)

type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	clock           clock.Clock
}

func newOptions(opts []Option) *options {
	cfg := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on every lock
// operation.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the locker's counters
// and latency histogram.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithClock swaps the source of time this Locker makes its TTL decisions
// against. An absent clock reads the wall clock.
//
// Expiry is the whole of this backend's semantics — whether a key is still held,
// whether a release still owns it, what a refresh extends to — and all of it ran
// off time.Now(), so a test for any of it had to sleep through a real TTL. The
// siblings that talk to Redis and Postgres delegate expiry to the server; this
// one implements it, which is exactly why it is the one that needs a clock.
func WithClock(c clock.Clock) Option {
	return func(o *options) { o.clock = c }
}

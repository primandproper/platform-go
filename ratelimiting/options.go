package ratelimiting

import (
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures the in-memory rate limiter this package constructs. The
// zero configuration works: an absent metrics provider records nothing, and the
// limiter reclaims idle keys against the wall clock under the default bound.
type Option func(*options)

type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	clock           clock.Clock
	maxLimiters     int
}

func newOptions(opts []Option) *options {
	cfg := &options{
		clock:       clock.NewClock(),
		maxLimiters: DefaultMaxLimiters,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	// A caller that named a nil clock gets the wall clock rather than a panic
	// on the first sweep; naming no clock and naming none of one are the same
	// request.
	if cfg.clock == nil {
		cfg.clock = clock.NewClock()
	}

	return cfg
}

// WithMetricsProvider attaches a metrics provider for the limiter's allowed
// and rejected counters.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, so the limiter's spans are
// children of the request that consulted it.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithClock supplies the clock the limiter ages its per-key state against.
//
// It measures idleness and paces the sweep, and nothing else — the token
// buckets themselves are golang.org/x/time/rate's, which read the wall clock on
// their own. So this makes eviction testable, not the limiting itself.
func WithClock(c clock.Clock) Option {
	return func(o *options) { o.clock = c }
}

// WithMaxLimiters bounds how many per-key limiters are held at once, replacing
// DefaultMaxLimiters.
//
// The bound only ever binds on a flood of distinct keys inside a single window,
// where the idle TTL has had nothing to reclaim yet. Evicting under it hands the
// affected keys a full burst early, so raising it trades memory for a limit that
// holds under key-cardinality pressure.
//
// A non-positive n removes the bound, leaving the idle TTL as the only thing
// that reclaims memory. That is a deliberate choice for a limiter whose key
// space is known to be small — a fixed set of tenants, say — and a bad one for
// anything keyed on client addresses.
func WithMaxLimiters(n int) Option {
	return func(o *options) { o.maxLimiters = n }
}

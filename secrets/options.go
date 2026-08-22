package secrets

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/retry"
)

// Option configures the caching source this package constructs. The zero
// configuration works: an absent logger logs nowhere, an absent tracer
// provider traces nowhere, an absent metrics provider records nothing, and an
// absent refresh means the cache is filled only by the reads that miss.
type Option func(*options)

type options struct {
	refreshCtx      context.Context
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	rand            retry.Rand
	refreshInterval time.Duration
}

func newOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithRand replaces the source that spreads a fleet's background refreshes
// apart. fn must return a value in [0,1]; a value of 1 yields the un-jittered
// interval, and every draw shortens rather than lengthens it.
//
// The default draws from math/rand/v2 and needs no seeding. A nil fn is
// ignored.
func WithRand(fn retry.Rand) Option {
	return func(o *options) {
		if fn != nil {
			o.rand = fn
		}
	}
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on every
// operation.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the package's counters
// and gauges.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithRefresh starts a background goroutine that re-resolves every cached
// secret every interval, so a warm cache is kept warm without any caller
// paying for the round-trip.
//
// Without it the cache is filled only by the reads that miss, which means
// every TTL expiry is paid for by whichever caller happens to arrive first —
// and, more to the point, that nothing observes a rotation until somebody
// asks. The refresh is what turns OnChange from a hook that fires on read into
// one that fires on time.
//
// The interval must be positive and shorter than the TTL, or NewCachingSource
// returns ErrInvalidRefreshInterval: a refresh that cannot land before the
// entry expires is not a refresh, it is a second way to spell the TTL.
// Individual waits are jittered downward from the interval — never past it —
// so a fleet that started together drifts apart instead of hitting the backend
// in lockstep, without any wait outliving the TTL it was chosen to stay under.
//
// The refresh stops on Close, and also when ctx is done, whichever happens
// first. Passing context.Background() and relying on Close is the ordinary
// shape; a cancellable ctx is for tying the refresh to something narrower than
// the source's own lifetime. A nil ctx or a non-positive interval starts no
// goroutine at all.
func WithRefresh(ctx context.Context, interval time.Duration) Option {
	return func(o *options) {
		if ctx == nil || interval <= 0 {
			return
		}

		o.refreshCtx = ctx
		o.refreshInterval = interval
	}
}

package devices

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

type (
	// Option configures a SQLStore at construction.
	Option func(*options)

	options struct {
		clock           clock.Clock
		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		//nolint:containedctx // deliberate: see WithSweeper
		sweepCtx      context.Context
		sweepInterval time.Duration
	}
)

// newOptions applies opts over the defaults, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{
		clock: clock.NewClock(),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithClock swaps the clock a row's two stamps are read from and the sweep's
// horizon is bound from.
//
// It is the same clock for all three on purpose: the sweep compares expires_at,
// which signin computed, against this clock, and a row whose stamps came from
// two clocks is one that is collected at the wrong moment.
func WithClock(c clock.Clock) Option {
	return func(o *options) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithSweeper starts a background sweep that removes the rows of logins that can
// no longer be alive, every interval, until ctx is done.
//
// A table does not reclaim its own expired rows, and without a sweep this one
// grows by a row for every login ever begun — each holding an address somebody
// signed in from, long after anybody could be shown it. Running it is not
// optional in any long-lived deployment; what is optional is running it here
// rather than from a scheduler that calls Sweep, which is the better answer for a
// fleet — one sweeper, not one per replica.
//
// The context bounds the goroutine's life. Passing a nil context or a
// non-positive interval starts nothing.
func WithSweeper(ctx context.Context, interval time.Duration) Option {
	return func(o *options) {
		if ctx == nil || interval <= 0 {
			return
		}

		o.sweepCtx = ctx
		o.sweepInterval = interval
	}
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider. An absent one traces nowhere.
//
// It takes a provider rather than a ready-made tracer so that this package's
// spans carry this package's instrumentation scope.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the sweeper's counters. An
// absent one records nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

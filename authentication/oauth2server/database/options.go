package database

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// ErrNilClient indicates NewStore was called without a database client. It
// wraps errors.ErrNilInputParameter, so a caller may check either.
var ErrNilClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 database client")

type (
	// Option configures a Store at construction.
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
	o := &options{clock: clock.NewClock()}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithSweeper starts a background sweep that removes dead records every
// interval, until ctx is done.
//
// Unlike a cache, a table does not reclaim its own expired rows, and this
// schema has four of them — one of which an unauthenticated caller can write
// to. Running a sweep is not optional in any long-lived deployment; what is
// optional is running it here rather than from a scheduler that calls Sweep,
// which is the better answer for a fleet: one sweeper, not one per replica.
//
// A nil context or a non-positive interval starts nothing.
func WithSweeper(ctx context.Context, interval time.Duration) Option {
	return func(o *options) {
		if ctx == nil || interval <= 0 {
			return
		}

		o.sweepCtx = ctx
		o.sweepInterval = interval
	}
}

// WithClock swaps the clock every deadline is evaluated against, and the one
// the sweeper ticks on.
//
// It is this store's own clock rather than the database server's, deliberately.
// Both would work for expiry, but only this one can be replaced in a test, and
// only this one agrees with the clock the Server stamped the deadline from —
// which is what keeps "issued for fifteen minutes" and "expired" measuring the
// same fifteen minutes.
func WithClock(c clock.Clock) Option {
	return func(o *options) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider. An absent one traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the sweeper's counters.
// An absent one records nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

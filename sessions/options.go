package sessions

import (
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

type (
	// Option configures a Store at construction.
	//
	// It is deliberately not parameterized on the Store's T. None of these
	// settings depend on it, and Go cannot infer a type argument from a call's
	// result type — so an Option[T] would force every call site to spell the
	// payload type out by hand, WithIdleTimeout[Principal](time.Hour), forever.
	Option func(*storeOptions)

	// storeOptions accumulates what the options set, so that Option can stay
	// free of the Store's type parameter.
	storeOptions struct {
		clock           clock.Clock
		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		// touch is a pointer because zero is meaningful — refresh the idle
		// deadline on every read — and so cannot double as "unset".
		touch *time.Duration

		absoluteTimeout time.Duration
		idleTimeout     time.Duration
		grace           time.Duration
	}
)

// newStoreOptions applies opts over the defaults, ignoring nil entries.
func newStoreOptions(opts []Option) *storeOptions {
	o := &storeOptions{
		clock:           clock.NewClock(),
		absoluteTimeout: DefaultAbsoluteTimeout,
		idleTimeout:     DefaultIdleTimeout,
		grace:           DefaultRetentionGrace,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithAbsoluteTimeout bounds a session's total lifetime, measured from when it
// was established and unaffected by activity or by Renew.
//
// A non-positive value disables it, which is only safe when the idle timeout is
// not also disabled — a store with neither never releases a session, and is
// rejected at construction.
func WithAbsoluteTimeout(timeout time.Duration) Option {
	return func(o *storeOptions) { o.absoluteTimeout = timeout }
}

// WithIdleTimeout bounds how long a session may go unread. A non-positive value
// disables it, and also disables touching: there is then no idle deadline for a
// read to refresh.
func WithIdleTimeout(timeout time.Duration) Option {
	return func(o *storeOptions) { o.idleTimeout = timeout }
}

// WithTouchInterval sets how much of the idle window must elapse before a read
// refreshes the idle deadline. Zero refreshes on every read; see Policy for
// what the interval buys and what it costs.
func WithTouchInterval(interval time.Duration) Option {
	return func(o *storeOptions) { o.touch = &interval }
}

// WithRetentionGrace sets how long an expired record is kept before the backing
// store may reclaim it. See Policy.Grace for what it buys; a non-positive value
// lets the backend reclaim the record at the deadline, so an expired session
// then reads as merely absent.
func WithRetentionGrace(grace time.Duration) Option {
	return func(o *storeOptions) { o.grace = grace }
}

// WithClock swaps the clock the store stamps and expires against, so timeout
// behavior is deterministic in tests.
func WithClock(c clock.Clock) Option {
	return func(o *storeOptions) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *storeOptions) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider. An absent one traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *storeOptions) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent one records
// nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *storeOptions) { o.metricsProvider = metricsProvider }
}

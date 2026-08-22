package timers

import (
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

type (
	// Option configures a timer set at construction.
	//
	// It is deliberately not parameterized on the set's K. None of these
	// settings depend on it, and Go cannot infer a type argument from a call's
	// result type — so an Option would force every call site to spell the key
	// type out by hand, forever.
	//
	// WithKeyCodec is the one setting that does depend on K. It stays generic
	// but still needs no annotation, because K is inferable from the codec it is
	// handed; see its documentation for how a mismatch is reported.
	Option func(*timerOptions)

	// timerOptions accumulates what the options set, so that Option can stay
	// free of the set's type parameter.
	timerOptions struct {
		clock           clock.Clock
		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		wakeup <-chan struct{}

		// keyCodec holds a KeyCodec[K] for the K of the set being built. It is
		// typed as any because Option cannot name K; New asserts it back to the
		// concrete interface and reports a mismatch rather than ignoring it.
		keyCodec any
	}
)

// newTimerOptions applies opts, ignoring nil entries.
func newTimerOptions(opts []Option) *timerOptions {
	o := &timerOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithClock swaps the clock this package reads.
//
// It reads one in exactly two places, and neither of them decides when a timer
// fires: ScheduleIn turns a delay into the instant the caller meant, and Wait
// paces this process's own sleeping. Whether a stored instant has arrived is
// always Postgres's now() to answer, so replacing the clock cannot desynchronize
// a fleet — it can only change what "in an hour" means at the moment it is said.
//
// Tests generally do not need it: inside a testing/synctest bubble the default
// clock already reads bubble time, so a timer scheduled a week out fires
// instantly and deterministically.
func WithClock(c clock.Clock) Option {
	return func(o *timerOptions) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithLogger attaches a logger. Nothing in this package fails loudly — a
// deadlock retried into success, a notification that could not be sent — so
// without one those events are visible only in metrics.
func WithLogger(logger logging.Logger) Option {
	return func(o *timerOptions) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider. A claim that leases nothing is
// not traced: a root span per empty poll is noise, and a timer poller is idle
// almost all of the time by design.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *timerOptions) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent provider records
// nothing — including the lateness gauge, which is the only way to see that a
// fleet has stopped firing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *timerOptions) { o.metricsProvider = metricsProvider }
}

// WithWakeup gives Wait a channel to return on, beside the next-due time and the
// poll interval. A receive means "the schedule may have changed"; the caller
// re-reads it, so nothing about the set's guarantees changes and a wake that
// never arrives costs only latency.
//
// The latency it removes is the one a timer set is uniquely exposed to. A poller
// with nothing due for an hour sleeps for an hour; a timer scheduled thirty
// seconds out, landing a moment after it went to sleep, fires an hour late
// without a wake and on time with one.
//
// It is a bare channel because the set must not learn where the wake came from.
// database/postgres/pgnotify fills it from LISTEN/NOTIFY — pair it with
// Config.NotifyChannel on the scheduling side — but a test fills it by hand.
//
// The channel should coalesce — capacity one, non-blocking sends, as
// pgnotify.Listener.Signal does. Config.MinWakeInterval floors the rate
// regardless.
func WithWakeup(wakeup <-chan struct{}) Option {
	return func(o *timerOptions) { o.wakeup = wakeup }
}

// WithKeyCodec overrides how keys are rendered into the table's primary key.
// The default is DefaultKeyCodec, which stores string-like keys as themselves
// and everything else as JSON.
//
// K is inferred from the codec, so this needs no type argument:
//
//	timers.WithKeyCodec(myCodec{})
//
// It must match the set it configures. Because Option carries no type parameter,
// a codec for the wrong key type cannot be rejected by the compiler; New returns
// ErrKeyCodecTypeMismatch instead, at construction, before a single key has been
// written under the wrong rendering.
func WithKeyCodec[K comparable](codec KeyCodec[K]) Option {
	return func(o *timerOptions) {
		if codec != nil {
			o.keyCodec = codec
		}
	}
}

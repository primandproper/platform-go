package eventcapture

import (
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures a Recorder.
//
// It carries no type parameter even though the Recorder does. Go cannot infer a
// type argument from a call's result type, so an Option[E] would force every
// call site to spell the event type out by hand — WithBufferSize[MyEvent](256) —
// forever. WithTransform and WithObserver are the two options that depend on
// the event type; they stay generic but still need no annotation, because E is
// inferable from the function each is handed.
type Option func(*options)

// options accumulates what the options set, so Option can stay free of the
// Recorder's type parameter.
type options struct {
	clock           clock.Clock
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	overflow func() uint64
	onFlush  func(now time.Time, final bool, emit func(record any))

	// transform and observe hold func(*E) any and func(*E) for the E of the
	// Recorder being built. They are typed as any because Option cannot name E;
	// NewRecorder asserts them back and reports a mismatch rather than dropping
	// them.
	transform any
	observe   any

	bufferSize    int
	flushInterval time.Duration
	noRawRecords  bool
}

// WithBufferSize caps the in-flight event channel. A full buffer drops (and
// counts) new events rather than ever blocking a caller.
func WithBufferSize(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.bufferSize = n
		}
	}
}

// WithFlushInterval sets the cadence of the flusher tick.
func WithFlushInterval(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.flushInterval = d
		}
	}
}

// WithClock swaps the clock driving the flush ticker. Tests generally do not
// need it: under testing/synctest the default clock already runs on bubble
// time.
func WithClock(c clock.Clock) Option {
	return func(o *options) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithLogger attaches a logger for sink errors and drop reporting. It is
// named after the package, so capture lines are attributable in aggregate
// logs.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) {
		o.logger = logger
	}
}

// WithTracerProvider attaches a tracer provider. The flusher deliberately does
// not open a span per flush tick — a root span every few seconds, with no
// caller to parent it to, is noise rather than signal. The tracer is used for
// Close, where the drain is a real, once-per-process operation a shutdown
// trace wants to account for.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) {
		o.tracerProvider = tracerProvider
	}
}

// WithMetricsProvider attaches a metrics provider, enabling the
// eventcapture_* instruments. These are the only signal that a capture
// pipeline has broken: per the package contract sink errors are never returned
// to a caller, and dropped events never reach the sink at all.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) {
		o.metricsProvider = metricsProvider
	}
}

// WithOverflowSource registers a function the flusher polls each tick to
// report observations an aggregation dropped for exceeding its key bound —
// pass an Aggregator's TakeOverflow. Without it, a full Aggregator discards
// observations silently, since the Recorder cannot see inside a composition
// whose key and counter types belong to the caller.
func WithOverflowSource(fn func() uint64) Option {
	return func(o *options) {
		o.overflow = fn
	}
}

// WithoutRawRecords disables the per-event sink write, for compositions that
// only emit derived records (e.g. aggregate rollups via WithOnFlush).
func WithoutRawRecords() Option {
	return func(o *options) {
		o.noRawRecords = true
	}
}

// WithTransform projects each event into the record written to the sink —
// typically a wire-shaped struct with stable JSON tags — instead of the raw
// *E. It runs in the flusher goroutine, off the hot path.
//
// E is inferred from the function, so this needs no type argument. It must
// match the Recorder it configures; NewRecorder returns ErrEventTypeMismatch
// otherwise, since Option cannot carry E for the compiler to check.
func WithTransform[E any](fn func(*E) any) Option {
	return func(o *options) {
		if fn != nil {
			o.transform = fn
		}
	}
}

// WithObserver runs fn for every consumed event, in the flusher goroutine.
// This is the composition point for an Aggregator's Observe.
//
// E is inferred from the function, so this needs no type argument. It must
// match the Recorder it configures; NewRecorder returns ErrEventTypeMismatch
// otherwise, since Option cannot carry E for the compiler to check.
func WithObserver[E any](fn func(*E)) Option {
	return func(o *options) {
		if fn != nil {
			o.observe = fn
		}
	}
}

// WithOnFlush runs fn on every flush tick and once more during the final
// drain (with final set). It runs in the flusher goroutine; emit writes a
// record through the sink with the Recorder's error handling. This is the
// composition point for emitting an Aggregator's completed buckets.
func WithOnFlush(fn func(now time.Time, final bool, emit func(record any))) Option {
	return func(o *options) {
		o.onFlush = fn
	}
}

package batching

import (
	"cmp"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures a GroupCommit or a Buffer.
//
// One type serves both, because the two shapes share most of what there is to
// configure — a clock, a flush timeout, an ordering, and the three
// observability pillars — and splitting it would mean two spellings of
// WithLogger. Each option below says which constructor reads it; one that the
// constructor has no use for is accepted and ignored, so a wiring site that
// builds both from one slice of options does not have to sort them out.
//
// Option carries no type parameter even though both shapes do. Go cannot infer
// a type argument from a call's result type, so an Option[T] would force every
// call site to spell the item type out by hand — WithFlushTimeout[MyRow](d) —
// forever. WithMerge and WithOrder are the two options that depend on the item
// type; they stay generic but still need no annotation, because the type is
// inferable from the functions each is handed.
type Option func(*options)

// options accumulates what the options set, so Option can stay free of the
// batchers' type parameters.
type options struct {
	clock           clock.Clock
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	// newAccumulator holds func() accumulator[T] and order holds
	// func(a, b T) int, for the T of the batcher being built. They are typed as
	// any because Option cannot name T; the constructors assert them back and
	// report a mismatch rather than dropping them.
	newAccumulator any
	order          any

	flushTimeout  time.Duration
	flushInterval time.Duration
	maxPending    int
}

// newOptions applies opts over the defaults.
func newOptions(opts []Option) *options {
	o := &options{
		clock:         clock.NewClock(),
		flushTimeout:  DefaultFlushTimeout,
		flushInterval: DefaultFlushInterval,
		maxPending:    DefaultMaxPending,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithMerge deduplicates a GroupCommit's batch by key and emits it in key
// order. Ignored by NewBuffer, which dedupes by the key type itself.
//
// merge folds an item into whatever the batch already held for that key, and is
// only called when there is something to fold into — the first item for a key
// is taken as it stands. A nil merge keeps the last item to arrive for each
// key.
//
// The item and key types are inferred from key, so this needs no type argument:
//
//	batching.WithMerge(func(r row) string { return r.id }, mergeRows)
//
// The key type must be ordered, because emitting in key order is half of what
// this option is for: the write function receives the batch in one total order,
// which is the lock ordering that keeps concurrent writers of the same rows
// queueing instead of deadlocking.
//
// It must match the GroupCommit it configures; NewGroupCommit returns
// ErrItemTypeMismatch otherwise, since Option cannot carry the item type for the
// compiler to check.
func WithMerge[T any, K cmp.Ordered](key func(T) K, merge func(existing, incoming T) T) Option {
	return func(o *options) {
		if key == nil {
			return
		}

		o.newAccumulator = func() accumulator[T] { return newKeyedAccumulator(key, merge) }
	}
}

// WithOrder sorts every flushed batch by compare (slices.SortFunc semantics), for
// batches whose order is not already decided by a merge key. Read by both
// constructors; for a Buffer, it also orders what Take hands back, since a
// caller taking keys is about to write them and wants the same lock order.
//
// Applied on top of WithMerge rather than instead of it: merge decides which
// items are written, order decides in what sequence. Supplying both is how a
// batch keyed by one field is written in another field's order.
//
// The item type is inferred from compare, so this needs no type argument:
//
//	batching.WithOrder(strings.Compare)
//
// It must match the batcher it configures; the constructors return
// ErrItemTypeMismatch otherwise.
func WithOrder[T any](compare func(a, b T) int) Option {
	return func(o *options) {
		if compare != nil {
			o.order = compare
		}
	}
}

// WithFlushTimeout bounds one flush. Read by both constructors.
//
// It is a fixed timeout rather than an inherited deadline on purpose: the batch
// outlives whichever caller opened it, so it cannot borrow one waiter's
// cancellation without abandoning the rest. Set it generously — a waiter that
// gives up first has already stopped caring, and the ones still blocked would
// rather the write finished.
func WithFlushTimeout(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.flushTimeout = d
		}
	}
}

// WithFlushInterval sets how often a Buffer flushes what it has accumulated.
// Ignored by NewGroupCommit, which is deliberately not timer-driven.
func WithFlushInterval(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.flushInterval = d
		}
	}
}

// WithMaxPending caps how many distinct keys a Buffer holds before it flushes
// without waiting for the interval. Ignored by NewGroupCommit.
//
// It is a flush trigger, not a bound: Add never blocks and never drops, so a
// process adding faster than the write function drains will exceed it between
// flushes. What it prevents is an unbounded batch, not an unbounded buffer.
func WithMaxPending(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.maxPending = n
		}
	}
}

// WithClock swaps the clock driving a Buffer's flush interval and both shapes'
// latency measurement. Tests generally do not need it: under testing/synctest
// the default clock already runs on bubble time.
func WithClock(c clock.Clock) Option {
	return func(o *options) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithLogger attaches a logger. It is what reports a Buffer's flush failures,
// which reach no caller by design — see NewBuffer.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) {
		o.logger = logger
	}
}

// WithTracerProvider attaches a tracer provider. A flush is traced as a span of
// its own rather than under whichever caller happened to trigger it, because
// that is what it is: one write on behalf of every waiter, none of whom own it.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) {
		o.tracerProvider = tracerProvider
	}
}

// WithMetricsProvider attaches a metrics provider, enabling the
// batching_group_commit_* and batching_buffer_* instruments.
//
// The batch size histogram is the one to watch. Merging is working when it
// climbs with load; a histogram pinned at one under load means callers are not
// arriving concurrently and the batcher is buying nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) {
		o.metricsProvider = metricsProvider
	}
}

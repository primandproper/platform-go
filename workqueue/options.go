package workqueue

import (
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

type (
	// Option configures a Queue at construction.
	//
	// It is deliberately not parameterized on the Queue's K. None of these
	// settings depend on it, and Go cannot infer a type argument from a call's
	// result type — so an Option would force every call site to spell the
	// Queue's key type out by hand, forever.
	//
	// WithKeyCodec is the one setting that does depend on K. It stays generic
	// but still needs no annotation, because K is inferable from the codec it is
	// handed; see its documentation for how a mismatch is reported.
	//
	// There is no clock option, alone among this module's scheduling components.
	// The database's now() is the only clock a Queue consults, and offering a
	// seam to replace it would offer a way to break the one property the whole
	// design rests on.
	Option func(*queueOptions)

	// queueOptions accumulates what the options set, so that Option can stay
	// free of the Queue's type parameter.
	queueOptions struct {
		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		wakeup <-chan struct{}

		// keyCodec holds a KeyCodec[K] for the K of the Queue being built. It is
		// typed as any because Option cannot name K; New asserts it back to the
		// concrete interface and reports a mismatch rather than ignoring it.
		keyCodec any
	}
)

// newQueueOptions applies opts, ignoring nil entries.
func newQueueOptions(opts []Option) *queueOptions {
	o := &queueOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithLogger attaches a logger. Nothing in this package fails loudly — a
// deadlock retried into success, a Release that could not be recorded — so
// without one those events are visible only in metrics.
func WithLogger(logger logging.Logger) Option {
	return func(o *queueOptions) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider. A claim that leases nothing is
// not traced: a root span per empty poll is noise, and an idle worker polls far
// more often than a busy one.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *queueOptions) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent provider records
// nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *queueOptions) { o.metricsProvider = metricsProvider }
}

// WithWakeup gives Wait a channel to return on, beside its poll interval. A
// receive means "there may be work now"; the caller runs the same Claim it
// would have run on its next poll, so nothing about the queue's guarantees
// changes and a wake that never arrives costs only latency.
//
// It is a bare channel because the queue must not learn where the wake came
// from. database/postgres/pgnotify fills it from LISTEN/NOTIFY — pair it with
// Config.NotifyChannel on the enqueueing side — but a test fills it by hand.
//
// The channel should coalesce — capacity one, non-blocking sends, as
// pgnotify.Listener.Signal does. Config.MinWakeInterval floors the rate
// regardless.
//
// Without one, Wait is a plain sleep and every claim loop keeps the behavior it
// has today.
func WithWakeup(wakeup <-chan struct{}) Option {
	return func(o *queueOptions) { o.wakeup = wakeup }
}

// WithKeyCodec overrides how keys are rendered into the table's primary key.
// The default is DefaultKeyCodec, which stores string-like keys as themselves
// and everything else as JSON.
//
// K is inferred from the codec, so this needs no type argument:
//
//	workqueue.WithKeyCodec(myCodec{})
//
// It must match the Queue it configures. Because Option carries no type
// parameter, a codec for the wrong key type cannot be rejected by the compiler;
// New returns ErrKeyCodecTypeMismatch instead, at construction, before a single
// key has been written under the wrong rendering.
func WithKeyCodec[K comparable](codec KeyCodec[K]) Option {
	return func(o *queueOptions) {
		if codec != nil {
			o.keyCodec = codec
		}
	}
}

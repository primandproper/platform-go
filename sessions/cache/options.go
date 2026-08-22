package cache

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// ErrNilCache indicates NewBackend was called without a cache. It wraps
// errors.ErrNilInputParameter, so a caller may check either.
var ErrNilCache = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil session cache")

type (
	// Option configures the backend at construction.
	//
	// It carries no type parameter even though NewBackend does: Go cannot infer
	// a type argument from a call's result type, so an Option[T] would force
	// every call site to spell the payload type out by hand forever.
	//
	// There is no WithMetricsProvider. Every counter worth having describes what
	// an operation meant — a session created, a session expired, an idle
	// deadline refreshed — and only the Store knows that; this layer would only
	// be able to count round trips the cache provider already counts.
	Option func(*options)

	options struct {
		logger         logging.Logger
		tracerProvider tracing.Provider
	}
)

// newOptions applies opts, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider. An absent one traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

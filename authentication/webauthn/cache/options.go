package cache

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// ErrNilCache indicates NewSessionStore was called without a cache. It wraps
// errors.ErrNilInputParameter, so a caller may check either.
var ErrNilCache = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webauthn session cache")

type (
	// Option configures the store at construction.
	//
	// There is no WithMetricsProvider. Every count worth having here describes
	// what a ceremony meant — begun, finished, refused — and only the relying
	// party knows that; this layer would count round trips the cache provider
	// already counts.
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

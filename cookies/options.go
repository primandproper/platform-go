package cookies

import (
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures the Manager this package constructs. The zero
// configuration works: an absent logger logs nowhere and an absent tracer
// provider traces nowhere.
type Option func(*options)

type options struct {
	logger         logging.Logger
	tracerProvider tracing.Provider
}

func newOptions(opts []Option) *options {
	cfg := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

// WithLogger attaches a logger.
//
// A decode failure is the one worth having: it means a cookie this deployment
// issued no longer verifies, which is a rotated key or a forgery attempt, and
// neither is visible in a span nobody sampled.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on every
// operation.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

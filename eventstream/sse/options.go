package sse

import (
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures the Upgrader this package constructs. The zero
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

// WithLogger attaches a logger, which every stream the Upgrader produces
// inherits — so a write that fails to reach a subscriber is reported somewhere
// other than in the error returned to a handler that has already sent its
// headers.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

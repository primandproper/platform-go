package mail

import (
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// options is what an Option writes, for either constructor.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// Option configures a QueuedMailer or a Drainer.
type Option func(*options)

// WithLogger sets the logger. Absent means none.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) {
		o.logger = logger
	}
}

// WithTracerProvider sets the tracer provider. Absent means none.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) {
		o.tracerProvider = tracerProvider
	}
}

// WithMetricsProvider sets the metrics provider. Absent means none.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) {
		o.metricsProvider = metricsProvider
	}
}

func applyOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

package posthog

import (
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"github.com/posthog/posthog-go"
)

// Option configures the EventReporter this package constructs. The zero
// configuration works: an absent logger logs nowhere, an absent tracer
// provider traces nowhere, and an absent metrics provider records nothing.
type Option func(*options)

type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	configModifiers []func(*posthog.Config)
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
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on every event.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithConfigModifiers appends functions that mutate the PostHog client config
// before the client is built. Modifiers accumulate across repeated uses of this
// option and run in the order provided.
func WithConfigModifiers(configModifiers ...func(*posthog.Config)) Option {
	return func(o *options) { o.configModifiers = append(o.configModifiers, configModifiers...) }
}

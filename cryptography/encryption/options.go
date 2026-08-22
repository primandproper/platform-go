package encryption

import (
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures a Keyring. The zero configuration works: an absent logger
// logs nowhere, an absent tracer provider traces nowhere, and an absent
// metrics provider records nothing.
type Option func(*options)

type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

func newOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on every
// operation.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the rotation counters.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithPillars supplies logger, tracer provider, and metrics provider at once.
//
// Options apply in order, so a WithPillars followed by a narrower option wins
// for that component: WithPillars(p) then WithMetricsProvider(nil) leaves the
// keyring traced and logged but unmetered.
func WithPillars(pillars *observability.Pillars) Option {
	return func(o *options) {
		if pillars == nil {
			return
		}

		o.logger = pillars.Logger
		o.tracerProvider = pillars.TracerProvider
		o.metricsProvider = pillars.MetricsProvider
	}
}

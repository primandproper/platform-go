package mcptool

import (
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures a [Surface] at construction.
type Option func(*options)

type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	safe            []error
}

// WithClientSafeSentinels names the sentinels whose own text a model may be
// told: a surface over one of this module's packages passes that package's
// ClientSafeSentinels, or the not-found refusal its reads answer with.
// Anything else a call fails with reaches the model as [ErrToolFailed].
//
// Absent names none, which is the safe direction to be wrong in: a refusal
// worth repeating reads as a failed call, and nothing the surface did not
// choose to say is said.
func WithClientSafeSentinels(sentinels ...error) Option {
	return func(o *options) { o.safe = append(o.safe, sentinels...) }
}

// WithLogger sets the surface's logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider sets the surface's tracer provider.
//
// A provider rather than a ready-made tracer, so the instrumentation scope is
// the surface's name rather than the caller's.
func WithTracerProvider(provider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = provider }
}

// WithMetricsProvider sets the surface's metrics provider.
func WithMetricsProvider(provider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = provider }
}

// WithPillars supplies logger, tracer provider and metrics provider at once.
//
// Options apply in order, so WithPillars(p) followed by WithMetricsProvider(nil)
// leaves this surface unmetered.
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

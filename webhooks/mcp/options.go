package mcp

import (
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures [Tools] at construction.
type Option func(*options)

type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// WithLogger sets the surface's logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider sets the surface's tracer provider.
//
// A provider rather than a ready-made tracer, so the instrumentation scope is
// this package's rather than the caller's.
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

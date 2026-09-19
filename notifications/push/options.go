package push

import (
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures a [Fanout] at construction.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. A caller wanting none of the three names none of them.
type Option func(*Fanout)

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(f *Fanout) { f.logger = logger }
}

// WithTracerProvider attaches a tracer provider.
//
// A provider rather than a ready-made tracer, so the spans this package emits
// carry this package's instrumentation scope. A caller-supplied tracer would
// attribute them to whoever built it.
func WithTracerProvider(provider tracing.Provider) Option {
	return func(f *Fanout) { f.tracerProvider = provider }
}

// WithMetricsProvider attaches a metrics provider. An absent provider records
// nothing.
func WithMetricsProvider(provider metrics.Provider) Option {
	return func(f *Fanout) { f.metricsProvider = provider }
}

// WithPillars supplies logger, tracer provider and metrics provider at once. A
// nil Pillars attaches nothing.
//
// Options apply in order, so WithPillars(p) followed by WithMetricsProvider(nil)
// leaves this fan-out unmetered.
func WithPillars(p *observability.Pillars) Option {
	return func(f *Fanout) { f.logger, f.tracerProvider, f.metricsProvider = p.Deps() }
}

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
// nothing. A caller wanting none of the three names none of them. The same is
// true of a recipient filter: an absent one leaves nobody out.
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

// WithRecipientFilter asks the filter, once per distinct principal and before a
// single handset is resolved, whether that person should receive this push. A
// nil filter asks nothing, which is what a fan-out built without this option
// does.
//
// What a preference is — which setting, which categories, quiet hours — is the
// consumer's, and this package reads none of it. What the fan-out owes is the
// one place to ask, so the call site that forgets to filter its principals is a
// call site that cannot exist. See [RecipientFilter].
func WithRecipientFilter(filter RecipientFilter) Option {
	return func(f *Fanout) { f.filter = filter }
}

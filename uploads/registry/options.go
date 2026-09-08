package registry

import (
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
)

// SQLStoreOption configures a SQLStore.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. A caller wanting none of the three names none of them.
type SQLStoreOption func(*SQLStore)

// WithTablePrefix namespaces the registry table. It must match the prefix the
// migrations were rendered with; nothing here can check that, and a mismatch
// surfaces as a missing table on the first query rather than at construction.
func WithTablePrefix(prefix string) SQLStoreOption {
	return func(s *SQLStore) { s.prefix = prefix }
}

// WithStoreLogger attaches a logger. An absent logger logs nowhere.
func WithStoreLogger(logger logging.Logger) SQLStoreOption {
	return func(s *SQLStore) { s.logger = logger }
}

// WithStoreTracerProvider attaches a tracer provider, enabling spans on every
// read and write. An absent provider traces nowhere.
//
// It takes a provider rather than a ready-made tracer so that the spans this
// package emits carry this package's instrumentation scope. A caller-supplied
// tracer would attribute them to whoever built it.
func WithStoreTracerProvider(tracerProvider tracing.Provider) SQLStoreOption {
	return func(s *SQLStore) { s.tracerProvider = tracerProvider }
}

// WithStoreMetricsProvider attaches a metrics provider. An absent provider
// records nothing.
func WithStoreMetricsProvider(metricsProvider metrics.Provider) SQLStoreOption {
	return func(s *SQLStore) { s.metricsProvider = metricsProvider }
}

// WithStorePillars attaches a logger, tracer provider, and metrics provider in
// one go. A nil Pillars attaches nothing.
//
// Options apply in order, so a caller can hand over its pillars and then
// override one of them.
func WithStorePillars(p *observability.Pillars) SQLStoreOption {
	return func(s *SQLStore) { s.logger, s.tracerProvider, s.metricsProvider = p.Deps() }
}

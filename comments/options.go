package comments

import (
	"maps"

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

// WithTablePrefix namespaces the comments table. It must match the prefix the
// migrations were rendered with; nothing here can check that, and a mismatch
// surfaces as a missing table on the first query rather than at construction.
func WithTablePrefix(prefix string) SQLStoreOption {
	return func(s *SQLStore) { s.prefix = prefix }
}

// WithTargets supplies the kinds of thing this application accepts comments on.
// A store built without it accepts none, which is the reading webhooks takes of
// an absent event catalog and for the same reason.
//
// The catalog is copied rather than retained, so a consumer that keeps mutating
// the map it passed does not quietly change what the store enforces. A catalog
// that should change is a store that should be rebuilt.
//
// Applying it twice replaces rather than merges: two calls are two answers to
// "what can be commented on", and merging them would make the store accept a
// type neither call meant on its own.
func WithTargets(targets Targets) SQLStoreOption {
	return func(s *SQLStore) {
		catalog := make(Targets, len(targets))
		maps.Copy(catalog, targets)

		s.targets = catalog
	}
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

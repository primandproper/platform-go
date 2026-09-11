package identity

import (
	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// SQLStoreOption configures a SQLStore.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. A caller wanting none of the three names none of them.
type SQLStoreOption func(*SQLStore)

// WithTablePrefix namespaces the seven identity tables. It must match the
// prefix the migrations were rendered with; nothing here can check that, and a
// mismatch surfaces as a missing table on the first query rather than at
// construction.
func WithTablePrefix(prefix string) SQLStoreOption {
	return func(s *SQLStore) { s.tablePrefix = prefix }
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

// WithClock replaces the clock every timestamp this store writes is read from,
// for tests that need a registration and an expiry to be a known distance
// apart. A nil clock is ignored.
func WithClock(c clock.Clock) SQLStoreOption {
	return func(s *SQLStore) {
		if c != nil {
			s.clock = c
		}
	}
}

// ServiceOption configures a Service.
//
// The observability dependencies are options rather than parameters for the
// reason SQLStoreOption gives, and Hooks is one because a consumer with nothing
// to commit alongside an identity write should have to name nothing.
type ServiceOption func(*Service)

// WithHooks attaches the hooks every operation calls inside its transaction.
// A nil Hooks is ignored, leaving the NoopHooks the Service is built with.
//
// It is the seam a consumer's audit entry, data change event or search stamp
// commits with the row — see Hooks for what belongs in one.
func WithHooks(hooks Hooks) ServiceOption {
	return func(s *Service) {
		if hooks != nil {
			s.hooks = hooks
		}
	}
}

// WithServiceLogger attaches a logger. An absent logger logs nowhere.
func WithServiceLogger(logger logging.Logger) ServiceOption {
	return func(s *Service) { s.logger = logger }
}

// WithServiceTracerProvider attaches a tracer provider, enabling a span per
// operation. An absent provider traces nowhere.
//
// It takes a provider rather than a ready-made tracer for the reason
// WithStoreTracerProvider does: the spans carry this package's instrumentation
// scope rather than whoever built the tracer.
func WithServiceTracerProvider(tracerProvider tracing.Provider) ServiceOption {
	return func(s *Service) { s.tracerProvider = tracerProvider }
}

// WithServiceMetricsProvider attaches a metrics provider, enabling the request,
// error and latency instruments each operation records. An absent provider
// records nothing.
func WithServiceMetricsProvider(metricsProvider metrics.Provider) ServiceOption {
	return func(s *Service) { s.metricsProvider = metricsProvider }
}

// WithServicePillars attaches a logger, tracer provider, and metrics provider
// in one go. A nil Pillars attaches nothing.
//
// Options apply in order, so a caller can hand over its pillars and then
// override one of them.
func WithServicePillars(p *observability.Pillars) ServiceOption {
	return func(s *Service) { s.logger, s.tracerProvider, s.metricsProvider = p.Deps() }
}

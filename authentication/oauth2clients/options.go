package oauth2clients

import (
	"github.com/primandproper/platform-go/v14/observability"
	"github.com/primandproper/platform-go/v14/observability/logging"
	"github.com/primandproper/platform-go/v14/observability/metrics"
	"github.com/primandproper/platform-go/v14/observability/tracing"
)

// SQLStoreOption configures a [SQLStore] at construction.
//
// Observability arrives this way rather than positionally, which is this
// module's convention: a caller who wants none names none, and every constructor
// resolves what it was not given through the Ensure* helpers.
type SQLStoreOption func(*SQLStore)

// WithTablePrefix namespaces this store's table, for a database shared between
// applications.
//
// It must match the prefix the migrations were rendered with. Nothing here can
// check that, and a mismatch surfaces as a missing table on the first query
// rather than at construction.
func WithTablePrefix(prefix string) SQLStoreOption {
	return func(s *SQLStore) { s.prefix = prefix }
}

// WithStoreLogger sets the store's logger.
func WithStoreLogger(logger logging.Logger) SQLStoreOption {
	return func(s *SQLStore) { s.logger = logger }
}

// WithStoreTracerProvider sets the store's tracer provider.
//
// A provider rather than a ready-made tracer, so the instrumentation scope is
// this package's rather than the caller's.
func WithStoreTracerProvider(provider tracing.Provider) SQLStoreOption {
	return func(s *SQLStore) { s.tracerProvider = provider }
}

// WithStorePillars supplies the store's logger and tracer provider at once.
//
// It reads two of the three pillars and drops the metrics provider on purpose:
// this store ships no instruments, and accepting one would be a wiring call
// that appears to have configured something. See [NewSQLStore]. The service's
// [WithServicePillars] takes all three.
func WithStorePillars(pillars *observability.Pillars) SQLStoreOption {
	return func(s *SQLStore) {
		if pillars == nil {
			return
		}

		s.logger = pillars.Logger
		s.tracerProvider = pillars.TracerProvider
	}
}

// ServiceOption configures a [Service] at construction.
type ServiceOption func(*Service)

// WithHooks sets what runs inside each operation's transaction.
//
// Absent, hooks are [NoopHooks] — a consumer with nothing to commit alongside a
// registration configures nothing.
func WithHooks(hooks Hooks) ServiceOption {
	return func(s *Service) {
		if hooks != nil {
			s.hooks = hooks
		}
	}
}

// WithCredentialGenerator replaces how client identifiers and secrets are
// minted.
//
// It exists for tests that need a credential they can predict, and for a
// deployment holding its randomness somewhere this package cannot reach. The
// default reads crypto/rand, and there is deliberately no option that shortens
// what it produces.
func WithCredentialGenerator(generate CredentialGenerator) ServiceOption {
	return func(s *Service) {
		if generate != nil {
			s.generate = generate
		}
	}
}

// WithServiceLogger sets the service's logger.
func WithServiceLogger(logger logging.Logger) ServiceOption {
	return func(s *Service) { s.logger = logger }
}

// WithServiceTracerProvider sets the service's tracer provider.
func WithServiceTracerProvider(provider tracing.Provider) ServiceOption {
	return func(s *Service) { s.tracerProvider = provider }
}

// WithServiceMetricsProvider sets the service's metrics provider.
func WithServiceMetricsProvider(provider metrics.Provider) ServiceOption {
	return func(s *Service) { s.metricsProvider = provider }
}

// WithServicePillars supplies the service's logger, tracer provider and metrics
// provider at once.
func WithServicePillars(pillars *observability.Pillars) ServiceOption {
	return func(s *Service) {
		if pillars == nil {
			return
		}

		s.logger = pillars.Logger
		s.tracerProvider = pillars.TracerProvider
		s.metricsProvider = pillars.MetricsProvider
	}
}

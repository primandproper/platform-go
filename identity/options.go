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

// WithTablePrefix namespaces the identity tables. It must match the
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
// reason SQLStoreOption gives. Hooks are not an option: NewService takes them
// positionally, so a consumer with nothing to commit alongside an identity
// write says so by naming NoopHooks.
type ServiceOption func(*Service)

// WithInvitationMailer attaches the mailer Service.Invite hands an issued
// invitation's token to, after the transaction commits. A nil InvitationMailer
// is ignored.
//
// Configuring one moves the token out of Hooks.AfterInvite: the hook then
// receives the invitation redacted, and the mailer is the only place the
// secret goes. Without one the hook keeps receiving it, so a consumer that
// queues the link from the hook is not broken by upgrading. See
// InvitationMailer.
func WithInvitationMailer(mailer InvitationMailer) ServiceOption {
	return func(s *Service) {
		if mailer != nil {
			s.invitationMailer = mailer
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

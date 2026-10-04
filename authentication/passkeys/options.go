package passkeys

import (
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

// WithTablePrefix namespaces the credential table. It must match the prefix the
// migrations were rendered with; nothing here can check that, and a mismatch
// surfaces as a missing table on the first query rather than at construction.
func WithTablePrefix(prefix string) SQLStoreOption {
	return func(s *SQLStore) { s.prefix = prefix }
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) SQLStoreOption {
	return func(s *SQLStore) { s.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on every read
// and write. An absent provider traces nowhere.
//
// It takes a provider rather than a ready-made tracer so that the spans this
// package emits carry this package's instrumentation scope. A caller-supplied
// tracer would attribute them to whoever built it.
func WithTracerProvider(tracerProvider tracing.Provider) SQLStoreOption {
	return func(s *SQLStore) { s.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent provider records
// nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) SQLStoreOption {
	return func(s *SQLStore) { s.metricsProvider = metricsProvider }
}

// WithPillars attaches a logger, tracer provider, and metrics provider in one
// go. A nil Pillars attaches nothing.
//
// Options apply in order, so a caller can hand over its pillars and then
// override one of them.
func WithPillars(p *observability.Pillars) SQLStoreOption {
	return func(s *SQLStore) { s.logger, s.tracerProvider, s.metricsProvider = p.Deps() }
}

// ServiceOption configures a [Service] at construction.
type ServiceOption func(*Service)

// WithEnrollmentGate sets the check a registration must pass before a passkey
// is added to somebody's account. It is required: see [EnrollmentGate] and
// ErrNoEnrollmentGate.
func WithEnrollmentGate(gate EnrollmentGate) ServiceOption {
	return func(s *Service) { s.gate = gate }
}

// WithUsernameResolver enables the named login, where a person types who they
// are before their authenticator proves it. Absent, [Service.BeginLogin] and
// [Service.FinishLogin] answer ErrNoUsernameResolver and the discoverable login
// is the only one there is.
func WithUsernameResolver(resolve UsernameResolver) ServiceOption {
	return func(s *Service) { s.usernames = resolve }
}

// WithAlternativeSignIn tells the last-credential guard how to learn whether a
// user has a way in other than their passkeys — a password, usually. Absent,
// the guard assumes they have none and refuses to archive anybody's last live
// passkey. See [Service.ArchiveCredential].
func WithAlternativeSignIn(check AlternativeSignIn) ServiceOption {
	return func(s *Service) { s.alternative = check }
}

// WithoutLastCredentialGuard turns the last-credential guard off, so a user may
// archive every passkey they hold whatever else they have. It is the named opt
// out for a deployment whose recovery flow is the answer to a locked-out user;
// see [Service.ArchiveCredential].
func WithoutLastCredentialGuard() ServiceOption {
	return func(s *Service) { s.unguarded = true }
}

// WithServiceLogger sets the service's logger. An absent logger logs nowhere.
func WithServiceLogger(logger logging.Logger) ServiceOption {
	return func(s *Service) { s.logger = logger }
}

// WithServiceTracerProvider sets the service's tracer provider. An absent
// provider traces nowhere.
func WithServiceTracerProvider(tracerProvider tracing.Provider) ServiceOption {
	return func(s *Service) { s.tracerProvider = tracerProvider }
}

// WithServiceMetricsProvider sets the service's metrics provider. An absent
// provider records nothing.
func WithServiceMetricsProvider(metricsProvider metrics.Provider) ServiceOption {
	return func(s *Service) { s.metricsProvider = metricsProvider }
}

// WithServicePillars sets the service's logger, tracer provider and metrics
// provider at once. A nil Pillars attaches nothing.
func WithServicePillars(p *observability.Pillars) ServiceOption {
	return func(s *Service) { s.logger, s.tracerProvider, s.metricsProvider = p.Deps() }
}

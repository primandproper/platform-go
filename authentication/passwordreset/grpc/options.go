package grpc

import (
	"context"

	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// ScopeResolver says whose directory a request is against.
//
// It exists for signin/grpc's reason, and here it is the only way the scope can
// arrive: every RPC on this service is for somebody who cannot sign in, so there
// is no principal to read it off and a scope on the request message would let an
// anonymous caller name the directory a reset is made against. It comes off the
// connection instead, and what on the connection carries it is the consumer's —
// a host, a piece of metadata, a client certificate, a resolver of their own.
//
// Returning an error refuses the request with codes.InvalidArgument, which is the
// honest answer for a request that arrived somewhere it cannot be placed.
//
// A consumer running this service beside signin/grpc gives both the same
// resolver. Two that disagree are a reset mailed from one directory and a
// sign-in attempted in another, which fails by finding nobody.
type ScopeResolver func(ctx context.Context) (tenancy.Scope, error)

// GlobalScope is the ScopeResolver a server uses when a consumer names none:
// every request is against tenancy.Global.
//
// It is the right default and not a lax one. A single-tenant application is what
// tenancy.Global exists for and behaves exactly as an unscoped one would. A
// multi-tenant deployment that forgot to configure a resolver looks up every
// address in the global directory, which has no users in it — so the failure is a
// reset flow that mails nobody rather than one that mails somebody else's user.
func GlobalScope(context.Context) (tenancy.Scope, error) {
	return tenancy.Global(), nil
}

// Option configures a Server.
type Option func(*Server)

// WithLogger sets the logger. Absent means no logging.
func WithLogger(logger logging.Logger) Option {
	return func(s *Server) { s.logger = logger }
}

// WithTracerProvider sets the tracer provider. Absent means no tracing.
//
// It is a provider rather than a ready-made tracer so that the instrumentation
// scope of this package's spans is decided here rather than by the caller.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(s *Server) { s.tracerProvider = tracerProvider }
}

// WithMetricsProvider sets the metrics provider. Absent means no metrics.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(s *Server) { s.metricsProvider = metricsProvider }
}

// WithPillars supplies logger, tracer provider and metrics provider at once.
//
// Options apply in order, so WithPillars(p) followed by WithMetricsProvider(nil)
// leaves this one component unmetered.
func WithPillars(p *observability.Pillars) Option {
	return func(s *Server) { s.logger, s.tracerProvider, s.metricsProvider = p.Deps() }
}

// WithScopeResolver sets what decides whose directory a request is against. A
// nil resolver is ignored, leaving GlobalScope.
func WithScopeResolver(resolve ScopeResolver) Option {
	return func(s *Server) {
		if resolve != nil {
			s.scopes = resolve
		}
	}
}

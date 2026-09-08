package grpc

import (
	"context"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
	"github.com/primandproper/primitives-go/tenancy"
)

// Principal is who is calling, as the consumer's authentication interceptor put
// it on the request context.
//
// It is identity/grpc's, aliased rather than redefined, so a consumer writes one
// extractor and both services read the same answer. Defining a second interface
// with the same three methods would be two chances to disagree about who is
// calling, in the two packages where that disagreement costs the most.
type Principal = identitygrpc.Principal

// PrincipalExtractor resolves a Principal off a request context, reporting
// whether there was one. It is identity/grpc's — see [Principal].
//
// Four of this service's RPCs need one and refuse without it. The other three
// are sign-in and do not ask.
type PrincipalExtractor = identitygrpc.PrincipalExtractor

// ScopeResolver says whose directory a request is against.
//
// It exists because this is the one service in the module where the scope cannot
// come off the principal: a caller signing in has not proved they are one yet,
// and a scope on the request message would let them name the directory their
// password is checked against. So it comes off the connection instead, and what
// on the connection carries it is the consumer's — a host, a piece of metadata,
// a client certificate, a resolver of their own.
//
// Returning an error refuses the request with codes.InvalidArgument, which is
// the honest answer for a request that arrived somewhere it cannot be placed.
//
// It applies to the authenticated RPCs too, and does not have to: they have a
// principal carrying a scope. It is used anyway, so that one wiring decision
// governs the whole service rather than the scope arriving one way for three
// methods and another way for four — see [WithScopeResolver] for what a
// consumer resolving both must keep true.
type ScopeResolver func(ctx context.Context) (tenancy.Scope, error)

// GlobalScope is the ScopeResolver a server uses when a consumer names none:
// every request is against tenancy.Global.
//
// It is the right default and not a lax one. A single-tenant application is what
// tenancy.Global exists for and behaves exactly as an unscoped one would. A
// multi-tenant deployment that forgot to configure a resolver looks up every
// handle in the global directory, which has no users in it, so the failure is a
// sign-in that refuses everybody rather than one that admits somebody to the
// wrong tenant.
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
//
// A consumer whose resolver reads the connection and whose authentication
// interceptor reads a token owes one thing: the two must agree. A token minted
// in one directory, presented on a connection that resolves to another, is
// refused by the store rather than honored — every read filters on the scope it
// is handed and the user is not in that one — so the failure is a read that finds
// nobody. That is the safe direction and it is still a confusing one, which is
// why the scope belongs in the token's claims: signin.DefaultClaims puts it
// there.
func WithScopeResolver(resolve ScopeResolver) Option {
	return func(s *Server) {
		if resolve != nil {
			s.scopes = resolve
		}
	}
}

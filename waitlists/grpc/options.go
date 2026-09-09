package grpc

import (
	"context"

	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
	"github.com/primandproper/primitives-go/tenancy"
)

// ScopeResolver says whose catalog of waitlists a request is against, for the
// requests that arrive with nobody on them.
//
// It is here for the reason authentication/signin/grpc has one: three of this
// service's RPCs are reachable by somebody who has not signed in, and a scope on
// the request message would let them name the tenant whose lists they join. So
// for those it comes off the connection instead, and what on the connection
// carries it is the consumer's — a host, a piece of metadata, a client
// certificate, a resolver of their own.
//
// It is asked only where there is no principal. A request carrying one takes its
// tenant from [Principal.Scope], which is a fact the consumer's authentication
// interceptor proved, and this is the answer for a request where nothing was
// proved because nothing had to be. So each call has exactly one source and the
// two never race; what a consumer resolving both owes is that they agree — see
// [WithScopeResolver].
//
// Returning an error refuses the request with codes.InvalidArgument, which is
// the honest answer for a request that arrived somewhere it cannot be placed.
type ScopeResolver func(ctx context.Context) (tenancy.Scope, error)

// GlobalScope is the ScopeResolver a server uses when a consumer names none:
// every anonymous request is against tenancy.Global.
//
// It is the right default and not a lax one. A single-tenant application is what
// tenancy.Global exists for and behaves exactly as an unscoped one would. A
// multi-tenant deployment that forgot to configure a resolver offers every
// anonymous visitor the global catalog, which holds no lists, so the failure is
// a signup page with nothing on it rather than one that joins somebody to the
// wrong tenant's list.
func GlobalScope(context.Context) (tenancy.Scope, error) {
	return tenancy.Global(), nil
}

// Option configures a [Server] at construction.
type Option func(*Server)

// WithScopeResolver sets what decides whose catalog an anonymous request is
// against. A nil resolver is ignored, leaving [GlobalScope].
//
// A consumer whose resolver reads the connection and whose authentication
// interceptor reads a token owes one thing: the two must agree. A person who
// joins a list through a host that resolves to one tenant, and an operator whose
// token names another, are looking at two catalogs — the signup lands where the
// anonymous request was placed and the console never shows it. Nothing crosses a
// tenant boundary in either direction, because every statement binds whichever
// scope answered; what goes wrong is that the row is somewhere nobody is looking.
func WithScopeResolver(resolve ScopeResolver) Option {
	return func(s *Server) {
		if resolve != nil {
			s.scopes = resolve
		}
	}
}

// WithLogger sets the server's logger.
func WithLogger(logger logging.Logger) Option {
	return func(s *Server) { s.logger = logger }
}

// WithTracerProvider sets the server's tracer provider.
//
// A provider rather than a ready-made tracer, so the instrumentation scope is
// this package's rather than the caller's.
func WithTracerProvider(provider tracing.Provider) Option {
	return func(s *Server) { s.tracerProvider = provider }
}

// WithMetricsProvider sets the server's metrics provider.
func WithMetricsProvider(provider metrics.Provider) Option {
	return func(s *Server) { s.metricsProvider = provider }
}

// WithPillars supplies logger, tracer provider and metrics provider at once.
//
// Options apply in order, so WithPillars(p) followed by WithMetricsProvider(nil)
// leaves this server unmetered.
func WithPillars(pillars *observability.Pillars) Option {
	return func(s *Server) {
		if pillars == nil {
			return
		}

		s.logger = pillars.Logger
		s.tracerProvider = pillars.TracerProvider
		s.metricsProvider = pillars.MetricsProvider
	}
}

package grpc

import (
	"context"

	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
	"github.com/primandproper/primitives-go/tenancy"
)

// ScopeResolver says whose log a request is against.
//
// It is the whole of this service's tenancy, and it reads the scope off the
// connection — a host, a piece of metadata, a client certificate, a principal
// the consumer's authentication interceptor already resolved. What carries it
// is the consumer's; that it is not the request is this package's.
//
// audit.Query has a scope selector in which nil means every tenant's events,
// and its own documentation says getting that backwards is a cross-tenant
// disclosure rather than a wrong answer. In a process it is a capability an
// operator built deliberately, by holding a Reader and passing the query
// themselves. In a request field it would be a capability every caller has, so
// there is no such field: the schema reserves the name, and the query this
// service binds is the caller's own scope with whatever else they narrowed by.
//
// Returning an error refuses the request with codes.InvalidArgument, which is
// the honest answer for a request that arrived somewhere it cannot be placed.
//
// It is authentication/signin/grpc's seam, deliberately not shared with it: the
// two resolve the same fact from the same place, and a log reader that imported
// the sign-in service to spell a function type would carry a directory into its
// dependency graph for a signature.
type ScopeResolver func(ctx context.Context) (tenancy.Scope, error)

// GlobalScope is the ScopeResolver a single-tenant deployment names: every
// request is against tenancy.Global, which is the scope an application that
// records no tenant on its entries has been writing to all along.
//
// It has to be named. Unlike authentication/signin/grpc, which defaults to this
// and gets a sign-in that refuses everybody when a multi-tenant deployment
// forgets, the failure here would be a console reading the platform chain and
// finding almost nothing in it. An audit log that answers "no entries" is the
// one wrong answer in this package that looks like a quiet system rather than
// like a broken one, so the deployment that wants it says so — see
// [ErrNilScopeResolver].
func GlobalScope(context.Context) (tenancy.Scope, error) {
	return tenancy.Global(), nil
}

// Option configures a Server.
type Option func(*Server)

// WithScopeResolver sets what decides whose log a request is against. It is
// required: a Server built without it, or given nil here, is refused with
// [ErrNilScopeResolver].
//
// It is the one option in this package with nothing behind it. The others are
// observability and absent means noop; this one is the whole of the service's
// tenancy, and the deployment that wants every request against tenancy.Global
// writes WithScopeResolver([GlobalScope]) and has said so — see
// [ErrNilScopeResolver] for why that sentence is not a default.
//
// Options apply in order, so a later WithScopeResolver(nil) clears an earlier
// resolver and the constructor refuses, which is the same reading
// [WithMetricsProvider] already takes of a nil provider after [WithPillars].
//
// A consumer whose resolver reads the connection and whose authentication
// interceptor reads a token owes one thing: the two must agree. A console whose
// token names one tenant, connecting somewhere that resolves to another, reads
// the second tenant's chain — every query binds whichever scope answered, so
// nothing crosses a boundary, but the log on the screen is not the log the
// operator believes they are auditing.
func WithScopeResolver(resolve ScopeResolver) Option {
	return func(s *Server) { s.scopes = resolve }
}

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

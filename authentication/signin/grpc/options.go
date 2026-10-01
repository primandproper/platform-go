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
// there. The administrative RPCs, whose subject is named by the request rather
// than being the caller, do not read the resolver at all: they act in the
// operator's own directory, the principal's Scope.
func WithScopeResolver(resolve ScopeResolver) Option {
	return func(s *Server) {
		if resolve != nil {
			s.scopes = resolve
		}
	}
}

// SignInAnnotator answers what the consumer recorded about the devices behind
// a person's logins: for each family it has something for, a map of whatever it
// chose to record — a device name, a user agent, the address a sign-in came
// from. ListSignIns and ListSignInsForUser call it once per answer, with the
// scope the listing ran in, the person whose logins they are, and every family
// the listing is about to return, and each entry's attributes are what it
// answered for that family.
//
// It is the other half of signin's refusal to store any of it. The platform
// lists the logins and records how each one happened; whether a device is
// recorded at all, under which keys and for how long, is the consumer's, and
// signin.Hooks.AfterIssueToken is where they record it, keyed on the family.
// This is how what they recorded reaches the listing RPCs' answer, so a
// consumer showing it needs no list RPC of their own.
//
// A family it has nothing for gets no attributes, and a family it answers for
// that the listing did not return is ignored. An error fails the listing: an
// answer with the logins and none of their devices would be a "where you're
// signed in" screen that silently stopped saying where, which is the half-answer
// this surface does not give. The error is handed back the way the service's
// are — see the package documentation — so a sentinel the consumer mapped
// keeps its code.
type SignInAnnotator func(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	familyIDs []string,
) (map[string]map[string]string, error)

// WithoutOpenRegistration builds the server with its sign-up door closed:
// Register is refused before the service sees it, with
// signin.ErrRegistrationClosed as codes.Unimplemented, which carries the
// client-safe reason REGISTRATION_CLOSED once errormappers.Register has run.
// The reason is the point: a bare Unimplemented is also what a server answers
// for an RPC it never mounted, and a client — or the conformance suite — that
// cannot tell a closed door from a broken one cannot say which it is looking at.
//
// It is for a deployment that does not want sign-up at all, and it closes the
// door to everybody, signed in or not. A deployment that wants sign-up for
// some people and not others keeps the door open and says who in its
// signin.RegistrationPolicy, which reads the caller off the context. Register
// stays in [AnonymousMethods] either way: the lists are fixed, and the server
// is the one place that decides.
//
// A deployment built from signincfg does not pass it by hand: naming
// Registration.Closed, or Registration.Disabled, puts it among the config's
// ServerOptions, which service's mount reads.
func WithoutOpenRegistration() Option {
	return func(s *Server) { s.registrationClosed = true }
}

// WithSignInAnnotator sets what fills each listed login's attributes. A nil
// annotator is ignored. Absent one, every login is listed with none.
func WithSignInAnnotator(annotate SignInAnnotator) Option {
	return func(s *Server) {
		if annotate != nil {
			s.annotate = annotate
		}
	}
}

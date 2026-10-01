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
// It is authentication/signin/grpc's ScopeResolver for the same reason: the
// login half of this service is anonymous, so the scope cannot come off a
// principal, and a scope on the request would let the caller name the
// directory their passkey is checked against. It comes off the connection
// instead. Returning an error refuses the request with codes.InvalidArgument.
//
// A deployment running this beside signin/grpc gives both the same resolver:
// the token FinishLogin mints is minted in the scope this one names.
type ScopeResolver func(ctx context.Context) (tenancy.Scope, error)

// GlobalScope is the ScopeResolver a server uses when a consumer names none,
// for the reason signin/grpc's GlobalScope gives: a single-tenant deployment is
// what tenancy.Global is for, and a multi-tenant one that forgot to name a
// resolver finds nobody rather than somebody in the wrong tenant.
func GlobalScope(context.Context) (tenancy.Scope, error) {
	return tenancy.Global(), nil
}

// UserHandle answers the WebAuthn user handle a signed-in user registers
// under: the value the service's UserResolver maps back to them.
//
// It is how the self-service half derives the handle from the principal
// rather than from the request, which is the whole of what keeps an
// enrollment on the caller's own account. The service checks the answer — a
// handle that resolves to anybody but the caller is passkeys.ErrHandleMismatch
// — so a UserHandle that disagrees with the UserResolver refuses every
// enrollment rather than enrolling somebody else.
type UserHandle func(ctx context.Context, scope tenancy.Scope, userID string) ([]byte, error)

// UserIDHandle is the UserHandle a server uses when a consumer names none: the
// user's ID, as bytes. It pairs with a UserResolver that reads a handle as the
// user ID it spells, and it is safe to put on an authenticator because
// identity's IDs are opaque xids rather than anything personally identifying.
// A deployment that wants random handles brings its own UserHandle and
// UserResolver as a pair. The default stays as it is: changing it would break
// every resolver that reads the handle as an ID, and every discoverable
// credential already on an authenticator.
func UserIDHandle(_ context.Context, _ tenancy.Scope, userID string) ([]byte, error) {
	return []byte(userID), nil
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

// WithUserHandle sets how a signed-in user's WebAuthn handle is derived. A nil
// one is ignored, leaving UserIDHandle.
func WithUserHandle(handles UserHandle) Option {
	return func(s *Server) {
		if handles != nil {
			s.handles = handles
		}
	}
}

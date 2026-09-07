package authserver

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v14/observability"
	"github.com/primandproper/platform-go/v14/observability/logging"
	"github.com/primandproper/platform-go/v14/observability/metrics"
	"github.com/primandproper/platform-go/v14/observability/tracing"
	"github.com/primandproper/platform-go/v14/tenancy"
)

// ScopeResolver reports which registry an authorization request belongs to.
//
// It is a seam rather than a value because a caller signing in has not become a
// principal yet: there is nobody on the context to read a scope off, so it has
// to come from the connection — the host header, a path segment, a subdomain, a
// header a gateway set. Which of those is the deployment's, and this module
// cannot guess it.
//
// It is the same shape and the same reasoning authentication/signin/grpc's
// ScopeResolver has, and a deployment that has answered the question once
// answers it the same way here.
//
// It belongs to [Authenticator] alone. A resolver reports its own scope instead
// — see [ScopedSubjectResolver] for why the two seams cannot answer this the
// same way.
type ScopeResolver func(ctx context.Context, req *http.Request) (tenancy.Scope, error)

// GlobalScope is the default [ScopeResolver]: every authorization request
// belongs to tenancy.Global().
//
// It is the correct default rather than a placeholder. A single-tenant
// deployment is entirely described by it, and a multi-tenant one whose
// registrations are all administered is too — Global() is the registry a client
// belonging to nobody in particular lives in. What it does not describe is a
// deployment whose people hold personal credentials per tenant, and that is the
// deployment that has a way to tell the tenants apart and should say so with
// [WithScopeResolver].
//
// It is safe as a default only because the scope it returns is handed to
// signin.Service.LoginForToken before it reaches [oauth2clients.Client.Admits]:
// a person who cannot sign in to the global registry never reaches the check at
// all. See [ScopedSubjectResolver] for the seam where that is not true and where
// there is consequently no default.
func GlobalScope(context.Context, *http.Request) (tenancy.Scope, error) {
	return tenancy.Global(), nil
}

// AuthenticatorOption configures an [Authenticator] at construction.
type AuthenticatorOption func(*Authenticator)

// WithScopeResolver sets how an authorization request's registry is decided.
func WithScopeResolver(resolve ScopeResolver) AuthenticatorOption {
	return func(a *Authenticator) {
		if resolve != nil {
			a.scopes = resolve
		}
	}
}

// WithMismatchMessage replaces what the login form says when the person signed
// in and this client is not one they may use.
//
// Write it for a person. It is rendered into a page, and it is the only string
// from this seam that reaches one. An empty message leaves
// [DefaultMismatchMessage] in place rather than rendering a blank alert.
func WithMismatchMessage(message string) AuthenticatorOption {
	return func(a *Authenticator) {
		if message != "" {
			a.message = message
		}
	}
}

// WithAdministrativeLogin sends the sign-in through authentication/signin's
// administrative door: the person must hold one of the service roles that
// service names, and must hold a proven second factor whatever its policy says.
//
// It is for an authorization server fronting an operator tool rather than a
// product. A remote MCP endpoint exposing administrative actions is the case it
// exists for, and it is the right place for the restriction because there is no
// client-side check that fixes "anybody with an account may sign in here".
//
// A service that named no administrative roles has no administrative door, and
// every sign-in through this authenticator is then refused — which is the
// failure to have at construction rather than in production, and is why this is
// an option a deployment sets deliberately rather than something inferred.
func WithAdministrativeLogin() AuthenticatorOption {
	return func(a *Authenticator) { a.administrative = true }
}

// WithLogger sets the authenticator's logger.
func WithLogger(logger logging.Logger) AuthenticatorOption {
	return func(a *Authenticator) { a.opts.logger = logger }
}

// WithTracerProvider sets the authenticator's tracer provider.
//
// A provider rather than a ready-made tracer, so the instrumentation scope is
// this package's rather than the caller's.
func WithTracerProvider(provider tracing.Provider) AuthenticatorOption {
	return func(a *Authenticator) { a.opts.tracerProvider = provider }
}

// WithMetricsProvider sets the authenticator's metrics provider.
func WithMetricsProvider(provider metrics.Provider) AuthenticatorOption {
	return func(a *Authenticator) { a.opts.metricsProvider = provider }
}

// WithPillars supplies the authenticator's three pillars at once.
func WithPillars(pillars *observability.Pillars) AuthenticatorOption {
	return func(a *Authenticator) { a.opts.setPillars(pillars) }
}

// ResolverOption configures a [GuardedResolver] at construction.
type ResolverOption func(*GuardedResolver)

// WithResolverLogger sets the guarded resolver's logger.
//
// It is worth setting. A resolver's refusal is a decline rather than an error —
// see [GuardedResolver] — so the log line is the only place it appears.
func WithResolverLogger(logger logging.Logger) ResolverOption {
	return func(r *GuardedResolver) { r.opts.logger = logger }
}

// WithResolverTracerProvider sets the guarded resolver's tracer provider.
func WithResolverTracerProvider(provider tracing.Provider) ResolverOption {
	return func(r *GuardedResolver) { r.opts.tracerProvider = provider }
}

// WithResolverMetricsProvider sets the guarded resolver's metrics provider.
func WithResolverMetricsProvider(provider metrics.Provider) ResolverOption {
	return func(r *GuardedResolver) { r.opts.metricsProvider = provider }
}

// WithResolverPillars supplies the guarded resolver's three pillars at once.
func WithResolverPillars(pillars *observability.Pillars) ResolverOption {
	return func(r *GuardedResolver) { r.opts.setPillars(pillars) }
}

// StoreOption configures a [Store] at construction.
type StoreOption func(*Store)

// WithStoreLogger sets the store decorator's logger.
func WithStoreLogger(logger logging.Logger) StoreOption {
	return func(s *Store) { s.opts.logger = logger }
}

// WithStoreTracerProvider sets the store decorator's tracer provider.
func WithStoreTracerProvider(provider tracing.Provider) StoreOption {
	return func(s *Store) { s.opts.tracerProvider = provider }
}

// WithStoreMetricsProvider sets the store decorator's metrics provider.
func WithStoreMetricsProvider(provider metrics.Provider) StoreOption {
	return func(s *Store) { s.opts.metricsProvider = provider }
}

// WithStorePillars supplies the store decorator's three pillars at once.
func WithStorePillars(pillars *observability.Pillars) StoreOption {
	return func(s *Store) { s.opts.setPillars(pillars) }
}

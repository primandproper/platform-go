package authserver

import (
	"context"
	"net/http"

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

// ResolverOption configures a [GuardedResolver] at construction.
type ResolverOption func(*GuardedResolver)

// WithResolverScopeResolver sets how the registry of a request the inner
// resolver answered is decided.
//
// It is a separate option from [WithScopeResolver] because the two seams are
// constructed separately, and a deployment must give both the same answer: a
// resolver reading one scope and an authenticator reading another would guard
// two different things.
func WithResolverScopeResolver(resolve ScopeResolver) ResolverOption {
	return func(r *GuardedResolver) {
		if resolve != nil {
			r.scopes = resolve
		}
	}
}

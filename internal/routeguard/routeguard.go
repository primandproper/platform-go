package routeguard

import (
	"context"
	nethttp "net/http"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzhttp "github.com/primandproper/primitives-go/v2/authorization/http"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/routing"
)

type (
	// Guard checks the grant a route requires before its handler runs, and
	// hands the handler the caller it resolved to decide whether to.
	Guard[T any] struct {
		enforcer *authzhttp.Enforcer
		resolve  func(context.Context) (T, error)
	}

	// resolved is what a guard found on a request, success or failure, so the
	// handler behind it answers from the same resolution.
	resolved[T any] struct {
		caller T
		err    error
	}
)

// New builds a guard over enforcer, resolving callers with resolve.
//
// A nil enforcer is not an open surface. It is replaced by one whose every check
// fails for want of grants, which writes the same 403 a consumer's enforcer
// writes for a caller holding nothing, and counts it as the misconfiguration it
// is: a surface with no way to check a grant has been told nothing about who may
// use it, and the answer to that is the one a fail-closed gRPC enforcer gives an
// undeclared method.
func New[T any](
	enforcer *authzhttp.Enforcer,
	resolve func(context.Context) (T, error),
	logger logging.Logger,
) (*Guard[T], error) {
	if enforcer == nil {
		refusal, err := authzhttp.NewEnforcer(func(context.Context) (authorization.Grants, bool) {
			return authorization.Grants{}, false
		}, authzhttp.WithLogger(logging.EnsureLogger(logger)))
		if err != nil {
			return nil, err
		}

		enforcer = refusal
	}

	return &Guard[T]{enforcer: enforcer, resolve: resolve}, nil
}

// Require is the middleware in front of a route that requires perms.
//
// The grant is checked before the handler runs, so before anything is read: a
// caller without it is refused as 403 whether or not the resource they named
// exists, and the refusal says nothing about which identifiers are real.
//
// A request whose caller does not resolve — nobody on it — goes straight to the
// handler instead, which refuses it the way it always has, as the resolver's own
// error, before it reads anything either. That keeps a request with nobody on
// it answered as what it is, rather than as a 403 claiming somebody was asked
// about and found wanting.
//
// Either way the resolution rides the request's context, keyed by the guard
// itself so that two surfaces resolving the same type cannot read each other's
// caller, and Caller answers from it rather than resolving a second time.
func (g *Guard[T]) Require(perms ...authorization.Permission) routing.Middleware {
	require := g.enforcer.Require(perms...)

	return func(next nethttp.Handler) nethttp.Handler {
		required := require(next)

		return nethttp.HandlerFunc(func(res nethttp.ResponseWriter, req *nethttp.Request) {
			caller, err := g.resolve(req.Context())
			req = req.WithContext(context.WithValue(req.Context(), g, resolved[T]{caller: caller, err: err}))

			if err != nil {
				next.ServeHTTP(res, req)

				return
			}

			required.ServeHTTP(res, req)
		})
	}
}

// Caller is the request's caller: the one Require resolved, on a guarded
// route, or a fresh resolution on a route with no guard in front of it.
func (g *Guard[T]) Caller(ctx context.Context) (T, error) {
	if r, ok := ctx.Value(g).(resolved[T]); ok {
		return r.caller, r.err
	}

	return g.resolve(ctx)
}

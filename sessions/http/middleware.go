package http

import (
	"context"
	stderrors "errors"
	"net/http"

	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/sessions"
)

// contextKey types the context value. It is generic so that two Managers over
// different payload types can both attach to one request without colliding, and
// so that SessionFromContext for the wrong T finds nothing rather than
// panicking on a type assertion.
type contextKey[T any] struct{}

// WithSession returns a context carrying session. Middleware does this for
// you; it is exported for tests and for handlers that load a session by some
// other route.
func WithSession[T any](ctx context.Context, session *sessions.Session[T]) context.Context {
	return context.WithValue(ctx, contextKey[T]{}, session)
}

// SessionFromContext returns the session carried by ctx, if any.
//
// The second return is false for an anonymous request, which on most services
// is most of them. It is not an error, and handlers that require a session
// should say so themselves — this package deliberately does not decide what an
// unauthenticated request deserves, because the answer differs per route
// between a 401, a redirect, and a perfectly good anonymous page.
func SessionFromContext[T any](ctx context.Context) (*sessions.Session[T], bool) {
	session, ok := ctx.Value(contextKey[T]{}).(*sessions.Session[T])

	return session, ok && session != nil
}

// Middleware loads the request's session and attaches it to the context.
//
// A request with no session, or with one that has expired, passes through
// untouched — SessionFromContext then reports false and the handler decides
// what that means. So does a request whose session could not be read because
// the store is unreachable: the failure is logged and traced, and the request
// is served as anonymous. That is the conservative direction, since every
// handler that requires a session will refuse, and it keeps a store outage from
// turning every page on the site into a 500.
//
// It is safe to install globally. It reads one cookie and, at most, performs
// one store lookup.
func (m *Manager[T]) Middleware() routing.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			ctx, op := m.o11y.Begin(req.Context())
			defer op.End()

			session, err := m.Load(ctx, req)
			if err != nil {
				if !stderrors.Is(err, sessions.ErrNotFound) {
					op.Acknowledge(err, "loading session for request")
				}

				next.ServeHTTP(res, req.WithContext(ctx))

				return
			}

			next.ServeHTTP(res, req.WithContext(WithSession(ctx, session)))
		})
	}
}

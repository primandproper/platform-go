package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/sessions"
	sessionsmock "github.com/primandproper/platform-go/v13/sessions/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// capture records what the handler behind the middleware saw.
type capture struct {
	session *sessions.Session[principal]
	present bool
	served  bool
}

// serve runs one request through the middleware and reports what reached the
// handler.
func serve(t *testing.T, manager *Manager[principal], req *http.Request) *capture {
	t.Helper()

	got := &capture{}

	manager.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got.served = true
		got.session, got.present = SessionFromContext[principal](r.Context())
	})).ServeHTTP(httptest.NewRecorder(), req)

	return got
}

func TestManager_Middleware(T *testing.T) {
	T.Parallel()

	T.Run("attaches the request's session", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)
		session, req := issue(t, manager, &principal{UserID: "u_1", Admin: true})

		got := serve(t, manager, req)
		must.True(t, got.present)
		test.EqOp(t, session.ID, got.session.ID)
		test.True(t, got.session.Data.Admin)
	})

	// Whether an anonymous request deserves a 401, a redirect, or a perfectly
	// good anonymous page is a per-route question this package cannot answer.
	T.Run("passes an anonymous request through", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

		got := serve(t, manager, req)
		test.True(t, got.served)
		test.False(t, got.present)
	})

	T.Run("passes an expired session through as anonymous", func(t *testing.T) {
		t.Parallel()

		manager, c := newTestManager(t, sessions.WithIdleTimeout(10*time.Minute))
		_, req := issue(t, manager, &principal{UserID: "u_1"})

		c.advance(10 * time.Minute)

		got := serve(t, manager, req)
		test.True(t, got.served)
		test.False(t, got.present)
	})

	// A store outage must not turn every page on the site into a 500. Every
	// handler that requires a session refuses anyway, which is the conservative
	// direction.
	T.Run("serves a request as anonymous when the store cannot be read", func(t *testing.T) {
		t.Parallel()

		store := &sessionsmock.StoreMock[principal]{
			PolicyFunc: func() sessions.Policy { return sessions.Policy{Idle: time.Hour} },
			NewFunc: func(_ context.Context, data *principal) (*sessions.Session[principal], error) {
				return &sessions.Session[principal]{ID: "id-1", Data: data}, nil
			},
			GetFunc: func(context.Context, string) (*sessions.Session[principal], error) {
				return nil, platformerrors.New("store is down")
			},
		}

		manager, err := NewManager[principal](store, newCookieManager(t))
		must.NoError(t, err)

		issued := httptest.NewRecorder()

		_, err = manager.Issue(t.Context(), issued, &principal{UserID: "u_1"})
		must.NoError(t, err)

		got := serve(t, manager, requestWithCookies(t, issued))
		test.True(t, got.served)
		test.False(t, got.present)
	})
}

func TestSessionFromContext(T *testing.T) {
	T.Parallel()

	T.Run("returns what WithSession attached", func(t *testing.T) {
		t.Parallel()

		want := &sessions.Session[principal]{ID: "id-1", Data: &principal{UserID: "u_1"}}

		got, ok := SessionFromContext[principal](WithSession(t.Context(), want))
		must.True(t, ok)
		test.EqOp(t, want, got)
	})

	T.Run("reports nothing for a bare context", func(t *testing.T) {
		t.Parallel()

		got, ok := SessionFromContext[principal](t.Context())
		test.False(t, ok)
		test.Nil(t, got)
	})

	// The context key is generic, so two managers over different payload types
	// can both attach to one request and neither reads the other's session.
	T.Run("does not answer for another payload type", func(t *testing.T) {
		t.Parallel()

		type other struct{ Name string }

		ctx := WithSession(t.Context(), &sessions.Session[principal]{ID: "id-1"})

		got, ok := SessionFromContext[other](ctx)
		test.False(t, ok)
		test.Nil(t, got)
	})

	// A nil session attached deliberately still reads as "no session", so a
	// handler's ok check cannot be satisfied by something it must not
	// dereference.
	T.Run("reports nothing for a nil session", func(t *testing.T) {
		t.Parallel()

		got, ok := SessionFromContext[principal](WithSession[principal](t.Context(), nil))
		test.False(t, ok)
		test.Nil(t, got)
	})
}

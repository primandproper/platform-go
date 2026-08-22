package http

import (
	"context"
	stderrors "errors"
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

func TestNewManager(T *testing.T) {
	T.Parallel()

	T.Run("requires a store", func(t *testing.T) {
		t.Parallel()

		_, err := NewManager[principal](nil, newCookieManager(t))
		test.ErrorIs(t, err, ErrNilStore)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	// No default cookie manager: an unsigned cookie would let a client present
	// any identifier it liked, and that is not a property to acquire by
	// omission.
	T.Run("requires a cookie manager", func(t *testing.T) {
		t.Parallel()

		store := &sessionsmock.StoreMock[principal]{}

		_, err := NewManager[principal](store, nil)
		test.ErrorIs(t, err, ErrNilCookieManager)
	})
}

func TestManager_Issue(T *testing.T) {
	T.Parallel()

	T.Run("establishes a session and sets its cookie", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)
		res := httptest.NewRecorder()

		session, err := manager.Issue(t.Context(), res, &principal{UserID: "u_1"})
		must.NoError(t, err)

		cookie := sessionCookie(t, res)
		must.NotNil(t, cookie)
		test.NotEq(t, "", cookie.Value)

		// The identifier is signed and encrypted rather than written in the
		// clear, so it cannot be read out of the cookie or forged into one.
		test.StrNotContains(t, cookie.Value, session.ID)
	})

	// The attributes the cookie manager supplies, asserted here because a
	// session cookie without them is the vulnerability rather than a nicety.
	T.Run("the cookie carries its security attributes", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)
		res := httptest.NewRecorder()

		_, err := manager.Issue(t.Context(), res, &principal{UserID: "u_1"})
		must.NoError(t, err)

		cookie := sessionCookie(t, res)
		must.NotNil(t, cookie)
		test.True(t, cookie.HttpOnly)
		test.EqOp(t, http.SameSiteLaxMode, cookie.SameSite)
		test.EqOp(t, "/", cookie.Path)
	})

	// Derived from the absolute timeout, not from ExpiresAt: a cookie cut to
	// the idle deadline would expire in the browser after one idle window even
	// for a user who never stopped clicking.
	T.Run("the cookie outlives the idle window", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t,
			sessions.WithAbsoluteTimeout(24*time.Hour), sessions.WithIdleTimeout(10*time.Minute))
		res := httptest.NewRecorder()

		_, err := manager.Issue(t.Context(), res, &principal{UserID: "u_1"})
		must.NoError(t, err)

		cookie := sessionCookie(t, res)
		must.NotNil(t, cookie)
		test.EqOp(t, int((24 * time.Hour).Seconds()), cookie.MaxAge)
	})

	T.Run("leaves the cookie manager's lifetime alone with no absolute timeout", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t,
			sessions.WithAbsoluteTimeout(0), sessions.WithIdleTimeout(10*time.Minute))
		res := httptest.NewRecorder()

		_, err := manager.Issue(t.Context(), res, &principal{UserID: "u_1"})
		must.NoError(t, err)

		cookie := sessionCookie(t, res)
		must.NotNil(t, cookie)
		test.EqOp(t, int((24 * time.Hour).Seconds()), cookie.MaxAge)
	})

	// A session the client can never name is a live session nobody can reach
	// and nobody can revoke, so it is removed rather than left to idle out.
	T.Run("discards the session when the cookie cannot be written", func(t *testing.T) {
		t.Parallel()

		deleted := ""
		store := &sessionsmock.StoreMock[principal]{
			NewFunc: func(_ context.Context, data *principal) (*sessions.Session[principal], error) {
				return &sessions.Session[principal]{ID: "id-1", Data: data}, nil
			},
			PolicyFunc: func() sessions.Policy { return sessions.Policy{Idle: time.Hour} },
			DeleteFunc: func(_ context.Context, id string) error {
				deleted = id

				return nil
			},
		}

		manager, err := NewManager[principal](store, newCookieManager(t))
		must.NoError(t, err)

		// A nil ResponseWriter is the reachable stand-in for "the cookie could
		// not be written"; every other failure inside BuildCookie is a
		// misconfigured cookie manager, which construction already rejects.
		_, err = manager.Issue(t.Context(), nil, &principal{UserID: "u_1"})
		must.Error(t, err)
		test.EqOp(t, "id-1", deleted)
	})
}

func TestManager_Load(T *testing.T) {
	T.Parallel()

	T.Run("reads the session named by the cookie", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)
		session, req := issue(t, manager, &principal{UserID: "u_1", Admin: true})

		loaded, err := manager.Load(t.Context(), req)
		must.NoError(t, err)
		test.EqOp(t, session.ID, loaded.ID)
		test.EqOp(t, "u_1", loaded.Data.UserID)
		test.True(t, loaded.Data.Admin)
	})

	// One answer for every unusable cookie, so a client cannot learn from the
	// response whether a guessed identifier ever existed.
	T.Run("every bad cookie is a missing session", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)

		t.Run("no cookie at all", func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

			_, err := manager.Load(t.Context(), req)
			test.ErrorIs(t, err, sessions.ErrNotFound)
		})

		t.Run("a cookie that does not verify", func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			req.AddCookie(&http.Cookie{Name: DefaultCookieName, Value: "forged"})

			_, err := manager.Load(t.Context(), req)
			test.ErrorIs(t, err, sessions.ErrNotFound)
		})

		t.Run("a cookie signed by another key", func(t *testing.T) {
			other, _ := newTestManager(t)
			_, req := issue(t, other, &principal{UserID: "u_1"})

			_, err := manager.Load(t.Context(), req)
			test.ErrorIs(t, err, sessions.ErrNotFound)
		})

		t.Run("a valid cookie naming a session that has ended", func(t *testing.T) {
			session, req := issue(t, manager, &principal{UserID: "u_1"})
			must.NoError(t, manager.store.Delete(t.Context(), session.ID))

			_, err := manager.Load(t.Context(), req)
			test.ErrorIs(t, err, sessions.ErrNotFound)
		})
	})

	T.Run("reports an expired session as expired", func(t *testing.T) {
		t.Parallel()

		manager, c := newTestManager(t, sessions.WithIdleTimeout(10*time.Minute))
		_, req := issue(t, manager, &principal{UserID: "u_1"})

		c.advance(10 * time.Minute)

		_, err := manager.Load(t.Context(), req)
		test.ErrorIs(t, err, sessions.ErrIdleTimeout)
	})

	T.Run("rejects a nil request", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)

		_, err := manager.Load(t.Context(), nil)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})
}

func TestManager_Save(T *testing.T) {
	T.Parallel()

	T.Run("replaces the payload without touching the cookie", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)
		_, req := issue(t, manager, &principal{UserID: "u_1"})

		must.NoError(t, manager.Save(t.Context(), req, &principal{UserID: "u_1", Admin: true}))

		loaded, err := manager.Load(t.Context(), req)
		must.NoError(t, err)
		test.True(t, loaded.Data.Admin)
	})

	T.Run("refuses a request with no session", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

		test.ErrorIs(t, manager.Save(t.Context(), req, &principal{}), sessions.ErrNotFound)
	})
}

func TestManager_Renew(T *testing.T) {
	T.Parallel()

	T.Run("rotates the identifier and rewrites the cookie", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)
		session, req := issue(t, manager, &principal{UserID: "u_1"})

		res := httptest.NewRecorder()

		renewed, err := manager.Renew(t.Context(), res, req)
		must.NoError(t, err)
		test.NotEqOp(t, session.ID, renewed.ID)
		test.EqOp(t, "u_1", renewed.Data.UserID)

		// The new cookie loads the new session.
		loaded, err := manager.Load(t.Context(), requestWithCookies(t, res))
		must.NoError(t, err)
		test.EqOp(t, renewed.ID, loaded.ID)
	})

	// The fixation defense: whatever a client held before the privilege change
	// is worthless after it.
	T.Run("the old cookie stops working", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)
		_, req := issue(t, manager, &principal{UserID: "u_1"})

		_, err := manager.Renew(t.Context(), httptest.NewRecorder(), req)
		must.NoError(t, err)

		_, err = manager.Load(t.Context(), req)
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	T.Run("does not extend the absolute deadline", func(t *testing.T) {
		t.Parallel()

		manager, c := newTestManager(t,
			sessions.WithAbsoluteTimeout(time.Hour), sessions.WithIdleTimeout(55*time.Minute))
		session, req := issue(t, manager, &principal{UserID: "u_1"})

		c.advance(30 * time.Minute)

		res := httptest.NewRecorder()

		renewed, err := manager.Renew(t.Context(), res, req)
		must.NoError(t, err)
		test.EqOp(t, session.CreatedAt, renewed.CreatedAt)

		// And the reissued cookie is cut to what is left, not to a fresh day.
		cookie := sessionCookie(t, res)
		must.NotNil(t, cookie)
		test.EqOp(t, int((30 * time.Minute).Seconds()), cookie.MaxAge)
	})

	T.Run("refuses a request with no session", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

		_, err := manager.Renew(t.Context(), httptest.NewRecorder(), req)
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})
}

func TestManager_End(T *testing.T) {
	T.Parallel()

	T.Run("ends the session and clears the cookie", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)
		_, req := issue(t, manager, &principal{UserID: "u_1"})

		res := httptest.NewRecorder()
		must.NoError(t, manager.End(t.Context(), res, req))

		cookie := sessionCookie(t, res)
		must.NotNil(t, cookie)
		test.EqOp(t, "", cookie.Value)
		test.EqOp(t, -1, cookie.MaxAge)

		_, err := manager.Load(t.Context(), req)
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	// A browser treats a deletion cookie whose attributes differ as a different
	// cookie, and silently keeps the one it was meant to remove.
	T.Run("the clearing cookie matches the one it replaces", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)

		issued := httptest.NewRecorder()

		_, err := manager.Issue(t.Context(), issued, &principal{UserID: "u_1"})
		must.NoError(t, err)

		cleared := httptest.NewRecorder()
		must.NoError(t, manager.End(t.Context(), cleared, requestWithCookies(t, issued)))

		before, after := sessionCookie(t, issued), sessionCookie(t, cleared)
		must.NotNil(t, before)
		must.NotNil(t, after)

		test.EqOp(t, before.Name, after.Name)
		test.EqOp(t, before.Path, after.Path)
		test.EqOp(t, before.Domain, after.Domain)
		test.EqOp(t, before.Secure, after.Secure)
		test.EqOp(t, before.HttpOnly, after.HttpOnly)
		test.EqOp(t, before.SameSite, after.SameSite)
	})

	// Sign-out is idempotent, and a client that has already signed out has
	// nothing to be told.
	T.Run("a request with no session is a success", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

		res := httptest.NewRecorder()
		must.NoError(t, manager.End(t.Context(), res, req))

		// The cookie is still cleared, so one naming a session that no longer
		// exists does not survive the request.
		must.NotNil(t, sessionCookie(t, res))
	})

	// Cleared before the record is touched: whatever happens to the store, the
	// client must not leave holding a usable cookie.
	T.Run("clears the cookie even when the store fails", func(t *testing.T) {
		t.Parallel()

		store := &sessionsmock.StoreMock[principal]{
			PolicyFunc: func() sessions.Policy { return sessions.Policy{Idle: time.Hour} },
			DeleteFunc: func(context.Context, string) error {
				return stderrors.New("store is down")
			},
			NewFunc: func(_ context.Context, data *principal) (*sessions.Session[principal], error) {
				return &sessions.Session[principal]{ID: "id-1", Data: data}, nil
			},
		}

		manager, err := NewManager[principal](store, newCookieManager(t))
		must.NoError(t, err)

		issued := httptest.NewRecorder()

		_, err = manager.Issue(t.Context(), issued, &principal{UserID: "u_1"})
		must.NoError(t, err)

		cleared := httptest.NewRecorder()

		must.Error(t, manager.End(t.Context(), cleared, requestWithCookies(t, issued)))

		cookie := sessionCookie(t, cleared)
		must.NotNil(t, cookie)
		test.EqOp(t, -1, cookie.MaxAge)
	})
}

func TestWithCookieName(T *testing.T) {
	T.Parallel()

	T.Run("uses the configured name", func(t *testing.T) {
		t.Parallel()

		manager, _ := newTestManager(t)

		named, err := NewManager(manager.store, newCookieManager(t), WithCookieName("sid"))
		must.NoError(t, err)

		res := httptest.NewRecorder()

		_, err = named.Issue(t.Context(), res, &principal{UserID: "u_1"})
		must.NoError(t, err)

		test.Nil(t, sessionCookie(t, res))

		req := requestWithCookies(t, res)

		_, err = named.Load(t.Context(), req)
		must.NoError(t, err)

		// The name is part of the signature, so a manager reading the same
		// cookie under a different name rejects it.
		_, err = manager.Load(t.Context(), req)
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	T.Run("an empty name is ignored", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithCookieName("")})
		test.EqOp(t, DefaultCookieName, o.cookieName)
	})
}

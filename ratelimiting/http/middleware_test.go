package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	httperrors "github.com/primandproper/platform-go/v13/errors/http"
	"github.com/primandproper/platform-go/v13/ratelimiting"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// stubLimiter answers however the test tells it to, and records the keys it was
// asked about.
type stubLimiter struct {
	allow func(key string) (bool, error)
	keys  []string
}

func (s *stubLimiter) Allow(_ context.Context, key string) (bool, error) {
	s.keys = append(s.keys, key)

	return s.allow(key)
}

func (s *stubLimiter) Close() error { return nil }

// hintingLimiter is a stubLimiter that also answers RetryHinter.
type hintingLimiter struct {
	*stubLimiter

	delay time.Duration
	ok    bool
}

func (h hintingLimiter) RetryAfter(context.Context, string) (time.Duration, bool) {
	return h.delay, h.ok
}

// alwaysAllow and alwaysRefuse are the two limiters most tests want.
func alwaysAllow() *stubLimiter {
	return &stubLimiter{allow: func(string) (bool, error) { return true, nil }}
}

func alwaysRefuse() *stubLimiter {
	return &stubLimiter{allow: func(string) (bool, error) { return false, nil }}
}

// serve runs one request through mw and reports what the client saw, plus
// whether the wrapped handler ran.
func serve(t *testing.T, mw func(http.Handler) http.Handler, req *http.Request) (*httptest.ResponseRecorder, bool) {
	t.Helper()

	reached := false
	handler := mw(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		reached = true
		res.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec, reached
}

func TestNewMiddleware(T *testing.T) {
	T.Parallel()

	T.Run("refuses to build without a limiter", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(nil, KeyByRemoteAddr())
		must.ErrorIs(t, err, ErrNilLimiter)
		test.Nil(t, mw)
	})

	T.Run("refuses to build without a key function", func(t *testing.T) {
		t.Parallel()

		// There is no default key, because the wrong one is worse than none.
		mw, err := NewMiddleware(alwaysAllow(), nil)
		must.ErrorIs(t, err, ErrNilKeyFunc)
		test.Nil(t, mw)
	})

	T.Run("builds with no observability at all", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(alwaysAllow(), KeyByRemoteAddr())
		must.NoError(t, err)
		test.NotNil(t, mw)
	})
}

func TestMiddleware_Allowed(T *testing.T) {
	T.Parallel()

	T.Run("passes an allowed request to the handler", func(t *testing.T) {
		t.Parallel()

		limiter := alwaysAllow()

		mw, err := NewMiddleware(limiter, KeyByRemoteAddr())
		must.NoError(t, err)

		rec, reached := serve(t, mw, request(t, "203.0.113.7:1"))
		test.True(t, reached)
		test.EqOp(t, http.StatusNoContent, rec.Code)
		test.Eq(t, []string{ipKeyPrefix + "203.0.113.7"}, limiter.keys)
	})

	T.Run("never consults the limiter for an exempted request", func(t *testing.T) {
		t.Parallel()

		// An empty key is the opt-out, and spending a token on it would count
		// the request the extractor just said not to count.
		limiter := alwaysRefuse()

		mw, err := NewMiddleware(limiter, func(*http.Request) (string, error) { return "", nil })
		must.NoError(t, err)

		_, reached := serve(t, mw, request(t, "203.0.113.7:1"))
		test.True(t, reached)
		test.SliceEmpty(t, limiter.keys)
	})

	T.Run("hands the handler the request it was given", func(t *testing.T) {
		t.Parallel()

		// The guard's span covers the check and ends before the handler runs.
		// Passing a context derived from it would keep it open for the whole
		// request and make every trace read as though the limiter took that
		// long.
		mw, err := NewMiddleware(alwaysAllow(), KeyByRemoteAddr())
		must.NoError(t, err)

		req := request(t, "203.0.113.7:1")

		var seen *http.Request
		handler := mw(http.HandlerFunc(func(_ http.ResponseWriter, inner *http.Request) {
			seen = inner
		}))
		handler.ServeHTTP(httptest.NewRecorder(), req)

		must.NotNil(t, seen)
		test.EqOp(t, req, seen)
	})

	T.Run("does not read the request body", func(t *testing.T) {
		t.Parallel()

		// What makes a global Router.Use safe on upload routes, unlike the
		// idempotency middleware.
		mw, err := NewMiddleware(alwaysAllow(), KeyByRemoteAddr())
		must.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/uploads", errorReader{})
		req.RemoteAddr = "203.0.113.7:1"

		_, reached := serve(t, mw, req)
		test.True(t, reached)
	})
}

func TestMiddleware_Refused(T *testing.T) {
	T.Parallel()

	T.Run("answers 429 in the platform envelope", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(alwaysRefuse(), KeyByRemoteAddr())
		must.NoError(t, err)

		rec, reached := serve(t, mw, request(t, "203.0.113.7:1"))
		test.False(t, reached)
		must.EqOp(t, http.StatusTooManyRequests, rec.Code)

		var body httperrors.APIResponse[any]
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		must.NotNil(t, body.Error)
		test.EqOp(t, httperrors.ErrTooManyRequests, body.Error.Code)
	})

	T.Run("says nothing about the key or the limit", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(alwaysRefuse(), KeyByRemoteAddr())
		must.NoError(t, err)

		rec, _ := serve(t, mw, request(t, "203.0.113.7:1"))
		test.StrNotContains(t, rec.Body.String(), "203.0.113.7")
	})

	T.Run("renders through a service's own error encoder", func(t *testing.T) {
		t.Parallel()

		// A service that replaced the platform envelope did so because its
		// clients parse something else; a 429 they cannot parse is a refusal
		// they cannot act on.
		mw, err := NewMiddleware(alwaysRefuse(), KeyByRemoteAddr(),
			WithErrorEncoder(func(_ context.Context, err error) (int, any) {
				must.ErrorIs(t, err, ratelimiting.ErrRateLimited)

				return http.StatusTooManyRequests, map[string]string{"detail": "slow down"}
			}))
		must.NoError(t, err)

		rec, _ := serve(t, mw, request(t, "203.0.113.7:1"))
		test.EqOp(t, http.StatusTooManyRequests, rec.Code)
		test.StrContains(t, rec.Body.String(), "slow down")
	})

	T.Run("writes a status and no body for a nil-bodied encoder", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(alwaysRefuse(), KeyByRemoteAddr(),
			WithErrorEncoder(func(context.Context, error) (int, any) {
				return http.StatusTooManyRequests, nil
			}))
		must.NoError(t, err)

		rec, _ := serve(t, mw, request(t, "203.0.113.7:1"))
		test.EqOp(t, http.StatusTooManyRequests, rec.Code)
		test.EqOp(t, 0, rec.Body.Len())
	})

	T.Run("clamps a status the ResponseWriter could not serve", func(t *testing.T) {
		t.Parallel()

		// A custom encoder is caller code, and an out-of-range status would
		// panic the writer on a request that was already being refused.
		mw, err := NewMiddleware(alwaysRefuse(), KeyByRemoteAddr(),
			WithErrorEncoder(func(context.Context, error) (int, any) {
				return 42, nil
			}))
		must.NoError(t, err)

		rec, _ := serve(t, mw, request(t, "203.0.113.7:1"))
		test.EqOp(t, http.StatusInternalServerError, rec.Code)
	})
}

func TestMiddleware_RetryAfter(T *testing.T) {
	T.Parallel()

	T.Run("passes on the limiter's own estimate", func(t *testing.T) {
		t.Parallel()

		limiter := hintingLimiter{stubLimiter: alwaysRefuse(), delay: 3 * time.Second, ok: true}

		mw, err := NewMiddleware(limiter, KeyByRemoteAddr())
		must.NoError(t, err)

		rec, _ := serve(t, mw, request(t, "203.0.113.7:1"))
		test.EqOp(t, "3", rec.Header().Get(RetryAfterHeader))
	})

	T.Run("rounds a sub-second estimate up rather than down", func(t *testing.T) {
		t.Parallel()

		// Rounded down it would be 0 — an invitation to come straight back for
		// a token that is not there yet.
		limiter := hintingLimiter{stubLimiter: alwaysRefuse(), delay: 200 * time.Millisecond, ok: true}

		mw, err := NewMiddleware(limiter, KeyByRemoteAddr())
		must.NoError(t, err)

		rec, _ := serve(t, mw, request(t, "203.0.113.7:1"))
		test.EqOp(t, "1", rec.Header().Get(RetryAfterHeader))
	})

	T.Run("falls back when the limiter cannot estimate", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(alwaysRefuse(), KeyByRemoteAddr(), WithRetryAfter(5*time.Second))
		must.NoError(t, err)

		rec, _ := serve(t, mw, request(t, "203.0.113.7:1"))
		test.EqOp(t, "5", rec.Header().Get(RetryAfterHeader))
	})

	T.Run("falls back when a hinter declines to answer", func(t *testing.T) {
		t.Parallel()

		limiter := hintingLimiter{stubLimiter: alwaysRefuse(), ok: false}

		mw, err := NewMiddleware(limiter, KeyByRemoteAddr())
		must.NoError(t, err)

		rec, _ := serve(t, mw, request(t, "203.0.113.7:1"))
		test.EqOp(t, "1", rec.Header().Get(RetryAfterHeader))
	})

	T.Run("sends the default without configuration", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(alwaysRefuse(), KeyByRemoteAddr())
		must.NoError(t, err)

		rec, _ := serve(t, mw, request(t, "203.0.113.7:1"))
		test.EqOp(t, "1", rec.Header().Get(RetryAfterHeader))
	})

	T.Run("sends nothing when the fallback is suppressed", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(alwaysRefuse(), KeyByRemoteAddr(), WithoutFallbackRetryAfter())
		must.NoError(t, err)

		rec, _ := serve(t, mw, request(t, "203.0.113.7:1"))
		test.EqOp(t, "", rec.Header().Get(RetryAfterHeader))
	})

	T.Run("still passes a real estimate on with the fallback suppressed", func(t *testing.T) {
		t.Parallel()

		// Suppression is about guesses, not about hints the limiter measured.
		limiter := hintingLimiter{stubLimiter: alwaysRefuse(), delay: 2 * time.Second, ok: true}

		mw, err := NewMiddleware(limiter, KeyByRemoteAddr(), WithoutFallbackRetryAfter())
		must.NoError(t, err)

		rec, _ := serve(t, mw, request(t, "203.0.113.7:1"))
		test.EqOp(t, "2", rec.Header().Get(RetryAfterHeader))
	})

	T.Run("sends no header on an allowed request", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(alwaysAllow(), KeyByRemoteAddr())
		must.NoError(t, err)

		rec, _ := serve(t, mw, request(t, "203.0.113.7:1"))
		test.EqOp(t, "", rec.Header().Get(RetryAfterHeader))
	})
}

func TestMiddleware_LimiterFailure(T *testing.T) {
	T.Parallel()

	boom := platformerrors.New("redis is having a moment")

	failing := func() *stubLimiter {
		return &stubLimiter{allow: func(string) (bool, error) { return false, boom }}
	}

	T.Run("lets the request through by default", func(t *testing.T) {
		t.Parallel()

		// A guard that cannot answer is a fault, not a verdict. Failing closed
		// would turn one dependency's bad minute into a total outage.
		mw, err := NewMiddleware(failing(), KeyByRemoteAddr())
		must.NoError(t, err)

		rec, reached := serve(t, mw, request(t, "203.0.113.7:1"))
		test.True(t, reached)
		test.EqOp(t, http.StatusNoContent, rec.Code)
	})

	T.Run("refuses the request when told to fail closed", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(failing(), KeyByRemoteAddr(), WithFailClosed())
		must.NoError(t, err)

		rec, reached := serve(t, mw, request(t, "203.0.113.7:1"))
		test.False(t, reached)
		test.EqOp(t, http.StatusTooManyRequests, rec.Code)
	})

	T.Run("treats a key extractor failure the same way", func(t *testing.T) {
		t.Parallel()

		limiter := alwaysAllow()
		keyFn := func(*http.Request) (string, error) { return "", boom }

		mw, err := NewMiddleware(limiter, keyFn)
		must.NoError(t, err)

		_, reached := serve(t, mw, request(t, "203.0.113.7:1"))
		test.True(t, reached)
		test.SliceEmpty(t, limiter.keys)

		closed, err := NewMiddleware(alwaysAllow(), keyFn, WithFailClosed())
		must.NoError(t, err)

		rec, reached := serve(t, closed, request(t, "203.0.113.7:1"))
		test.False(t, reached)
		test.EqOp(t, http.StatusTooManyRequests, rec.Code)
	})

	T.Run("keeps the failure's context out of the response", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(failing(), KeyByRemoteAddr(), WithFailClosed())
		must.NoError(t, err)

		rec, _ := serve(t, mw, request(t, "203.0.113.7:1"))
		test.StrNotContains(t, rec.Body.String(), "redis")
	})
}

// errorReader is a request body that fails if anything tries to read it.
type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, platformerrors.New("the middleware read a body it should not have")
}

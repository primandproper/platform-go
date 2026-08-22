package httpclient

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v13/ratelimiting"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// stubLimiter answers Allow from a function, recording the keys it was asked
// about so a test can assert the bucket is keyed by host.
type stubLimiter struct {
	allow func(key string) (bool, error)
	keys  []string
}

var _ ratelimiting.RateLimiter = (*stubLimiter)(nil)

func (l *stubLimiter) Allow(_ context.Context, key string) (bool, error) {
	l.keys = append(l.keys, key)

	return l.allow(key)
}

func (l *stubLimiter) Close() error { return nil }

func TestRateLimitTransport_RoundTrip(T *testing.T) {
	T.Parallel()

	T.Run("sends the request when the bucket has a token", func(t *testing.T) {
		t.Parallel()

		limiter := &stubLimiter{allow: func(string) (bool, error) { return true, nil }}

		var calls int
		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++

				return response(http.StatusOK, "fine"), nil
			})),
			WithRateLimit(limiter),
		)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, 1, calls)
		test.Eq(t, []string{"example.com"}, limiter.keys)
	})

	T.Run("refuses the request when the bucket is empty", func(t *testing.T) {
		t.Parallel()

		limiter := &stubLimiter{allow: func(string) (bool, error) { return false, nil }}

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Error("the request should never have reached the wire")

				return nil, nil
			})),
			WithRateLimit(limiter),
		)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		test.Nil(t, resp)
		must.Error(t, err)
		test.ErrorIs(t, err, ratelimiting.ErrRateLimited)
	})

	T.Run("propagates a limiter failure", func(t *testing.T) {
		t.Parallel()

		expected := errors.New("redis is unreachable")
		limiter := &stubLimiter{allow: func(string) (bool, error) { return false, expected }}

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Error("the request should never have reached the wire")

				return nil, nil
			})),
			WithRateLimit(limiter),
		)

		_, err := get(t.Context(), client, "http://example.com/thing")
		must.Error(t, err)
		test.ErrorIs(t, err, expected)

		// A limiter that cannot answer is not a limiter that says yes.
		test.False(t, errors.Is(err, ratelimiting.ErrRateLimited))
	})

	T.Run("keys the bucket by host and port", func(t *testing.T) {
		t.Parallel()

		limiter := &stubLimiter{allow: func(string) (bool, error) { return true, nil }}

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, "fine"), nil
			})),
			WithRateLimit(limiter),
		)

		for _, url := range []string{"http://a.example.com/x", "http://b.example.com:8443/y"} {
			resp, err := get(t.Context(), client, url)
			must.NoError(t, err)
			_ = resp.Body.Close()
		}

		test.Eq(t, []string{"a.example.com", "b.example.com:8443"}, limiter.keys)
	})

	T.Run("a nil limiter is ignored", func(t *testing.T) {
		t.Parallel()

		client := newClient(t, WithTransport(stubRoundTripper{}), WithRateLimit(nil))

		_, ok := client.Transport.(stubRoundTripper)
		test.True(t, ok)
	})
}

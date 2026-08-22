package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/retry"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newRetryClient builds a client whose only layer is the retrying transport, so
// a test asserts on retry behavior and nothing else.
func newRetryClient(t *testing.T, base http.RoundTripper, attempts int, opts ...RetryOption) *http.Client {
	t.Helper()

	return newClient(t,
		WithTransport(base),
		WithRetryPolicy(&immediatePolicy{attempts: attempts}, opts...),
	)
}

func TestRetryTransport_RoundTrip(T *testing.T) {
	T.Parallel()

	T.Run("returns a success without retrying", func(t *testing.T) {
		t.Parallel()

		var calls int
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return response(http.StatusOK, "fine"), nil
		}), 3)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, http.StatusOK, resp.StatusCode)
		test.EqOp(t, 1, calls)
	})

	T.Run("retries a 5xx and returns the eventual success", func(t *testing.T) {
		t.Parallel()

		var calls int
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if calls < 3 {
				return response(http.StatusBadGateway, "nope"), nil
			}

			return response(http.StatusOK, "fine"), nil
		}), 5)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, http.StatusOK, resp.StatusCode)
		test.EqOp(t, 3, calls)
	})

	T.Run("returns the last response when the attempts run out", func(t *testing.T) {
		t.Parallel()

		var calls int
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return response(http.StatusServiceUnavailable, "still down"), nil
		}), 3)

		// Exhausting the retries is not an error the caller has to learn about:
		// the server's own answer comes back, readable as it always was.
		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, http.StatusServiceUnavailable, resp.StatusCode)
		test.EqOp(t, 3, calls)

		body, err := io.ReadAll(resp.Body)
		must.NoError(t, err)
		test.EqOp(t, "still down", string(body))
	})

	T.Run("does not retry a 404", func(t *testing.T) {
		t.Parallel()

		var calls int
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return response(http.StatusNotFound, "gone"), nil
		}), 5)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, http.StatusNotFound, resp.StatusCode)
		test.EqOp(t, 1, calls)
	})

	T.Run("retries the timing 4xxs", func(t *testing.T) {
		t.Parallel()

		for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests} {
			var calls int
			client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++

				return response(status, "slow down"), nil
			}), 3)

			resp, err := get(t.Context(), client, "http://example.com/thing")
			must.NoError(t, err)
			_ = resp.Body.Close()

			test.EqOp(t, 3, calls)
		}
	})

	T.Run("retries a transport error", func(t *testing.T) {
		t.Parallel()

		expected := errors.New("connection reset")

		var calls int
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return nil, expected
		}), 3)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		test.Nil(t, resp)
		must.Error(t, err)
		test.ErrorIs(t, err, expected)
		test.EqOp(t, 3, calls)
	})

	T.Run("does not retry a POST by default", func(t *testing.T) {
		t.Parallel()

		var calls int
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return response(http.StatusServiceUnavailable, "down"), nil
		}), 3)

		resp, err := post(t.Context(), client, "http://example.com/thing", strings.NewReader("payload"))
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, 1, calls)
	})

	T.Run("retries a POST once it is opted in", func(t *testing.T) {
		t.Parallel()

		var calls int
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return response(http.StatusServiceUnavailable, "down"), nil
		}), 3, WithRetryMethods(http.MethodPost))

		resp, err := post(t.Context(), client, "http://example.com/thing", strings.NewReader("payload"))
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, 3, calls)
	})

	T.Run("replays the body on every attempt", func(t *testing.T) {
		t.Parallel()

		var seen []string
		client := newRetryClient(t, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			must.NoError(t, err)
			_ = req.Body.Close()
			seen = append(seen, string(body))

			return response(http.StatusServiceUnavailable, "down"), nil
		}), 3)

		// http.NewRequest fills in GetBody for a *strings.Reader, which is what
		// makes the second and third attempts possible at all.
		req := newRequest(t.Context(), http.MethodPut, "http://example.com/thing", strings.NewReader("payload"))

		resp, err := client.Do(req)
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.Eq(t, []string{"payload", "payload", "payload"}, seen)
	})

	T.Run("does not retry a body it cannot replay", func(t *testing.T) {
		t.Parallel()

		var calls int
		client := newRetryClient(t, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			_, _ = io.Copy(io.Discard, req.Body)
			_ = req.Body.Close()

			return response(http.StatusServiceUnavailable, "down"), nil
		}), 3)

		// A bare io.Reader leaves GetBody nil, so there is no second copy of the
		// body to send and the request gets exactly one attempt.
		req := newRequest(t.Context(), http.MethodPut, "http://example.com/thing", io.LimitReader(strings.NewReader("payload"), 7))
		test.Nil(t, req.GetBody)

		resp, err := client.Do(req)
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, 1, calls)
	})

	T.Run("closes the bodies of superseded responses", func(t *testing.T) {
		t.Parallel()

		var bodies []*trackedBody
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			body := &trackedBody{Reader: strings.NewReader("down")}
			bodies = append(bodies, body)

			resp := response(http.StatusServiceUnavailable, "")
			resp.Body = body

			return resp, nil
		}), 3)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		must.SliceLen(t, 3, bodies)

		// The first two were replaced by a later attempt and had to give their
		// connections back; the last one is what the caller is holding.
		test.True(t, bodies[0].closed)
		test.True(t, bodies[1].closed)
		test.False(t, bodies[2].closed)
	})

	T.Run("waits out a Retry-After within the cap", func(t *testing.T) {
		t.Parallel()

		var calls int
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return withHeader(response(http.StatusTooManyRequests, "slow down"), "Retry-After", "1"), nil
		}), 2)

		started := time.Now()

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, 2, calls)
		test.True(t, time.Since(started) >= 900*time.Millisecond)
	})

	T.Run("gives up on a Retry-After beyond the cap", func(t *testing.T) {
		t.Parallel()

		var calls int
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return withHeader(response(http.StatusTooManyRequests, "come back tomorrow"), "Retry-After", "3600"), nil
		}), 5, WithMaxRetryAfter(time.Second))

		// Retrying sooner than asked is exactly what the header forbids, so the
		// only honest options are to wait an hour or to stop. It stops.
		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, http.StatusTooManyRequests, resp.StatusCode)
		test.EqOp(t, 1, calls)
	})

	T.Run("stops when the request context is done", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		var calls int
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++
			cancel()

			return response(http.StatusServiceUnavailable, "down"), nil
		}), 5)

		resp, err := client.Do(newRequest(ctx, http.MethodGet, "http://example.com/thing", nil))
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, 1, calls)
	})

	T.Run("reports a policy that never ran the operation", func(t *testing.T) {
		t.Parallel()

		// A zero-attempt policy would otherwise produce (nil, nil), which no
		// http.Client is prepared to be handed.
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			t.Error("the base transport should never have been reached")

			return nil, nil
		}), 0)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		test.Nil(t, resp)
		must.Error(t, err)
	})
}

func TestRetryTransport_retryable(T *testing.T) {
	T.Parallel()

	T.Run("a nil body is always replayable", func(t *testing.T) {
		t.Parallel()

		transport := retryTransportForTest(t, &immediatePolicy{attempts: 1})
		test.True(t, transport.retryable(newRequest(t.Context(), http.MethodGet, "http://example.com", nil)))
	})

	T.Run("http.NoBody is always replayable", func(t *testing.T) {
		t.Parallel()

		transport := retryTransportForTest(t, &immediatePolicy{attempts: 1})

		req := newRequest(t.Context(), http.MethodDelete, "http://example.com", http.NoBody)
		req.GetBody = nil

		test.True(t, transport.retryable(req))
	})
}

func TestRetryOptions(T *testing.T) {
	T.Parallel()

	T.Run("empty and nil options leave the defaults in place", func(t *testing.T) {
		t.Parallel()

		transport := retryTransportForTest(t, &immediatePolicy{attempts: 1},
			nil,
			WithRetryMethods(),
			WithMaxRetryAfter(0),
			WithMaxRetryAfter(-time.Second),
		)

		test.Eq(t, defaultRetryMethods, transport.methods)
		test.EqOp(t, DefaultMaxRetryAfter, transport.maxRetryAfter)
	})

	T.Run("a nil policy is ignored", func(t *testing.T) {
		t.Parallel()

		client := newClient(t, WithTransport(stubRoundTripper{}), WithRetryPolicy(nil))

		_, ok := client.Transport.(stubRoundTripper)
		test.True(t, ok)
	})
}

func TestRetryAfterDelay(T *testing.T) {
	T.Parallel()

	T.Run("reads a count of seconds", func(t *testing.T) {
		t.Parallel()

		delay, ok := retryAfterDelay(withHeader(response(http.StatusTooManyRequests, ""), "Retry-After", " 12 "))
		test.True(t, ok)
		test.EqOp(t, 12*time.Second, delay)
	})

	T.Run("reads an HTTP date", func(t *testing.T) {
		t.Parallel()

		when := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)

		delay, ok := retryAfterDelay(withHeader(response(http.StatusServiceUnavailable, ""), "Retry-After", when))
		test.True(t, ok)
		test.True(t, delay > 59*time.Minute)
		test.True(t, delay <= time.Hour)
	})

	T.Run("a date already past means now", func(t *testing.T) {
		t.Parallel()

		when := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)

		delay, ok := retryAfterDelay(withHeader(response(http.StatusServiceUnavailable, ""), "Retry-After", when))
		test.True(t, ok)
		test.EqOp(t, time.Duration(0), delay)
	})

	T.Run("a negative count means now", func(t *testing.T) {
		t.Parallel()

		delay, ok := retryAfterDelay(withHeader(response(http.StatusServiceUnavailable, ""), "Retry-After", "-5"))
		test.True(t, ok)
		test.EqOp(t, time.Duration(0), delay)
	})

	T.Run("an absent header asks for nothing", func(t *testing.T) {
		t.Parallel()

		_, ok := retryAfterDelay(response(http.StatusServiceUnavailable, ""))
		test.False(t, ok)
	})

	T.Run("an unparseable header asks for nothing", func(t *testing.T) {
		t.Parallel()

		_, ok := retryAfterDelay(withHeader(response(http.StatusServiceUnavailable, ""), "Retry-After", "soon"))
		test.False(t, ok)
	})
}

func TestDrainAndClose(T *testing.T) {
	T.Parallel()

	T.Run("tolerates a nil response and a nil body", func(t *testing.T) {
		t.Parallel()

		drainAndClose(nil)

		resp := response(http.StatusOK, "")
		resp.Body = nil
		drainAndClose(resp)
	})

	T.Run("reads no more than the drain cap", func(t *testing.T) {
		t.Parallel()

		// The bytes are being thrown away, so a huge error page must not be
		// read in full just to reclaim one pooled connection.
		body := &trackedBody{Reader: strings.NewReader(strings.Repeat("x", maxDrainBytes*4))}

		resp := response(http.StatusServiceUnavailable, "")
		resp.Body = body

		drainAndClose(resp)
		test.True(t, body.closed)

		remaining, err := io.ReadAll(body.Reader)
		must.NoError(t, err)
		test.EqOp(t, maxDrainBytes*3, len(remaining))
	})
}

// unretryableIsRespected pins the vocabulary this transport speaks to the retry
// package: a terminal status has to come back wrapped in ErrUnretryable, not as
// some private notion of "do not try again".
func TestRetryTransport_classify(T *testing.T) {
	T.Parallel()

	T.Run("a plain 4xx is unretryable", func(t *testing.T) {
		t.Parallel()

		transport := retryTransportForTest(t, &immediatePolicy{attempts: 1})

		err := transport.classify(t.Context(), classifyRequest(t.Context()), response(http.StatusBadRequest, ""))
		must.Error(t, err)
		test.ErrorIs(t, err, retry.ErrUnretryable)
	})

	T.Run("a 5xx is retryable", func(t *testing.T) {
		t.Parallel()

		transport := retryTransportForTest(t, &immediatePolicy{attempts: 1})

		err := transport.classify(t.Context(), classifyRequest(t.Context()), response(http.StatusInternalServerError, ""))
		must.Error(t, err)
		test.False(t, errors.Is(err, retry.ErrUnretryable))
	})

	T.Run("a 2xx and a 3xx are successes", func(t *testing.T) {
		t.Parallel()

		transport := retryTransportForTest(t, &immediatePolicy{attempts: 1})

		test.NoError(t, transport.classify(t.Context(), classifyRequest(t.Context()), response(http.StatusOK, "")))
		test.NoError(t, transport.classify(t.Context(), classifyRequest(t.Context()), response(http.StatusFound, "")))
	})

	T.Run("a done context cuts the Retry-After wait short", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		transport := retryTransportForTest(t, &immediatePolicy{attempts: 1})

		err := transport.classify(ctx, classifyRequest(ctx), withHeader(response(http.StatusTooManyRequests, ""), "Retry-After", "10"))
		must.Error(t, err)
		test.ErrorIs(t, err, context.Canceled)
	})
}

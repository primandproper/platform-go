package httpclient

import (
	"net/http"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	"github.com/primandproper/platform-go/v13/ratelimiting"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestResilienceNesting(T *testing.T) {
	T.Parallel()

	T.Run("wraps observability over cache over breaker over retry over rate limit over the base", func(t *testing.T) {
		t.Parallel()

		base := stubRoundTripper{}

		client := newClient(t,
			WithTransport(base),
			// Deliberately named in an order that does not match the nesting:
			// the arrangement is a property of the middlewares, not of the
			// caller's typing.
			WithRateLimit(&stubLimiter{allow: func(string) (bool, error) { return true, nil }}),
			WithCircuitBreaker(closedBreaker()),
			WithHTTPCache(cacheForTest(t)),
			WithRetryPolicy(&immediatePolicy{attempts: 2}),
		)

		// Outermost, so the span it opens covers the breaker's rejections as
		// well as the attempts underneath them.
		observed, ok := client.Transport.(*observedTransport)
		must.True(t, ok)

		// Above the breaker, because a hit is not a request: it reaches no
		// wire, so it must not report an outcome to a circuit or spend a token.
		responses, ok := observed.base.(*cacheTransport)
		must.True(t, ok)

		breaker, ok := responses.base.(*breakerTransport)
		must.True(t, ok)

		retrier, ok := breaker.base.(*retryTransport)
		must.True(t, ok)

		limiter, ok := retrier.base.(*rateLimitTransport)
		must.True(t, ok)

		_, ok = limiter.base.(stubRoundTripper)
		test.True(t, ok)
	})

	T.Run("tracing sits below the resilience layers", func(t *testing.T) {
		t.Parallel()

		client := newClient(t,
			WithTransport(stubRoundTripper{}),
			WithTracing(true),
			WithRetryPolicy(&immediatePolicy{attempts: 2}),
		)

		observed, ok := client.Transport.(*observedTransport)
		must.True(t, ok)

		retrier, ok := observed.base.(*retryTransport)
		must.True(t, ok)

		// Each attempt gets its own client span rather than one span stretched
		// over the whole loop.
		_, ok = retrier.base.(stubRoundTripper)
		test.False(t, ok)
	})

	T.Run("a client with no middleware is not wrapped", func(t *testing.T) {
		t.Parallel()

		// Nothing to describe that otelhttp does not already describe, so the
		// logical-request span would only duplicate the per-attempt one.
		client := newClient(t, WithTransport(stubRoundTripper{}))

		_, ok := client.Transport.(*observedTransport)
		test.False(t, ok)
	})

	T.Run("a cache alone is enough to be wrapped", func(t *testing.T) {
		t.Parallel()

		// A hit reaches neither the tracing transport nor the wire, so without
		// this it would produce no span at all — the same hole an open circuit
		// leaves, and the same reason for closing it.
		client := newClient(t, WithTransport(stubRoundTripper{}), WithHTTPCache(cacheForTest(t)))

		_, ok := client.Transport.(*observedTransport)
		test.True(t, ok)
	})
}

func TestResilienceComposition(T *testing.T) {
	T.Parallel()

	T.Run("every retried attempt spends a token", func(t *testing.T) {
		t.Parallel()

		limiter := &stubLimiter{allow: func(string) (bool, error) { return true, nil }}

		var calls int
		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++

				return response(http.StatusServiceUnavailable, "down"), nil
			})),
			WithRateLimit(limiter),
			WithRetryPolicy(&immediatePolicy{attempts: 3}),
		)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		// The whole point of putting the limiter below the retry loop: a provider
		// counting requests against a documented budget counts all three of these.
		test.EqOp(t, 3, calls)
		test.SliceLen(t, 3, limiter.keys)
	})

	T.Run("an open circuit is not retried", func(t *testing.T) {
		t.Parallel()

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Error("the request should never have reached the wire")

				return nil, nil
			})),
			WithCircuitBreaker(openBreaker()),
			WithRetryPolicy(&immediatePolicy{attempts: 3}),
		)

		// Failing fast three times with backoff in between is not failing fast,
		// which is why the breaker is outermost.
		resp, err := get(t.Context(), client, "http://example.com/thing")
		test.Nil(t, resp)
		must.Error(t, err)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
	})

	T.Run("a rate-limited attempt is retried rather than failing the request", func(t *testing.T) {
		t.Parallel()

		var asked int
		limiter := &stubLimiter{allow: func(string) (bool, error) {
			asked++

			// The bucket refills between attempts, which is what the retry
			// policy's backoff is really waiting for.
			return asked > 2, nil
		}}

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, "fine"), nil
			})),
			WithRateLimit(limiter),
			WithRetryPolicy(&immediatePolicy{attempts: 5}),
		)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, http.StatusOK, resp.StatusCode)
		test.EqOp(t, 3, asked)
	})

	T.Run("the breaker judges the outcome after retrying", func(t *testing.T) {
		t.Parallel()

		breaker := closedBreaker()

		var calls int
		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if calls < 3 {
					return response(http.StatusBadGateway, "down"), nil
				}

				return response(http.StatusOK, "fine"), nil
			})),
			WithCircuitBreaker(breaker),
			WithRetryPolicy(&immediatePolicy{attempts: 5}),
		)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		// Two transient 502s that a retry absorbed say nothing about the host's
		// health, so the breaker sees one success rather than two failures.
		test.EqOp(t, 3, calls)
		test.SliceLen(t, 1, breaker.SucceededCalls())
		test.SliceLen(t, 0, breaker.FailedCalls())
	})

	T.Run("a locally refused request is not held against the host", func(t *testing.T) {
		t.Parallel()

		breaker := closedBreaker()
		limiter := &stubLimiter{allow: func(string) (bool, error) { return false, nil }}

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Error("the request should never have reached the wire")

				return nil, nil
			})),
			WithCircuitBreaker(breaker),
			WithRateLimit(limiter),
		)

		_, err := get(t.Context(), client, "http://example.com/thing")
		must.Error(t, err)
		test.ErrorIs(t, err, ratelimiting.ErrRateLimited)

		// The breaker sees the error, but the wire was never touched: this says
		// something about the local budget and nothing about the host. Counting
		// it would let ordinary throttling trip — and keep tripped — a circuit
		// against a dependency that is perfectly well.
		test.SliceLen(t, 0, breaker.FailedCalls())
		test.SliceLen(t, 0, breaker.SucceededCalls())
	})

	T.Run("a cache hit spends no token and reports to no circuit", func(t *testing.T) {
		t.Parallel()

		breaker := closedBreaker()
		limiter := &stubLimiter{allow: func(string) (bool, error) { return true, nil }}

		var calls int
		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++

				return withHeader(response(http.StatusOK, "jwks"), "Cache-Control", "max-age=300"), nil
			})),
			WithHTTPCache(cacheForTest(t)),
			WithCircuitBreaker(breaker),
			WithRateLimit(limiter),
		)

		for range 3 {
			resp, err := get(t.Context(), client, cacheURL)
			must.NoError(t, err)
			test.EqOp(t, "jwks", readBody(t, resp))
		}

		// One request on the wire, and exactly one of everything that describes
		// a request on the wire. A provider's documented budget counts requests
		// the origin saw, and a circuit judges a host on answers it gave — two
		// hits produce neither, and a cache placed under either layer would
		// have manufactured both.
		test.EqOp(t, 1, calls)
		test.SliceLen(t, 1, limiter.keys)
		test.SliceLen(t, 1, breaker.SucceededCalls())
	})

	T.Run("a revalidation passes through every layer", func(t *testing.T) {
		t.Parallel()

		breaker := closedBreaker()
		limiter := &stubLimiter{allow: func(string) (bool, error) { return true, nil }}
		clk := &steppingClock{now: time.Unix(1_700_000_000, 0)}

		var calls int
		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++

				if calls == 1 {
					resp := withHeader(response(http.StatusOK, "jwks"), "Cache-Control", "max-age=60")

					return withHeader(resp, "ETag", `"v1"`), nil
				}

				return response(http.StatusNotModified, ""), nil
			})),
			WithHTTPCache(cacheForTest(t), WithCacheClock(clk)),
			WithCircuitBreaker(breaker),
			WithRateLimit(limiter),
		)

		resp, err := get(t.Context(), client, cacheURL)
		must.NoError(t, err)
		test.EqOp(t, "jwks", readBody(t, resp))

		clk.advance(61 * time.Second)

		revalidated, err := get(t.Context(), client, cacheURL)
		must.NoError(t, err)
		test.EqOp(t, "jwks", readBody(t, revalidated))

		// The body came from the cache, but the question went to the origin —
		// so it is a request like any other, and pays for itself like one.
		test.EqOp(t, 2, calls)
		test.SliceLen(t, 2, limiter.keys)
		test.SliceLen(t, 2, breaker.SucceededCalls())
	})

	T.Run("an open circuit does not reject what the cache can answer", func(t *testing.T) {
		t.Parallel()

		store := cacheForTest(t)

		warm := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return withHeader(response(http.StatusOK, "jwks"), "Cache-Control", "max-age=300"), nil
			})),
			WithHTTPCache(store),
		)

		resp, err := get(t.Context(), warm, cacheURL)
		must.NoError(t, err)
		test.EqOp(t, "jwks", readBody(t, resp))

		// Same store, and a dependency that has since fallen over hard enough
		// to trip its circuit.
		broken := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Error("the request should never have reached the wire")

				return nil, nil
			})),
			WithHTTPCache(store),
			WithCircuitBreaker(openBreaker()),
		)

		// The cache is above the breaker, so a fresh entry is still served.
		// This is the payoff of that placement rather than an accident of it: a
		// stable third-party document does not stop being readable because the
		// origin serving it is down.
		served, err := get(t.Context(), broken, cacheURL)
		must.NoError(t, err)
		test.EqOp(t, "jwks", readBody(t, served))
	})
}

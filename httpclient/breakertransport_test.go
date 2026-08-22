package httpclient

import (
	"errors"
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	circuitbreakingmock "github.com/primandproper/platform-go/v13/circuitbreaking/mock"
	"github.com/primandproper/platform-go/v13/circuitbreaking/partitioned"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// closedBreaker is a breaker that lets everything through and records what it
// was told about the outcome.
func closedBreaker() *circuitbreakingmock.CircuitBreakerMock {
	return &circuitbreakingmock.CircuitBreakerMock{
		CanProceedFunc:    func() bool { return true },
		CannotProceedFunc: func() bool { return false },
		FailedFunc:        func() {},
		SucceededFunc:     func() {},
	}
}

// openBreaker is a breaker that has tripped.
func openBreaker() *circuitbreakingmock.CircuitBreakerMock {
	return &circuitbreakingmock.CircuitBreakerMock{
		CanProceedFunc:    func() bool { return false },
		CannotProceedFunc: func() bool { return true },
		FailedFunc:        func() {},
		SucceededFunc:     func() {},
	}
}

func TestBreakerTransport_RoundTrip(T *testing.T) {
	T.Parallel()

	T.Run("fails fast when the circuit is open", func(t *testing.T) {
		t.Parallel()

		breaker := openBreaker()
		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Error("the request should never have reached the wire")

				return nil, nil
			})),
			WithCircuitBreaker(breaker),
		)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		test.Nil(t, resp)
		must.Error(t, err)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
	})

	T.Run("records a success on a 2xx", func(t *testing.T) {
		t.Parallel()

		breaker := closedBreaker()
		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, "fine"), nil
			})),
			WithCircuitBreaker(breaker),
		)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.SliceLen(t, 1, breaker.SucceededCalls())
		test.SliceLen(t, 0, breaker.FailedCalls())
	})

	T.Run("records a failure on a 5xx", func(t *testing.T) {
		t.Parallel()

		breaker := closedBreaker()
		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusInternalServerError, "boom"), nil
			})),
			WithCircuitBreaker(breaker),
		)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.SliceLen(t, 1, breaker.FailedCalls())
		test.SliceLen(t, 0, breaker.SucceededCalls())
	})

	T.Run("records a failure on a transport error", func(t *testing.T) {
		t.Parallel()

		breaker := closedBreaker()
		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("dial tcp: no route to host")
			})),
			WithCircuitBreaker(breaker),
		)

		_, err := get(t.Context(), client, "http://example.com/thing")
		must.Error(t, err)

		test.SliceLen(t, 1, breaker.FailedCalls())
	})

	T.Run("a 4xx is the caller's problem, not the host's", func(t *testing.T) {
		t.Parallel()

		breaker := closedBreaker()
		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusNotFound, "gone"), nil
			})),
			WithCircuitBreaker(breaker),
		)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		// Tripping every caller's circuit because one of them asked for a
		// missing document would be a self-inflicted outage.
		test.SliceLen(t, 0, breaker.FailedCalls())
		test.SliceLen(t, 1, breaker.SucceededCalls())
	})

	T.Run("breaks per host", func(t *testing.T) {
		t.Parallel()

		broken, healthy := openBreaker(), closedBreaker()
		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, "fine"), nil
			})),
			WithKeyedCircuitBreaker(partitioned.NewKeyedCircuitBreaker(healthy, map[string]circuitbreaking.CircuitBreaker{
				"broken.example.com": broken,
			})),
		)

		_, err := get(t.Context(), client, "http://broken.example.com/thing")
		must.Error(t, err)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)

		// The dead dependency is isolated: everything else still goes out.
		resp, err := get(t.Context(), client, "http://healthy.example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, http.StatusOK, resp.StatusCode)
		test.SliceLen(t, 1, healthy.SucceededCalls())
	})

	T.Run("passes through when no breaker resolves for the host", func(t *testing.T) {
		t.Parallel()

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, "fine"), nil
			})),
			// A keyed breaker with no global fallback answers nil for an
			// unregistered host, which must not take the client down with it.
			WithKeyedCircuitBreaker(partitioned.NewKeyedCircuitBreaker(nil, nil)),
		)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, http.StatusOK, resp.StatusCode)
	})

	T.Run("nil breakers are ignored", func(t *testing.T) {
		t.Parallel()

		client := newClient(t,
			WithTransport(stubRoundTripper{}),
			WithCircuitBreaker(nil),
			WithKeyedCircuitBreaker(nil),
		)

		_, ok := client.Transport.(stubRoundTripper)
		test.True(t, ok)
	})
}

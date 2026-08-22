package httpclient

import (
	"errors"
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v13/circuitbreaking/partitioned"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/ratelimiting"
	"github.com/primandproper/platform-go/v13/retry"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestDefaultOutcome(T *testing.T) {
	T.Parallel()

	T.Run("a transport error counts against the host", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, OutcomeFailure, DefaultOutcome(nil, errors.New("dial tcp: connection refused")))
	})

	T.Run("a 5xx counts against the host", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, OutcomeFailure, DefaultOutcome(response(http.StatusInternalServerError, ""), nil))
		test.EqOp(t, OutcomeFailure, DefaultOutcome(response(http.StatusServiceUnavailable, ""), nil))
	})

	T.Run("a 4xx does not", func(t *testing.T) {
		t.Parallel()

		// The request was wrong, which is this caller's problem and no reason to
		// cut off every other caller of the same host.
		test.EqOp(t, OutcomeSuccess, DefaultOutcome(response(http.StatusBadRequest, ""), nil))
		test.EqOp(t, OutcomeSuccess, DefaultOutcome(response(http.StatusNotFound, ""), nil))
		test.EqOp(t, OutcomeSuccess, DefaultOutcome(response(http.StatusTooManyRequests, ""), nil))
	})

	T.Run("a success is a success", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, OutcomeSuccess, DefaultOutcome(response(http.StatusOK, ""), nil))
	})

	T.Run("a locally rate limited request is ignored outright", func(t *testing.T) {
		t.Parallel()

		// It never reached the host, so it is evidence about the local budget
		// and none at all about the dependency's health. Counting it as a
		// failure would let ordinary throttling trip — and hold open — a circuit
		// against a host that is perfectly well.
		err := platformerrors.Wrapf(ratelimiting.ErrRateLimited, "host %q", "example.com")

		test.EqOp(t, OutcomeIgnored, DefaultOutcome(nil, err))
	})
}

func TestDefaultRetryClassification(T *testing.T) {
	T.Parallel()

	T.Run("anything below 400 is accepted", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, DefaultRetryClassification(response(http.StatusOK, "")))
		test.NoError(t, DefaultRetryClassification(response(http.StatusFound, "")))
	})

	T.Run("a 5xx is worth another attempt", func(t *testing.T) {
		t.Parallel()

		err := DefaultRetryClassification(response(http.StatusBadGateway, ""))
		must.Error(t, err)
		test.False(t, errors.Is(err, retry.ErrUnretryable))
	})

	T.Run("the timing 4xxs are worth another attempt", func(t *testing.T) {
		t.Parallel()

		for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests} {
			err := DefaultRetryClassification(response(status, ""))
			must.Error(t, err)
			test.False(t, errors.Is(err, retry.ErrUnretryable))
		}
	})

	T.Run("every other 4xx ends the loop", func(t *testing.T) {
		t.Parallel()

		for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict} {
			err := DefaultRetryClassification(response(status, ""))
			must.Error(t, err)
			test.ErrorIs(t, err, retry.ErrUnretryable)
		}
	})
}

func TestOutcome_String(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "success", OutcomeSuccess.String())
		test.EqOp(t, "failure", OutcomeFailure.String())
		test.EqOp(t, "ignored", OutcomeIgnored.String())
		test.EqOp(t, "unknown", Outcome(99).String())
	})
}

func TestWithOutcomeClassifier(T *testing.T) {
	T.Parallel()

	T.Run("reclassifies what the breaker is told", func(t *testing.T) {
		t.Parallel()

		var failures, successes int

		breaker := closedBreaker()
		breaker.FailedFunc = func() { failures++ }
		breaker.SucceededFunc = func() { successes++ }

		// This host answers 400 when it is overloaded, which the default rule
		// would read as a well-formed refusal of a bad request.
		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusBadRequest, ""), nil
			})),
			WithCircuitBreaker(breaker, WithOutcomeClassifier(func(resp *http.Response, err error) Outcome {
				if resp != nil && resp.StatusCode == http.StatusBadRequest {
					return OutcomeFailure
				}

				return DefaultOutcome(resp, err)
			})),
		)

		resp, err := get(t.Context(), client, "http://example.com")
		must.NoError(t, err)
		must.NoError(t, resp.Body.Close())

		test.EqOp(t, 1, failures)
		test.EqOp(t, 0, successes)
	})

	T.Run("an ignored outcome is recorded neither way", func(t *testing.T) {
		t.Parallel()

		var failures, successes int

		breaker := closedBreaker()
		breaker.FailedFunc = func() { failures++ }
		breaker.SucceededFunc = func() { successes++ }

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusServiceUnavailable, ""), nil
			})),
			WithCircuitBreaker(breaker, WithOutcomeClassifier(func(*http.Response, error) Outcome {
				return OutcomeIgnored
			})),
		)

		resp, err := get(t.Context(), client, "http://example.com")
		must.NoError(t, err)
		must.NoError(t, resp.Body.Close())

		test.EqOp(t, 0, failures)
		test.EqOp(t, 0, successes)
	})

	T.Run("a nil classifier leaves the default in place", func(t *testing.T) {
		t.Parallel()

		transport := newBreakerTransport(partitioned.NewKeyedCircuitBreaker(closedBreaker(), nil), []BreakerOption{
			nil,
			WithOutcomeClassifier(nil),
		})

		test.EqOp(t, OutcomeFailure, transport.classifier(response(http.StatusInternalServerError, ""), nil))
	})
}

func TestWithRetryClassifier(T *testing.T) {
	T.Parallel()

	T.Run("retries a status the default would not", func(t *testing.T) {
		t.Parallel()

		var calls int

		// A 409 that clears on its own — a lock the server is still releasing.
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if calls < 3 {
				return response(http.StatusConflict, ""), nil
			}

			return response(http.StatusOK, ""), nil
		}), 3, WithRetryClassifier(func(resp *http.Response) error {
			if resp.StatusCode == http.StatusConflict {
				return platformerrors.New("lock still held")
			}

			return DefaultRetryClassification(resp)
		}))

		resp, err := get(t.Context(), client, "http://example.com")
		must.NoError(t, err)
		must.NoError(t, resp.Body.Close())

		test.EqOp(t, 3, calls)
		test.EqOp(t, http.StatusOK, resp.StatusCode)
	})

	T.Run("stops on a status the default would retry", func(t *testing.T) {
		t.Parallel()

		var calls int

		// This host answers 503 for a tenant that is out of quota, which will
		// not resolve itself inside a retry loop.
		client := newRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return response(http.StatusServiceUnavailable, ""), nil
		}), 4, WithRetryClassifier(func(resp *http.Response) error {
			if resp.StatusCode == http.StatusServiceUnavailable {
				return retry.Unretryable(platformerrors.New("out of quota"))
			}

			return DefaultRetryClassification(resp)
		}))

		resp, err := get(t.Context(), client, "http://example.com")
		must.NoError(t, err)
		must.NoError(t, resp.Body.Close())

		test.EqOp(t, 1, calls)
	})

	T.Run("a nil classifier leaves the default in place", func(t *testing.T) {
		t.Parallel()

		transport := retryTransportForTest(t, &immediatePolicy{attempts: 1}, WithRetryClassifier(nil))

		test.ErrorIs(t, transport.classifier(response(http.StatusBadRequest, "")), retry.ErrUnretryable)
	})
}

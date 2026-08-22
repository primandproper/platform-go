package httpclient

import (
	"errors"
	"net/http"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/ratelimiting"
	"github.com/primandproper/platform-go/v13/retry"
)

// Outcome is what one completed exchange taught us about the host it was sent
// to. It is a third state wider than a bool on purpose: a request can fail
// without the host having done anything wrong, and recording that as either a
// failure or a success is a lie in both directions.
type Outcome int

const (
	// OutcomeSuccess reports that the host answered, and answered acceptably.
	// The breaker counts it toward closing a half-open circuit.
	OutcomeSuccess Outcome = iota

	// OutcomeFailure reports that the host is unwell, and that continuing to
	// ask is the thing worth stopping.
	OutcomeFailure

	// OutcomeIgnored reports that this exchange says nothing about the host
	// either way, so the breaker records neither result. It is what a request
	// that never reached the wire deserves.
	OutcomeIgnored
)

// OutcomeClassifier decides what a finished request says about the health of
// the host it was addressed to. It is consulted once per request, after any
// retrying, and its answer is the only thing the circuit breaker is told.
//
// Unlike RetryClassifier it receives the transport error as well as the
// response, because a dial failure or a reset is exactly the kind of evidence a
// breaker exists to accumulate — there, the absence of a response is the
// finding. When err is non-nil, resp is nil.
//
// Override it with WithOutcomeClassifier when a host's idea of a status code
// differs from the standard's — which is most hosts. Delegating to
// DefaultOutcome for everything a classifier does not have an opinion about
// keeps the rest of the behavior described here intact.
type OutcomeClassifier func(resp *http.Response, err error) Outcome

// DefaultOutcome is the classifier installed when none is named.
//
// A transport error counts against the host, and so does a 5xx: both say the
// dependency is unwell, which is the thing a breaker is for. A 4xx does not —
// it says this particular request was wrong, which is the caller's problem and
// no reason to cut off every other caller of the same host.
//
// A request this client's own limiter refused is ignored outright. It never
// reached the host, so it is evidence about the local budget and none at all
// about the dependency's health; counting it would let ordinary throttling trip
// a circuit against a host that is perfectly well — and then keep it tripped,
// since the refusals continue whether or not the host recovers.
func DefaultOutcome(resp *http.Response, err error) Outcome {
	if errors.Is(err, ratelimiting.ErrRateLimited) {
		return OutcomeIgnored
	}

	if err != nil {
		return OutcomeFailure
	}

	if resp != nil && resp.StatusCode >= http.StatusInternalServerError {
		return OutcomeFailure
	}

	return OutcomeSuccess
}

// RetryClassifier decides whether a response is worth another attempt, and
// answers in the retry package's own vocabulary:
//
//   - nil accepts the response and ends the loop successfully.
//   - retry.Unretryable(err) ends the loop immediately, without spending the
//     attempts that remain.
//   - any other error asks for another attempt, subject to the policy.
//
// The error a classifier returns is what the retry.Policy sees, so it is worth
// making descriptive; it is not what the caller of the HTTP client gets back,
// since a request that produced a response returns that response either way.
//
// It is asked only about responses. A transport error — a dial failure, a
// reset, a timeout — is always retryable, because it means no answer arrived at
// all and that is the failure retrying exists for.
//
// Override it with WithRetryClassifier. A classifier that wants the standard
// rules for everything but one endpoint's quirk should call
// DefaultRetryClassification for the cases it does not special-case.
type RetryClassifier func(resp *http.Response) error

// DefaultRetryClassification is the classifier WithRetryPolicy installs when
// none is named.
//
// Anything below 400 is accepted. A 5xx is retried. A 4xx is the server saying
// the request itself is wrong, and repeating a wrong request cannot make it
// right — so it ends the loop, with the two exceptions that are about timing
// rather than about the request: 408 and 429.
func DefaultRetryClassification(resp *http.Response) error {
	if resp == nil || resp.StatusCode < http.StatusBadRequest {
		return nil
	}

	err := platformerrors.Newf("server responded with %s", resp.Status)

	if terminalStatus(resp.StatusCode) {
		return retry.Unretryable(err)
	}

	return err
}

// terminalStatus reports whether a status code is the server saying the request
// itself is wrong, so repeating it unchanged cannot make it right — every 4xx
// but the two that are about timing rather than about the request.
//
// It has a name because it has two readers. DefaultRetryClassification asks it
// on behalf of the retry transport, which holds a response; StatusError.Is asks
// it on behalf of a caller's own retry loop, which holds an error. A second
// copy of this list would be a rule that can drift, and the drift would show up
// as an outer loop re-sending a request the inner one had already given up on.
func terminalStatus(code int) bool {
	return code >= http.StatusBadRequest &&
		code < http.StatusInternalServerError &&
		code != http.StatusRequestTimeout &&
		code != http.StatusTooManyRequests
}

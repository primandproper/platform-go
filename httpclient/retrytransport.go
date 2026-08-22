package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/retry"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// DefaultMaxRetryAfter is how long a Retry-After header may park a request
// before this transport stops honoring it and hands the response back instead.
const DefaultMaxRetryAfter = 30 * time.Second

// maxDrainBytes bounds the read of a superseded response body. The bytes are
// being thrown away; reading an unbounded error page to reclaim one pooled
// connection is a bad trade.
const maxDrainBytes = 4 << 10

// defaultRetryMethods are the methods retried by default: the ones RFC 9110
// defines as idempotent, where a repeat the server already saw cannot have a
// second effect. POST and PATCH are absent because a RoundTripper cannot tell a
// request that never arrived from a response that never came back — see
// WithRetryMethods for how to opt them in safely.
var defaultRetryMethods = []string{
	http.MethodGet,
	http.MethodHead,
	http.MethodOptions,
	http.MethodTrace,
	http.MethodPut,
	http.MethodDelete,
}

// retryTransport re-sends idempotent requests through a retry.Policy.
type retryTransport struct {
	base          http.RoundTripper
	policy        retry.Policy
	classifier    RetryClassifier
	obs           *transportObserver
	methods       []string
	maxRetryAfter time.Duration
}

var _ http.RoundTripper = (*retryTransport)(nil)

// RetryOption tunes the transport WithRetryPolicy installs.
type RetryOption func(*retryTransport)

// WithRetryMethods replaces the set of methods eligible for retry, which
// defaults to the idempotent ones.
//
// Adding POST is the common reason to reach for this, and it is safe exactly
// when the request carries an idempotency key — pair it with
// idempotency/http's transport, which sends one key across every attempt, so
// the server can recognize the repeat. Without that, a retried POST is a second
// charge, not a second try. An empty list leaves the default in place.
func WithRetryMethods(methods ...string) RetryOption {
	return func(t *retryTransport) {
		if len(methods) > 0 {
			t.methods = slices.Clone(methods)
		}
	}
}

// WithMaxRetryAfter caps how long a Retry-After header may delay an attempt. A
// response asking for longer is returned to the caller rather than retried. A
// non-positive duration leaves DefaultMaxRetryAfter in place.
func WithMaxRetryAfter(maxRetryAfter time.Duration) RetryOption {
	return func(t *retryTransport) {
		if maxRetryAfter > 0 {
			t.maxRetryAfter = maxRetryAfter
		}
	}
}

// WithRetryClassifier replaces the rule deciding whether a response is worth
// another attempt.
//
// The default, DefaultRetryClassification, retries 5xx, 408, and 429 and stops
// on every other 4xx. That is what the status registry says those codes mean;
// it is not always what a given service means by them. An API that reports its
// own overload as 400, or returns 200 with a failure document, or uses 409 for
// a condition that clears on its own, needs the rule stated in its terms rather
// than the registry's.
//
// The classifier answers in the retry package's vocabulary — nil to accept,
// retry.Unretryable to stop, any other error to try again — and should delegate
// to DefaultRetryClassification for whatever it does not have an opinion about:
//
//	httpclient.WithRetryPolicy(policy, httpclient.WithRetryClassifier(
//		func(resp *http.Response) error {
//			if resp.StatusCode == http.StatusConflict {
//				return platformerrors.New("lock still held")
//			}
//
//			return httpclient.DefaultRetryClassification(resp)
//		},
//	))
//
// Retry-After is still honored for whatever the classifier asks to retry, and
// the method and body-replay rules still apply — a classifier widens which
// responses are worth another attempt, not which requests may be repeated. A
// nil classifier is ignored.
func WithRetryClassifier(classifier RetryClassifier) RetryOption {
	return func(t *retryTransport) {
		if classifier != nil {
			t.classifier = classifier
		}
	}
}

// newRetryTransport resolves the retry options into an unattached transport.
// buildClient fills in the base and the observer.
func newRetryTransport(policy retry.Policy, opts []RetryOption) *retryTransport {
	t := &retryTransport{
		policy:        policy,
		classifier:    DefaultRetryClassification,
		methods:       defaultRetryMethods,
		maxRetryAfter: DefaultMaxRetryAfter,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}

	return t
}

// RoundTrip sends the request, retrying per the policy while the failure still
// looks worth another attempt.
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.retryable(req) {
		return t.base.RoundTrip(req)
	}

	var (
		resp     *http.Response
		attempts int
	)

	err := t.policy.Execute(req.Context(), func(ctx context.Context) error {
		// The previous attempt's response is superseded the moment another one
		// starts, so give its connection back rather than leaving it pinned for
		// the whole retry loop.
		if resp != nil {
			drainAndClose(resp)
			resp = nil
		}

		attempts++

		attempt := req.Clone(ctx)

		// The first attempt sends the body it was handed; the base transport
		// owns closing it. Later attempts need a fresh one, because that body
		// has been read.
		if attempts > 1 {
			t.obs.retryAttempts.Add(ctx, 1, requestAttrs(req))

			if req.GetBody != nil {
				body, bodyErr := req.GetBody()
				if bodyErr != nil {
					return retry.Unretryable(platformerrors.Wrap(bodyErr, "rewinding request body"))
				}

				attempt.Body = body
			}
		}

		var roundTripErr error

		// The body outlives this closure by design: a superseded response is
		// drained and closed at the top of the next attempt, and the surviving
		// one belongs to the caller.
		resp, roundTripErr = t.base.RoundTrip(attempt) //nolint:bodyclose // ownership passes to the next attempt or to the caller
		if roundTripErr != nil {
			// A transport error means no answer at all — a dial failure, a
			// reset, a timeout. Those are the failures retrying exists for.
			return platformerrors.Wrap(roundTripErr, "sending request")
		}

		return t.classify(ctx, req, resp)
	})

	// A policy that returns without ever running the operation would otherwise
	// yield (nil, nil), which no http.Client is prepared for.
	if resp == nil && err == nil {
		return nil, platformerrors.New("retry policy returned without sending the request")
	}

	t.report(req, resp, err, attempts)

	// Attempts are spent, but the last one still produced a real response. A
	// 503 is the server's answer, not this transport's failure, and the caller
	// reads it exactly as it would have without any retrying.
	if resp != nil {
		return resp, nil
	}

	return nil, err
}

// report records how the loop ended.
//
// It exists because RoundTrip throws the loop's error away whenever there is a
// response to return — which is the right answer for the caller and a terrible
// one for anybody trying to understand the system afterward. Without this, a
// request that burned four attempts and eight seconds of backoff to arrive at a
// 503 is indistinguishable from one that got a 503 immediately, and the
// difference between those two is the entire reason retrying was configured.
func (t *retryTransport) report(req *http.Request, resp *http.Response, err error, attempts int) {
	ctx := req.Context()

	// Onto the span observedTransport opened, so a trace answers "how many
	// times did we ask?" without needing the log line beside it.
	tracing.AttachToSpan(oteltrace.SpanFromContext(ctx), keys.RequestAttemptsKey, attempts)

	logger := t.obs.o11y.Logger().WithRequest(req).WithValue(keys.RequestAttemptsKey, attempts)
	if resp != nil {
		logger = logger.WithResponse(resp)
	}

	if err == nil {
		// One attempt that worked is the unremarkable case, and the tracing
		// transport below already recorded it.
		if attempts > 1 {
			logger.Debug("request succeeded after retrying")
		}

		return
	}

	// Stopped without ever retrying: a 4xx the classifier called terminal, or a
	// body that could not be rewound. Worth a line for anyone asking why no
	// retry happened, but it is not an exhausted loop and must not be counted
	// as one — every 404 this client sees comes through here.
	if attempts <= 1 {
		logger.WithError(err).Debug("not retried")

		return
	}

	attrs := []attribute.KeyValue{}
	if resp != nil {
		attrs = append(attrs, attribute.Int(keys.ResponseStatusKey, resp.StatusCode))
	}

	t.obs.retriesExhausted.Add(ctx, 1, requestAttrs(req, attrs...))

	logger.Error("giving up on request after retrying", err)
}

// retryable reports whether this request may be sent more than once.
func (t *retryTransport) retryable(req *http.Request) bool {
	// Without GetBody a second attempt would have to re-send a body that has
	// already been consumed, so the request gets one shot.
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return false
	}

	return slices.Contains(t.methods, req.Method)
}

// classify asks the classifier what the response means for another attempt, and
// honors Retry-After for whatever it asks to retry.
func (t *retryTransport) classify(ctx context.Context, req *http.Request, resp *http.Response) error {
	err := t.classifier(resp)
	if err == nil || errors.Is(err, retry.ErrUnretryable) {
		return err
	}

	delay, ok := retryAfterDelay(resp)
	if !ok {
		return err
	}

	logger := t.obs.o11y.Logger().WithRequest(req).WithResponse(resp).WithValue(keys.RetryAfterKey, delay.String())

	// A Retry-After beyond the cap is honored by not retrying at all. Retrying
	// sooner than asked is the behavior the header exists to prevent, and
	// parking the caller for an interval the server picked is worse than
	// handing back the response already in hand.
	if delay > t.maxRetryAfter {
		logger.Debug("Retry-After exceeds the cap, returning the response rather than waiting")

		return retry.Unretryable(err)
	}

	t.obs.retryAfterWaits.Record(ctx, delay.Seconds(), requestAttrs(req))
	logger.Debug("honoring Retry-After before the next attempt")

	// The policy's own backoff runs after this, so Retry-After is a floor under
	// the wait rather than a replacement for it.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
	}

	return err
}

// retryAfterDelay returns how long the response asks the caller to wait, and
// whether it asked at all. Both encodings RFC 9110 allows are read: a count of
// seconds and an HTTP date. A date already in the past means "now".
func retryAfterDelay(resp *http.Response) (time.Duration, bool) {
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if value == "" {
		return 0, false
	}

	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0, true
		}

		return time.Duration(seconds) * time.Second, true
	}

	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay, true
		}

		return 0, true
	}

	return 0, false
}

// drainAndClose consumes and closes a superseded response so its connection
// returns to the pool instead of being torn down.
func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}

	// Both failures are ignored on purpose: the bytes are being discarded either
	// way, and the only thing lost is the chance to reuse this connection.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes)) //nolint:errcheck // best-effort drain for connection reuse
	_ = resp.Body.Close()                                                //nolint:errcheck // best-effort close of a discarded response
}

package httpclient

import (
	"net/http"

	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// spanOperation names the span observedTransport opens, distinguishing it from
// the per-attempt spans the tracing transport emits below the retry loop.
const spanOperation = "resilient request"

// observedTransport opens one span covering the logical request the caller
// made, as distinct from the attempts it took to answer it.
//
// It is the outermost layer, above the circuit breaker, and it exists because
// the resilience stack otherwise leaves two holes in a trace:
//
// The tracing transport sits below the retry loop, which is correct — each
// attempt should be its own client span rather than one span smeared across a
// loop — but it means three attempts arrive as three sibling spans with nothing
// tying them to the one call the caller believes it made. This is their parent.
//
// And a request the breaker or the limiter refuses never reaches the tracing
// transport at all, so it produces no span whatsoever. A dependency this client
// has stopped talking to entirely is exactly the thing you want to find in a
// trace, and it was the one thing that could not be found there.
type observedTransport struct {
	base http.RoundTripper
	obs  *transportObserver
}

var _ http.RoundTripper = (*observedTransport)(nil)

// RoundTrip records the request, then hands it to the resilience stack.
func (t *observedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, op := t.obs.o11y.BeginCustom(req.Context(), tracing.FormatSpan(spanOperation, req))
	defer op.End()

	op.SpanOnly(keys.ServerAddressKey, req.URL.Host)
	op.SpanOnly(keys.RequestMethodKey, req.Method)

	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		// Attached to the span and returned unchanged. The layer that produced
		// it has already logged it with the context that makes it legible, and
		// wrapping it again here would only lengthen the message the caller
		// eventually reads.
		tracing.AttachErrorToSpan(op.Span(), "sending request", err)

		return nil, err
	}

	op.SpanOnly(keys.ResponseStatusKey, resp.StatusCode)

	return resp, nil
}

package httpclient

import (
	"net/http"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/ratelimiting"
)

// rateLimitTransport spends a token from a per-host bucket before each request.
type rateLimitTransport struct {
	base    http.RoundTripper
	limiter ratelimiting.RateLimiter
	obs     *transportObserver
}

var _ http.RoundTripper = (*rateLimitTransport)(nil)

// RoundTrip sends the request if the host's bucket has a token for it.
func (t *rateLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host

	allowed, err := t.limiter.Allow(req.Context(), host)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "consulting the rate limiter for host %q", host)
	}

	if !allowed {
		// Counted here rather than left to the limiter's own instruments. Those
		// know a token was refused; only this transport knows which host and
		// method it was refused for, and that the refusal cost an HTTP request
		// rather than some other kind of work.
		t.obs.rateLimited.Add(req.Context(), 1, requestAttrs(req))

		// Debug, not error: a bucket doing its job is the expected path, and
		// this fires once per attempt inside the retry loop. The line that
		// matters is the one the retry transport writes when the attempts run
		// out having never gotten a token.
		t.obs.o11y.Logger().WithRequest(req).Debug("rate limited before reaching the wire")

		// Refusals are deliberately left retryable: this transport sits inside
		// the retry loop, so the policy's backoff is what waits for the bucket
		// to refill. Marking it Unretryable would turn a full bucket into a
		// hard failure of the whole request.
		return nil, platformerrors.Wrapf(ratelimiting.ErrRateLimited, "host %q", host)
	}

	return t.base.RoundTrip(req)
}

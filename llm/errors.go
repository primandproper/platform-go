package llm

import (
	"fmt"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// The conditions a provider can report. Every error a Provider returns matches
// one of these under errors.Is, or none of them when the failure has no
// platform-level meaning — a transport error, say. They exist so that callers
// can react to a rate limit or a context overflow without importing, or even
// knowing about, whichever client library the provider is built on.
var (
	// ErrRateLimited means the provider refused the request for rate reasons.
	// Errors matching it are usually a *RateLimitError, which may carry how
	// long to wait.
	ErrRateLimited = platformerrors.New("llm provider rate limited the request")
	// ErrContextTooLong means the prompt did not fit in the model's context
	// window. Retrying unchanged will fail the same way.
	ErrContextTooLong = platformerrors.New("llm request exceeded the model's context window")
	// ErrAuthentication means the provider rejected the credentials.
	ErrAuthentication = platformerrors.New("llm provider rejected the credentials")
	// ErrModelNotFound means the requested model does not exist, or the account
	// cannot reach it.
	ErrModelNotFound = platformerrors.New("llm model not found")
	// ErrContentFiltered means the provider's safety filter refused the
	// request or the response.
	ErrContentFiltered = platformerrors.New("llm provider filtered the content")
	// ErrInvalidRequest means the request was malformed. This covers requests
	// this package rejects before sending as well as ones the provider
	// rejected.
	ErrInvalidRequest = platformerrors.New("llm request was invalid")
	// ErrUnsupportedFeature means the provider or model does not support
	// something the request asked for. Provider.Capabilities reports the coarse
	// version of this ahead of time.
	ErrUnsupportedFeature = platformerrors.New("llm provider does not support the requested feature")
)

// RateLimitError is the rate limit condition with the provider's advice about
// when to try again attached. It matches ErrRateLimited under errors.Is, so a
// caller that only wants to know "was I throttled" need not reach for
// errors.As.
type RateLimitError struct {
	// RetryAfter is how long the provider asked the caller to wait. It is zero
	// when the provider did not say, which is not the same as "retry
	// immediately" — a caller with no advice should fall back to its own
	// backoff.
	RetryAfter time.Duration
}

// Error implements error.
func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s (retry after %s)", ErrRateLimited, e.RetryAfter)
	}

	return ErrRateLimited.Error()
}

// Unwrap returns ErrRateLimited, so that errors.Is matches the sentinel.
func (e *RateLimitError) Unwrap() error {
	return ErrRateLimited
}

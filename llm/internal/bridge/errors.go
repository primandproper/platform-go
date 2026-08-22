package bridge

import (
	"errors"
	"time"

	"github.com/primandproper/platform-go/v13/llm"

	anyllm "github.com/mozilla-ai/any-llm-go"
)

// normalizedError pairs a platform sentinel with the upstream error that
// produced it. It unwraps to both, so errors.Is finds the sentinel and the
// upstream message stays available for logs — which matters, because the
// sentinels are deliberately generic and "invalid request" on its own tells an
// operator nothing about which part of the request was invalid.
type normalizedError struct {
	sentinel error
	cause    error
}

// Error implements error.
func (e *normalizedError) Error() string {
	return e.sentinel.Error() + ": " + e.cause.Error()
}

// Unwrap returns both the sentinel and the cause, so that errors.Is and
// errors.As traverse either branch.
func (e *normalizedError) Unwrap() []error {
	return []error{e.sentinel, e.cause}
}

// NormalizeError maps an any-llm-go error onto the platform's sentinels.
//
// This is the seam that keeps callers off our transitive dependency: an
// application handling a rate limit does it with llm.ErrRateLimited and
// *llm.RateLimitError, never with anyllm's. An error that matches none of
// any-llm-go's classifications — a transport failure, a context cancellation —
// is returned unchanged, because inventing a platform-level meaning for it
// would be a lie.
func NormalizeError(err error) error {
	if err == nil {
		return nil
	}

	// Rate limits carry data, so they get a type rather than a sentinel.
	if rateLimit, ok := errors.AsType[*anyllm.RateLimitError](err); ok {
		return &normalizedError{
			sentinel: &llm.RateLimitError{RetryAfter: time.Duration(rateLimit.RetryAfter) * time.Second},
			cause:    err,
		}
	}

	mappings := []struct {
		upstream error
		sentinel error
	}{
		{anyllm.ErrRateLimit, llm.ErrRateLimited},
		{anyllm.ErrContextLength, llm.ErrContextTooLong},
		{anyllm.ErrAuthentication, llm.ErrAuthentication},
		// A missing API key is an authentication failure the caller can act on
		// in exactly the same way, so it does not get a sentinel of its own.
		{anyllm.ErrMissingAPIKey, llm.ErrAuthentication},
		{anyllm.ErrModelNotFound, llm.ErrModelNotFound},
		{anyllm.ErrContentFilter, llm.ErrContentFiltered},
		{anyllm.ErrInvalidRequest, llm.ErrInvalidRequest},
		{anyllm.ErrUnsupportedParam, llm.ErrUnsupportedFeature},
		{anyllm.ErrUnsupportedProvider, llm.ErrUnsupportedFeature},
	}

	for i := range mappings {
		if errors.Is(err, mappings[i].upstream) {
			return &normalizedError{sentinel: mappings[i].sentinel, cause: err}
		}
	}

	return err
}

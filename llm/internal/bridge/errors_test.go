package bridge

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/llm"

	anyllm "github.com/mozilla-ai/any-llm-go"
	anyllmerrors "github.com/mozilla-ai/any-llm-go/errors"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNormalizeError(T *testing.T) {
	T.Parallel()

	T.Run("maps every classified upstream error onto a platform sentinel", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			upstream error
			sentinel error
			name     string
		}{
			{
				name:     "context length",
				upstream: anyllmerrors.NewContextLengthError("anthropic", errors.New("too long")),
				sentinel: llm.ErrContextTooLong,
			},
			{
				name:     "authentication",
				upstream: anyllmerrors.NewAuthenticationError("anthropic", errors.New("bad key")),
				sentinel: llm.ErrAuthentication,
			},
			{
				name:     "missing api key",
				upstream: anyllmerrors.NewMissingAPIKeyError("anthropic", "ANTHROPIC_API_KEY"),
				sentinel: llm.ErrAuthentication,
			},
			{
				name:     "model not found",
				upstream: anyllmerrors.NewModelNotFoundError("openai", errors.New("no such model")),
				sentinel: llm.ErrModelNotFound,
			},
			{
				name:     "content filter",
				upstream: anyllmerrors.NewContentFilterError("openai", errors.New("blocked")),
				sentinel: llm.ErrContentFiltered,
			},
			{
				name:     "invalid request",
				upstream: anyllmerrors.NewInvalidRequestError("openai", errors.New("malformed")),
				sentinel: llm.ErrInvalidRequest,
			},
			{
				name:     "unsupported parameter",
				upstream: anyllmerrors.NewUnsupportedParamError("openai", "seed"),
				sentinel: llm.ErrUnsupportedFeature,
			},
			{
				name:     "unsupported provider",
				upstream: anyllmerrors.NewUnsupportedProviderError("gopher"),
				sentinel: llm.ErrUnsupportedFeature,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				err := NormalizeError(tc.upstream)
				must.ErrorIs(t, err, tc.sentinel)

				// The upstream message has to survive: the sentinels are
				// deliberately generic, and an operator reading a log needs the
				// provider's own words to act on.
				test.StrContains(t, err.Error(), tc.upstream.Error())
			})
		}
	})

	T.Run("normalizes a rate limit into the typed error", func(t *testing.T) {
		t.Parallel()

		upstream := anyllmerrors.NewRateLimitError("anthropic", errors.New("429"))
		upstream.RetryAfter = 30

		err := NormalizeError(upstream)

		must.ErrorIs(t, err, llm.ErrRateLimited)

		rateLimit, ok := errors.AsType[*llm.RateLimitError](err)
		must.True(t, ok)
		test.EqOp(t, 30*time.Second, rateLimit.RetryAfter)
	})

	T.Run("normalizes a rate limit with no retry advice", func(t *testing.T) {
		t.Parallel()

		err := NormalizeError(anyllmerrors.NewRateLimitError("openai", errors.New("429")))

		must.ErrorIs(t, err, llm.ErrRateLimited)

		rateLimit, ok := errors.AsType[*llm.RateLimitError](err)
		must.True(t, ok)
		test.EqOp(t, time.Duration(0), rateLimit.RetryAfter)
	})

	T.Run("matches the bare sentinel when there is no typed error", func(t *testing.T) {
		t.Parallel()

		// Nothing in any-llm-go returns a bare sentinel today, but errors.Is is
		// the documented contract and a provider that starts wrapping one
		// directly must still normalize.
		err := NormalizeError(fmt.Errorf("wrapped: %w", anyllm.ErrRateLimit))

		must.ErrorIs(t, err, llm.ErrRateLimited)
	})

	T.Run("finds a classified error through a wrapper", func(t *testing.T) {
		t.Parallel()

		upstream := anyllmerrors.NewContextLengthError("anthropic", errors.New("too long"))

		err := NormalizeError(fmt.Errorf("completing request: %w", upstream))

		must.ErrorIs(t, err, llm.ErrContextTooLong)
	})

	T.Run("leaves an unclassified error alone", func(t *testing.T) {
		t.Parallel()

		// A transport failure has no platform-level meaning, and inventing one
		// would tell the caller something untrue about whether to retry.
		upstream := errors.New("dial tcp: connection refused")

		err := NormalizeError(upstream)

		test.EqOp(t, upstream, err)

		for _, sentinel := range []error{
			llm.ErrRateLimited,
			llm.ErrContextTooLong,
			llm.ErrAuthentication,
			llm.ErrModelNotFound,
			llm.ErrContentFiltered,
			llm.ErrInvalidRequest,
			llm.ErrUnsupportedFeature,
		} {
			test.False(t, errors.Is(err, sentinel))
		}
	})

	T.Run("leaves a general provider error alone", func(t *testing.T) {
		t.Parallel()

		// anyllm.ErrProvider is the "something went wrong upstream" bucket, and
		// there is no platform sentinel that says more than the error already
		// does.
		upstream := error(anyllmerrors.NewProviderError("openai", errors.New("500")))

		test.EqOp(t, upstream, NormalizeError(upstream))
	})

	T.Run("with a nil error", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, NormalizeError(nil))
	})
}

func TestNormalizedError(T *testing.T) {
	T.Parallel()

	T.Run("unwraps to both the sentinel and the cause", func(t *testing.T) {
		t.Parallel()

		cause := errors.New("the provider's own words")
		err := error(&normalizedError{sentinel: llm.ErrInvalidRequest, cause: cause})

		must.ErrorIs(t, err, llm.ErrInvalidRequest)
		must.ErrorIs(t, err, cause)
		test.EqOp(t, llm.ErrInvalidRequest.Error()+": "+cause.Error(), err.Error())
	})
}

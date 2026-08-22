package http

import (
	stderrors "errors"
	"net/http"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/ratelimiting"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRateLimitedMapping(T *testing.T) {
	T.Parallel()

	T.Run("maps the sentinel to a 429", func(t *testing.T) {
		t.Parallel()

		code, msg := ToAPIError(ratelimiting.ErrRateLimited)
		test.EqOp(t, ErrTooManyRequests, code)
		test.EqOp(t, "too many requests", msg)
		test.EqOp(t, http.StatusTooManyRequests, HTTPStatusForCode(code))
	})

	T.Run("maps a wrapped sentinel too", func(t *testing.T) {
		t.Parallel()

		// The middleware wraps the sentinel with what it was doing when the
		// limiter failed, so the mapping has to survive that.
		code, _ := ToAPIError(platformerrors.Wrap(ratelimiting.ErrRateLimited, "consulting the rate limiter"))
		test.EqOp(t, ErrTooManyRequests, code)
	})

	T.Run("says nothing about the limit or the key", func(t *testing.T) {
		t.Parallel()

		// The message reaches the client verbatim. A threshold in it is a gift
		// to whoever is probing for one.
		_, msg := ToAPIError(platformerrors.Wrap(ratelimiting.ErrRateLimited, "key ip:203.0.113.7 over 10/s"))
		test.EqOp(t, "too many requests", msg)
	})

	T.Run("round-trips through the response envelope", func(t *testing.T) {
		t.Parallel()

		status, body := ToAPIResponse(ratelimiting.ErrRateLimited)
		must.NotNil(t, body)
		test.EqOp(t, http.StatusTooManyRequests, status)
		must.NotNil(t, body.Error)
		test.EqOp(t, ErrTooManyRequests, body.Error.Code)
	})
}

func TestErrorForCode(T *testing.T) {
	T.Parallel()

	T.Run("returns the sentinel a code was mapped from", func(t *testing.T) {
		t.Parallel()

		// This is the direction a typed client reads: 429 with E116 becomes the
		// same sentinel an in-process caller would have been handed.
		err := ErrorForCode(ErrTooManyRequests)
		must.NotNil(t, err)
		test.True(t, stderrors.Is(err, ratelimiting.ErrRateLimited))
	})

	T.Run("returns nil for a code with no single sentinel behind it", func(t *testing.T) {
		t.Parallel()

		// Five sentinels and every registered domain mapper produce this code;
		// there is no one error to hand back, and guessing would be worse.
		test.Nil(t, ErrorForCode(ErrValidatingRequestInput))
		test.Nil(t, ErrorForCode(ErrNothingSpecific))
		test.Nil(t, ErrorForCode(ErrorCode("E999")))
	})

	T.Run("inverts the forward mapping for every code it claims", func(t *testing.T) {
		t.Parallel()

		// The table is only useful if it is actually an inverse: every entry's
		// sentinel must map forward to the code it is filed under.
		for code, err := range codeToError {
			forward, _, ok := PlatformMapper.Map(err)
			must.True(t, ok, must.Sprintf("no forward mapping for %v", err))
			test.EqOp(t, code, forward)
		}
	})
}

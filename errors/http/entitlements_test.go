package http

import (
	stderrors "errors"
	"net/http"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v10/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestEntitlementMappings(T *testing.T) {
	T.Parallel()

	T.Run("maps not-entitled to a 402", func(t *testing.T) {
		t.Parallel()

		code, msg := ToAPIError(platformerrors.ErrNotEntitled)

		test.EqOp(t, ErrNotEntitled, code)
		test.EqOp(t, "not entitled", msg)
		test.EqOp(t, http.StatusPaymentRequired, HTTPStatusForCode(code))
	})

	T.Run("maps an exhausted quota to a 402 with its own code", func(t *testing.T) {
		t.Parallel()

		code, msg := ToAPIError(platformerrors.ErrQuotaExhausted)

		test.EqOp(t, ErrQuotaExhausted, code)
		test.EqOp(t, "quota exhausted for the current billing period", msg)
		test.EqOp(t, http.StatusPaymentRequired, HTTPStatusForCode(code))
	})

	T.Run("the two denials keep their own codes", func(t *testing.T) {
		t.Parallel()

		// Same status, different code, because the remedies differ: one is
		// answered by upgrading and the other by waiting for the period to roll.
		entitled, _ := ToAPIError(platformerrors.ErrNotEntitled)
		exhausted, _ := ToAPIError(platformerrors.ErrQuotaExhausted)

		test.NotEqOp(t, entitled, exhausted)
	})

	T.Run("neither is a permission denial", func(t *testing.T) {
		t.Parallel()

		// A 403 sends the customer to an administrator who cannot help.
		code, _ := ToAPIError(platformerrors.ErrNotEntitled)

		test.NotEqOp(t, ErrUserIsNotAuthorized, code)
		test.NotEqOp(t, http.StatusForbidden, HTTPStatusForCode(code))
	})

	T.Run("an exhausted quota is not a rate limit", func(t *testing.T) {
		t.Parallel()

		// A 429 tells a client to retry a request that will fail identically for
		// the rest of the month.
		code, _ := ToAPIError(platformerrors.ErrQuotaExhausted)

		test.NotEqOp(t, ErrTooManyRequests, code)
		test.NotEqOp(t, http.StatusTooManyRequests, HTTPStatusForCode(code))
	})

	T.Run("maps wrapped sentinels too", func(t *testing.T) {
		t.Parallel()

		code, _ := ToAPIError(platformerrors.Wrap(platformerrors.ErrQuotaExhausted, "checking entitlement"))

		test.EqOp(t, ErrQuotaExhausted, code)
	})

	T.Run("says nothing about the feature or the limit", func(t *testing.T) {
		t.Parallel()

		// The message reaches the client verbatim. Which features exist is not
		// something to disclose to a caller who just failed to reach one.
		_, msg := ToAPIError(platformerrors.Wrap(platformerrors.ErrNotEntitled, "feature advanced_search on plan free"))

		test.EqOp(t, "not entitled", msg)
	})

	T.Run("round-trips through the response envelope", func(t *testing.T) {
		t.Parallel()

		status, body := ToAPIResponse(platformerrors.ErrNotEntitled)

		must.NotNil(t, body)
		must.NotNil(t, body.Error)
		test.EqOp(t, http.StatusPaymentRequired, status)
		test.EqOp(t, ErrNotEntitled, body.Error.Code)
	})

	T.Run("round-trips back to the sentinel", func(t *testing.T) {
		t.Parallel()

		// What a typed client reads: the same sentinel a caller inside the
		// serving process would have gotten.
		test.True(t, stderrors.Is(ErrorForCode(ErrNotEntitled), platformerrors.ErrNotEntitled))
		test.True(t, stderrors.Is(ErrorForCode(ErrQuotaExhausted), platformerrors.ErrQuotaExhausted))
	})
}

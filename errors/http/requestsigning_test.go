package http

import (
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRequestSignatureMapping(T *testing.T) {
	T.Parallel()

	T.Run("maps an invalid signature to a 401", func(t *testing.T) {
		t.Parallel()

		code, msg := ToAPIError(requestsigning.ErrInvalidSignature)
		test.EqOp(t, ErrInvalidRequestSignature, code)
		test.EqOp(t, "invalid request signature", msg)
		test.EqOp(t, http.StatusUnauthorized, HTTPStatusForCode(code))
	})

	// Same code, different message. Clock skew is the one benign cause, and it
	// is the only thing a caller can act on without knowing the key.
	T.Run("distinguishes a stale signature in the message only", func(t *testing.T) {
		t.Parallel()

		code, msg := ToAPIError(requestsigning.ErrStaleSignature)
		test.EqOp(t, ErrInvalidRequestSignature, code)
		test.EqOp(t, "request signature timestamp outside tolerance", msg)
		test.EqOp(t, http.StatusUnauthorized, HTTPStatusForCode(code))
	})

	T.Run("maps a wrapped sentinel too", func(t *testing.T) {
		t.Parallel()

		// The middleware wraps the sentinel with what it was doing, and the
		// stale one arrives carrying the skew it measured.
		code, _ := ToAPIError(platformerrors.Wrap(requestsigning.ErrInvalidSignature, "no X-Platform-Signature header"))
		test.EqOp(t, ErrInvalidRequestSignature, code)

		code, _ = ToAPIError(platformerrors.Wrapf(requestsigning.ErrStaleSignature, "timestamp %d is 6m0s from now", 1))
		test.EqOp(t, ErrInvalidRequestSignature, code)
	})

	T.Run("says nothing about the key or the header contents", func(t *testing.T) {
		t.Parallel()

		// The message reaches the client verbatim.
		_, msg := ToAPIError(platformerrors.Wrap(requestsigning.ErrInvalidSignature, "expected s=deadbeef"))
		test.EqOp(t, "invalid request signature", msg)
	})

	// A verifier with no keys is the server's own misconfiguration. Mapping it
	// to 401 would report a broken deployment as a fleet of bad callers.
	T.Run("a verifier with no keys is not a 401", func(t *testing.T) {
		t.Parallel()

		code, _ := ToAPIError(requestsigning.ErrNoVerificationKey)
		test.EqOp(t, ErrNothingSpecific, code)
		test.EqOp(t, http.StatusInternalServerError, HTTPStatusForCode(code))
	})

	T.Run("round-trips through the response envelope", func(t *testing.T) {
		t.Parallel()

		status, body := ToAPIResponse(requestsigning.ErrInvalidSignature)
		must.NotNil(t, body)
		test.EqOp(t, http.StatusUnauthorized, status)
		must.NotNil(t, body.Error)
		test.EqOp(t, ErrInvalidRequestSignature, body.Error.Code)
	})

	// Two sentinels produce this code, so the reverse table cannot claim it:
	// handing back one of them would be a guess a client would then branch on.
	T.Run("has no reverse mapping", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, ErrorForCode(ErrInvalidRequestSignature))
	})
}

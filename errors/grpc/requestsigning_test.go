package grpc

import (
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRequestSignatureMapping(T *testing.T) {
	T.Parallel()

	// Unauthenticated, not PermissionDenied: nothing has been identified yet,
	// so there is nothing to deny.
	T.Run("maps both sentinels to Unauthenticated", func(t *testing.T) {
		t.Parallel()

		for _, sentinel := range []error{requestsigning.ErrInvalidSignature, requestsigning.ErrStaleSignature} {
			code, ok := PlatformMapper.Map(sentinel)
			must.True(t, ok)
			test.EqOp(t, codes.Unauthenticated, code)
		}
	})

	T.Run("maps a wrapped sentinel too", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, codes.Unauthenticated,
			MapToGRPC(platformerrors.Wrap(requestsigning.ErrInvalidSignature, "verifying the request signature"), codes.Unknown))
	})

	T.Run("sends the sentinel's own message rather than the code's", func(t *testing.T) {
		t.Parallel()

		// Neither sentinel says anything about the key, and the stale one names
		// the single cause a caller can fix for itself. The wrapping context —
		// which does name things — must not follow it out.
		interceptor := UnaryErrorEncodingInterceptor()

		_, err := interceptor(t.Context(), nil, nil, failingHandler(
			platformerrors.Wrapf(requestsigning.ErrStaleSignature, "timestamp %d is 6m0s from now", 1753900000),
		))
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, codes.Unauthenticated, st.Code())
		test.EqOp(t, requestsigning.ErrStaleSignature.Error(), st.Message())
		test.StrNotContains(t, st.Message(), "1753900000")
	})

	// The server's own misconfiguration is not the caller's problem to solve,
	// and must not be reported as though it were.
	T.Run("a verifier with no keys is not Unauthenticated", func(t *testing.T) {
		t.Parallel()

		_, ok := PlatformMapper.Map(requestsigning.ErrNoVerificationKey)
		test.False(t, ok)
	})
}

package grpc

import (
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestEntitlementMappings(T *testing.T) {
	T.Parallel()

	T.Run("maps not-entitled to PermissionDenied", func(t *testing.T) {
		t.Parallel()

		code, ok := PlatformMapper.Map(platformerrors.ErrNotEntitled)

		must.True(t, ok)
		test.EqOp(t, codes.PermissionDenied, code)
	})

	T.Run("maps an exhausted quota to ResourceExhausted", func(t *testing.T) {
		t.Parallel()

		code, ok := PlatformMapper.Map(platformerrors.ErrQuotaExhausted)

		must.True(t, ok)
		test.EqOp(t, codes.ResourceExhausted, code)
	})

	T.Run("maps wrapped sentinels too", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, codes.ResourceExhausted,
			MapToGRPC(platformerrors.Wrap(platformerrors.ErrQuotaExhausted, "checking entitlement"), codes.Unknown))
		test.EqOp(t, codes.PermissionDenied,
			MapToGRPC(platformerrors.Wrap(platformerrors.ErrNotEntitled, "checking entitlement"), codes.Unknown))
	})

	T.Run("sends the sentinel's own message rather than the code's", func(t *testing.T) {
		t.Parallel()

		// gRPC has no 402 to be precise with, so the two denials arrive under
		// codes that are also used for other things. The message is the only
		// thing that tells a caller which remedy applies — and the wrapping
		// context, which names the feature, must not follow it out.
		interceptor := UnaryErrorEncodingInterceptor()

		_, err := interceptor(t.Context(), nil, nil, failingHandler(
			platformerrors.Wrap(platformerrors.ErrQuotaExhausted, "feature llm_tokens on plan pro"),
		))
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, codes.ResourceExhausted, st.Code())
		test.EqOp(t, platformerrors.ErrQuotaExhausted.Error(), st.Message())
		test.StrNotContains(t, st.Message(), "llm_tokens")
	})

	T.Run("an exhausted quota does not decay into not-entitled", func(t *testing.T) {
		t.Parallel()

		// The specific sentinel is checked first, so a handler that returns both
		// does not collapse into the vaguer answer.
		joined := platformerrors.Join(platformerrors.ErrNotEntitled, platformerrors.ErrQuotaExhausted)

		code, ok := PlatformMapper.Map(joined)

		must.True(t, ok)
		test.EqOp(t, codes.ResourceExhausted, code)
	})
}

package grpc

import (
	"context"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/ratelimiting"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// failingHandler is a unary handler that always returns err.
func failingHandler(err error) grpc.UnaryHandler {
	return func(context.Context, any) (any, error) { return nil, err }
}

func TestRateLimitedMapping(T *testing.T) {
	T.Parallel()

	T.Run("maps the sentinel to ResourceExhausted", func(t *testing.T) {
		t.Parallel()

		code, ok := PlatformMapper.Map(ratelimiting.ErrRateLimited)
		must.True(t, ok)
		test.EqOp(t, codes.ResourceExhausted, code)
	})

	T.Run("maps a wrapped sentinel too", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, codes.ResourceExhausted,
			MapToGRPC(platformerrors.Wrap(ratelimiting.ErrRateLimited, "consulting the rate limiter"), codes.Unknown))
	})

	T.Run("sends the sentinel's own message rather than the code's", func(t *testing.T) {
		t.Parallel()

		// The sentinel is documented as client-safe, and "rate limited" tells a
		// caller what to do differently in a way that "ResourceExhausted" does
		// not. The wrapping context must not follow it out.
		interceptor := UnaryErrorEncodingInterceptor()

		_, err := interceptor(t.Context(), nil, nil, failingHandler(
			platformerrors.Wrap(ratelimiting.ErrRateLimited, "key ip:203.0.113.7 over 10/s"),
		))
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, codes.ResourceExhausted, st.Code())
		test.EqOp(t, ratelimiting.ErrRateLimited.Error(), st.Message())
		test.StrNotContains(t, st.Message(), "203.0.113.7")
	})

	T.Run("carries the encoded error in the status details", func(t *testing.T) {
		t.Parallel()

		interceptor := UnaryErrorEncodingInterceptor()

		_, err := interceptor(t.Context(), nil, nil, failingHandler(ratelimiting.ErrRateLimited))
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.SliceNotEmpty(t, st.Details())
	})
}

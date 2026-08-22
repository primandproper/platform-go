package grpc

import (
	"context"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	grpcerrors "github.com/primandproper/platform-go/v13/errors/grpc"
	"github.com/primandproper/platform-go/v13/ratelimiting"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testMethod = "/things.v1.Things/GetThing"

// stubLimiter answers however the test tells it to, and records the keys it was
// asked about.
type stubLimiter struct {
	allow func(key string) (bool, error)
	keys  []string
}

func (s *stubLimiter) Allow(_ context.Context, key string) (bool, error) {
	s.keys = append(s.keys, key)

	return s.allow(key)
}

func (s *stubLimiter) Close() error { return nil }

// hintingLimiter is a stubLimiter that also answers RetryHinter.
type hintingLimiter struct {
	*stubLimiter

	delay time.Duration
	ok    bool
}

func (h hintingLimiter) RetryAfter(context.Context, string) (time.Duration, bool) {
	return h.delay, h.ok
}

func alwaysAllow() *stubLimiter {
	return &stubLimiter{allow: func(string) (bool, error) { return true, nil }}
}

func alwaysRefuse() *stubLimiter {
	return &stubLimiter{allow: func(string) (bool, error) { return false, nil }}
}

// invoke runs one RPC through the interceptor and reports what the caller saw,
// plus whether the handler ran.
func invoke(t *testing.T, interceptor grpc.UnaryServerInterceptor) (error, bool) {
	t.Helper()

	reached := false
	handler := func(context.Context, any) (any, error) {
		reached = true

		return "ok", nil
	}

	_, err := interceptor(t.Context(), "req", &grpc.UnaryServerInfo{FullMethod: testMethod}, handler)

	return err, reached
}

// staticKey is a KeyFunc that always returns the same key.
func staticKey(key string) KeyFunc {
	return func(context.Context, *grpc.UnaryServerInfo) (string, error) { return key, nil }
}

// retryInfo returns the RetryInfo detail on err, or nil.
func retryInfo(t *testing.T, err error) *errdetails.RetryInfo {
	t.Helper()

	st, ok := status.FromError(err)
	must.True(t, ok)

	for _, detail := range st.Details() {
		if info, isInfo := detail.(*errdetails.RetryInfo); isInfo {
			return info
		}
	}

	return nil
}

func TestNewUnaryServerInterceptor(T *testing.T) {
	T.Parallel()

	T.Run("refuses to build without a limiter", func(t *testing.T) {
		t.Parallel()

		interceptor, err := NewUnaryServerInterceptor(nil, KeyByPeer())
		must.ErrorIs(t, err, ErrNilLimiter)
		test.Nil(t, interceptor)
	})

	T.Run("refuses to build without a key function", func(t *testing.T) {
		t.Parallel()

		interceptor, err := NewUnaryServerInterceptor(alwaysAllow(), nil)
		must.ErrorIs(t, err, ErrNilKeyFunc)
		test.Nil(t, interceptor)
	})

	T.Run("builds with no observability at all", func(t *testing.T) {
		t.Parallel()

		interceptor, err := NewUnaryServerInterceptor(alwaysAllow(), KeyByPeer())
		must.NoError(t, err)
		test.NotNil(t, interceptor)
	})
}

func TestInterceptor_Allowed(T *testing.T) {
	T.Parallel()

	T.Run("passes an allowed RPC to the handler", func(t *testing.T) {
		t.Parallel()

		limiter := alwaysAllow()

		interceptor, err := NewUnaryServerInterceptor(limiter, staticKey("caller"))
		must.NoError(t, err)

		rpcErr, reached := invoke(t, interceptor)
		must.NoError(t, rpcErr)
		test.True(t, reached)
		test.Eq(t, []string{"caller"}, limiter.keys)
	})

	T.Run("never consults the limiter for an exempted RPC", func(t *testing.T) {
		t.Parallel()

		limiter := alwaysRefuse()

		interceptor, err := NewUnaryServerInterceptor(limiter, staticKey(""))
		must.NoError(t, err)

		rpcErr, reached := invoke(t, interceptor)
		must.NoError(t, rpcErr)
		test.True(t, reached)
		test.SliceEmpty(t, limiter.keys)
	})
}

func TestInterceptor_Refused(T *testing.T) {
	T.Parallel()

	T.Run("answers RESOURCE_EXHAUSTED", func(t *testing.T) {
		t.Parallel()

		interceptor, err := NewUnaryServerInterceptor(alwaysRefuse(), staticKey("caller"))
		must.NoError(t, err)

		rpcErr, reached := invoke(t, interceptor)
		must.Error(t, rpcErr)
		test.False(t, reached)

		st, ok := status.FromError(rpcErr)
		must.True(t, ok)
		test.EqOp(t, codes.ResourceExhausted, st.Code())
	})

	T.Run("unwraps to the platform sentinel", func(t *testing.T) {
		t.Parallel()

		// An in-process caller and a remote one branch on the same value.
		interceptor, err := NewUnaryServerInterceptor(alwaysRefuse(), staticKey("caller"))
		must.NoError(t, err)

		rpcErr, _ := invoke(t, interceptor)
		must.ErrorIs(t, rpcErr, ratelimiting.ErrRateLimited)
	})

	T.Run("says nothing about the key or the limit", func(t *testing.T) {
		t.Parallel()

		interceptor, err := NewUnaryServerInterceptor(alwaysRefuse(), staticKey("peer:203.0.113.7"))
		must.NoError(t, err)

		rpcErr, _ := invoke(t, interceptor)

		st, ok := status.FromError(rpcErr)
		must.True(t, ok)
		test.EqOp(t, ratelimiting.ErrRateLimited.Error(), st.Message())
		test.StrNotContains(t, st.Message(), "203.0.113.7")
	})

	T.Run("keeps the right code through the error-encoding interceptor", func(t *testing.T) {
		t.Parallel()

		// The two compose in a real server, and the outer one re-derives the
		// code from the mapping rather than from the status already on it.
		guarded, err := NewUnaryServerInterceptor(alwaysRefuse(), staticKey("caller"))
		must.NoError(t, err)

		encoding := grpcerrors.UnaryErrorEncodingInterceptor()

		_, rpcErr := encoding(t.Context(), "req", &grpc.UnaryServerInfo{FullMethod: testMethod},
			func(ctx context.Context, req any) (any, error) {
				return guarded(ctx, req, &grpc.UnaryServerInfo{FullMethod: testMethod},
					func(context.Context, any) (any, error) { return "ok", nil })
			})
		must.Error(t, rpcErr)

		st, ok := status.FromError(rpcErr)
		must.True(t, ok)
		test.EqOp(t, codes.ResourceExhausted, st.Code())
		test.EqOp(t, ratelimiting.ErrRateLimited.Error(), st.Message())
	})
}

func TestInterceptor_RetryInfo(T *testing.T) {
	T.Parallel()

	T.Run("attaches the limiter's own estimate", func(t *testing.T) {
		t.Parallel()

		limiter := hintingLimiter{stubLimiter: alwaysRefuse(), delay: 3 * time.Second, ok: true}

		interceptor, err := NewUnaryServerInterceptor(limiter, staticKey("caller"))
		must.NoError(t, err)

		rpcErr, _ := invoke(t, interceptor)

		info := retryInfo(t, rpcErr)
		must.NotNil(t, info)
		test.EqOp(t, 3*time.Second, info.GetRetryDelay().AsDuration())
	})

	T.Run("falls back when the limiter cannot estimate", func(t *testing.T) {
		t.Parallel()

		interceptor, err := NewUnaryServerInterceptor(alwaysRefuse(), staticKey("caller"),
			WithRetryAfter(5*time.Second))
		must.NoError(t, err)

		rpcErr, _ := invoke(t, interceptor)

		info := retryInfo(t, rpcErr)
		must.NotNil(t, info)
		test.EqOp(t, 5*time.Second, info.GetRetryDelay().AsDuration())
	})

	T.Run("attaches nothing when the fallback is suppressed", func(t *testing.T) {
		t.Parallel()

		interceptor, err := NewUnaryServerInterceptor(alwaysRefuse(), staticKey("caller"),
			WithoutFallbackRetryAfter())
		must.NoError(t, err)

		rpcErr, _ := invoke(t, interceptor)
		test.Nil(t, retryInfo(t, rpcErr))
	})

	T.Run("still attaches a real estimate with the fallback suppressed", func(t *testing.T) {
		t.Parallel()

		limiter := hintingLimiter{stubLimiter: alwaysRefuse(), delay: 2 * time.Second, ok: true}

		interceptor, err := NewUnaryServerInterceptor(limiter, staticKey("caller"),
			WithoutFallbackRetryAfter())
		must.NoError(t, err)

		rpcErr, _ := invoke(t, interceptor)

		info := retryInfo(t, rpcErr)
		must.NotNil(t, info)
		test.EqOp(t, 2*time.Second, info.GetRetryDelay().AsDuration())
	})
}

func TestInterceptor_LimiterFailure(T *testing.T) {
	T.Parallel()

	boom := platformerrors.New("redis is having a moment")

	failing := func() *stubLimiter {
		return &stubLimiter{allow: func(string) (bool, error) { return false, boom }}
	}

	T.Run("lets the RPC through by default", func(t *testing.T) {
		t.Parallel()

		interceptor, err := NewUnaryServerInterceptor(failing(), staticKey("caller"))
		must.NoError(t, err)

		rpcErr, reached := invoke(t, interceptor)
		must.NoError(t, rpcErr)
		test.True(t, reached)
	})

	T.Run("refuses the RPC when told to fail closed", func(t *testing.T) {
		t.Parallel()

		interceptor, err := NewUnaryServerInterceptor(failing(), staticKey("caller"), WithFailClosed())
		must.NoError(t, err)

		rpcErr, reached := invoke(t, interceptor)
		must.Error(t, rpcErr)
		test.False(t, reached)

		st, ok := status.FromError(rpcErr)
		must.True(t, ok)
		test.EqOp(t, codes.ResourceExhausted, st.Code())
	})

	T.Run("attaches no hint to a failure it did not measure", func(t *testing.T) {
		t.Parallel()

		// Nothing timed a wait here, so there is no honest number to give.
		interceptor, err := NewUnaryServerInterceptor(failing(), staticKey("caller"), WithFailClosed())
		must.NoError(t, err)

		rpcErr, _ := invoke(t, interceptor)
		test.Nil(t, retryInfo(t, rpcErr))
	})

	T.Run("treats a key extractor failure the same way", func(t *testing.T) {
		t.Parallel()

		limiter := alwaysAllow()
		keyFn := func(context.Context, *grpc.UnaryServerInfo) (string, error) { return "", boom }

		open, err := NewUnaryServerInterceptor(limiter, keyFn)
		must.NoError(t, err)

		rpcErr, reached := invoke(t, open)
		must.NoError(t, rpcErr)
		test.True(t, reached)
		test.SliceEmpty(t, limiter.keys)

		closed, err := NewUnaryServerInterceptor(alwaysAllow(), keyFn, WithFailClosed())
		must.NoError(t, err)

		rpcErr, reached = invoke(t, closed)
		must.Error(t, rpcErr)
		test.False(t, reached)
	})

	T.Run("keeps the failure's context out of the status", func(t *testing.T) {
		t.Parallel()

		interceptor, err := NewUnaryServerInterceptor(failing(), staticKey("caller"), WithFailClosed())
		must.NoError(t, err)

		rpcErr, _ := invoke(t, interceptor)

		st, ok := status.FromError(rpcErr)
		must.True(t, ok)
		test.StrNotContains(t, st.Message(), "redis")
	})
}

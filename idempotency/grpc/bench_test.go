package grpc

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/idempotency"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func BenchmarkInterceptor(b *testing.B) {
	handler := func(context.Context, any) (any, error) { return str("ch_1"), nil }
	req := str("request")

	// The cost every non-participating call pays. It should be a metadata
	// lookup and nothing else.
	b.Run("NoKey", func(b *testing.B) {
		interceptor := newTestInterceptor(b)
		ctx := b.Context()

		for b.Loop() {
			_, _ = interceptor(ctx, req, info(), handler)
		}
	})

	// Replay pays a fingerprint, one store read, and the reconstruction of the
	// reply from the registry.
	b.Run("Replay", func(b *testing.B) {
		interceptor := newTestInterceptor(b)
		ctx := keyed(b.Context(), testKey)

		if _, err := interceptor(ctx, req, info(), handler); err != nil {
			b.Fatal(err)
		}

		for b.Loop() {
			_, _ = interceptor(ctx, req, info(), handler)
		}
	})

	// Execute is a first-time call: fingerprint, lock, claim, handler, record.
	b.Run("Execute", func(b *testing.B) {
		interceptor := newTestInterceptor(b)

		var i int
		for b.Loop() {
			i++
			ctx := keyed(b.Context(), strconv.Itoa(i))
			_, _ = interceptor(ctx, req, info(), handler)
		}
	})
}

// BenchmarkFingerprint prices the deterministic marshal plus hash against
// request size, since that is what scales with the call rather than the route.
func BenchmarkFingerprint(b *testing.B) {
	for _, size := range []int{1 << 10, 64 << 10, 1 << 20} {
		b.Run(strconv.Itoa(size>>10)+"KiB", func(b *testing.B) {
			req := wrapperspb.String(strings.Repeat("a", size))

			b.SetBytes(int64(size))
			for b.Loop() {
				_, _ = fingerprint(testMethod, "user-1", req)
			}
		})
	}
}

// BenchmarkClientInterceptor covers both client paths. Keyed pays a metadata
// append; unkeyed should pay almost nothing, since that is what every call
// from a caller who has not opted in costs.
func BenchmarkClientInterceptor(b *testing.B) {
	interceptor := NewUnaryClientInterceptor()

	// A plain function rather than recordingInvoker: that one accumulates a
	// context per call, which would measure a growing slice instead of the
	// interceptor.
	invoker := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return nil
	}

	b.Run("Keyed", func(b *testing.B) {
		ctx, _ := idempotency.WithNewKey(b.Context())

		for b.Loop() {
			_ = interceptor(ctx, testMethod, str("req"), str("res"), nil, invoker)
		}
	})

	b.Run("Unkeyed", func(b *testing.B) {
		ctx := b.Context()

		for b.Loop() {
			_ = interceptor(ctx, testMethod, str("req"), str("res"), nil, invoker)
		}
	})
}

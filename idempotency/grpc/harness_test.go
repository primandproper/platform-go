package grpc

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/primandproper/platform-go/v13/cache"
	cachememory "github.com/primandproper/platform-go/v13/cache/memory"
	cachemock "github.com/primandproper/platform-go/v13/cache/mock"
	"github.com/primandproper/platform-go/v13/distributedlock"
	dlmemory "github.com/primandproper/platform-go/v13/distributedlock/memory"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"

	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	testKey    = "d3f1a0c4-5b6e-4a2f-9c8d-1e2f3a4b5c6d"
	testMethod = "/test.Charges/Create"
)

// wrapperspb types stand in for generated messages. They are ordinary proto
// messages registered in the global registry at init, which is exactly what
// replay reconstruction depends on, so nothing here is special-cased.

func newTestManager(tb testing.TB, opts ...idempotency.Option) *idempotency.Manager[Response] {
	tb.Helper()

	store, err := cachememory.NewInMemoryCache[idempotency.Record[Response]](0)
	must.NoError(tb, err)

	locker, err := dlmemory.NewLocker()
	must.NoError(tb, err)

	scoped, err := distributedlock.NewScopedLocker(locker)
	must.NoError(tb, err)

	m, err := NewManager(store, scoped, opts...)
	must.NoError(tb, err)

	return m
}

func newTestInterceptor(tb testing.TB, opts ...Option) grpc.UnaryServerInterceptor {
	tb.Helper()

	return newInterceptorFor(tb, newTestManager(tb), opts...)
}

func newInterceptorFor(
	tb testing.TB,
	manager *idempotency.Manager[Response],
	opts ...Option,
) grpc.UnaryServerInterceptor {
	tb.Helper()

	i, err := NewUnaryServerInterceptor(manager, opts...)
	must.NoError(tb, err)

	return i
}

// keyed returns a context carrying key in incoming metadata, as a server sees
// it.
func keyed(ctx context.Context, key string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs(MetadataKey, key))
}

// countingHandler records how many times it ran and what it returns.
type countingHandler struct {
	reply any
	err   error
	calls atomic.Int64
}

func newCountingHandler(reply any) *countingHandler {
	return &countingHandler{reply: reply}
}

func (h *countingHandler) handle(context.Context, any) (any, error) {
	h.calls.Add(1)

	return h.reply, h.err
}

func (h *countingHandler) Calls() int64 { return h.calls.Load() }

// info is the server info a unary interceptor receives.
func info() *grpc.UnaryServerInfo {
	return infoFor(testMethod)
}

// infoFor builds server info for a specific method.
func infoFor(fullMethod string) *grpc.UnaryServerInfo {
	return &grpc.UnaryServerInfo{FullMethod: fullMethod}
}

// newFailingStoreManager builds a manager whose store cannot be read, for
// exercising the store failure policy.
func newFailingStoreManager(tb testing.TB, opts ...idempotency.Option) *idempotency.Manager[Response] {
	tb.Helper()

	store := &cachemock.CacheMock[idempotency.Record[Response]]{
		GetFunc: func(context.Context, string) (*idempotency.Record[Response], error) {
			return nil, platformerrors.New("store is down")
		},
		SetFunc: func(context.Context, string, *idempotency.Record[Response], ...cache.WriteOption) error {
			return nil
		},
		DeleteFunc: func(context.Context, string) error { return nil },
	}

	locker, err := dlmemory.NewLocker()
	must.NoError(tb, err)

	scoped, err := distributedlock.NewScopedLocker(locker)
	must.NoError(tb, err)

	m, err := NewManager(store, scoped, opts...)
	must.NoError(tb, err)

	return m
}

// str is shorthand for a request or reply message.
func str(value string) *wrapperspb.StringValue {
	return wrapperspb.String(value)
}

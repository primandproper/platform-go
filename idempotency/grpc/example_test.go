package grpc_test

import (
	"context"
	"fmt"

	cachememory "github.com/primandproper/platform-go/v13/cache/memory"
	"github.com/primandproper/platform-go/v13/distributedlock"
	dlmemory "github.com/primandproper/platform-go/v13/distributedlock/memory"
	"github.com/primandproper/platform-go/v13/idempotency"
	idempotencygrpc "github.com/primandproper/platform-go/v13/idempotency/grpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func newInterceptor() (grpc.UnaryServerInterceptor, error) {
	store, err := cachememory.NewInMemoryCache[idempotency.Record[idempotencygrpc.Response]](0)
	if err != nil {
		return nil, err
	}

	locker, err := dlmemory.NewLocker()
	if err != nil {
		return nil, err
	}

	scoped, err := distributedlock.NewScopedLocker(locker)
	if err != nil {
		return nil, err
	}

	// NewManager rather than idempotency.NewManager: it applies the rule that
	// a server-fault code is not recorded.
	manager, err := idempotencygrpc.NewManager(store, scoped)
	if err != nil {
		return nil, err
	}

	return idempotencygrpc.NewUnaryServerInterceptor(manager)
}

// ExampleNewUnaryServerInterceptor shows a retried call reaching the handler
// once. The metadata is set by hand here; in a real client the client
// interceptor does it.
func ExampleNewUnaryServerInterceptor() {
	interceptor, err := newInterceptor()
	if err != nil {
		panic(err)
	}

	charges := 0
	handler := func(context.Context, any) (any, error) {
		charges++

		return wrapperspb.String("ch_1"), nil
	}

	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs(idempotencygrpc.MetadataKey, "d3f1a0c4-5b6e-4a2f-9c8d-1e2f3a4b5c6d"),
	)

	info := &grpc.UnaryServerInfo{FullMethod: "/example.Charges/Create"}

	for range 2 {
		reply, replyErr := interceptor(ctx, wrapperspb.String("charge-10"), info, handler)
		if replyErr != nil {
			panic(replyErr)
		}

		msg, ok := reply.(*wrapperspb.StringValue)
		if !ok {
			panic("unexpected reply type")
		}

		fmt.Println("reply:", msg.GetValue())
	}

	fmt.Println("charges:", charges)

	// Output:
	// reply: ch_1
	// reply: ch_1
	// charges: 1
}

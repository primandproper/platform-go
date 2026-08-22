package grpc_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v13/ratelimiting"
	ratelimitinggrpc "github.com/primandproper/platform-go/v13/ratelimiting/grpc"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func ExampleNewUnaryServerInterceptor() {
	// One RPC per second, no burst, so the second in a row is refused and the
	// limiter can say when to come back.
	limiter, err := ratelimiting.NewInMemoryRateLimiter(1, 1)
	if err != nil {
		panic(err)
	}
	defer limiter.Close()

	interceptor, err := ratelimitinggrpc.NewUnaryServerInterceptor(limiter,
		ratelimitinggrpc.PerMethod(ratelimitinggrpc.KeyByMetadata("x-api-key")),
	)
	if err != nil {
		panic(err)
	}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-api-key", "sk_example"))
	info := &grpc.UnaryServerInfo{FullMethod: "/things.v1.Things/GetThing"}
	handler := func(context.Context, any) (any, error) { return "thing", nil }

	// call reports the RPC's code and whether the refusal told the caller when
	// to come back. The delay itself is a measurement, so it is not printed.
	call := func() (code string, hinted bool) {
		_, callErr := interceptor(ctx, "req", info, handler)
		if callErr == nil {
			return "OK", false
		}

		st, _ := status.FromError(callErr)
		for _, detail := range st.Details() {
			if _, ok := detail.(*errdetails.RetryInfo); ok {
				return st.Code().String(), true
			}
		}

		return st.Code().String(), false
	}

	fmt.Println(call())
	fmt.Println(call())

	// Output:
	// OK false
	// ResourceExhausted true
}

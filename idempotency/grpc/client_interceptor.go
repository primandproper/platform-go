package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v13/idempotency"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// NewUnaryClientInterceptor builds an interceptor that sends the idempotency
// key carried by a call's context.
//
//	conn, err := grpc.NewClient(target,
//		grpc.WithChainUnaryInterceptor(idempotencygrpc.NewUnaryClientInterceptor()),
//	)
//
//	ctx, _ := idempotency.WithNewKey(ctx)   // once, per logical operation
//	reply, err := client.CreateCharge(ctx, req)
//
// # It never invents a key
//
// With no key in the context this does nothing. An interceptor cannot tell a
// retry from a second, deliberate call, so minting one per invocation would
// give no protection while looking like it does, and deriving one from the
// request would silently swallow a genuine duplicate. Only the caller knows
// where a logical operation begins, which is what idempotency.WithNewKey
// expresses.
//
// # Built-in retries come along for free
//
// Client interceptors run above grpc-go's own retry policy, and the outgoing
// metadata stamped here is replayed on each transparent attempt. So one call
// to this interceptor covers the whole retry sequence — unlike HTTP, where
// retries happen above the transport and each attempt re-enters it.
func NewUnaryClientInterceptor(opts ...ClientOption) grpc.UnaryClientInterceptor {
	cfg := newClientConfig(opts...)

	return func(
		ctx context.Context,
		method string,
		req, reply any,
		conn *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		callOpts ...grpc.CallOption,
	) error {
		if cfg.methodFilter != nil && !cfg.methodFilter(method) {
			return invoker(ctx, method, req, reply, conn, callOpts...)
		}

		key, ok := idempotency.KeyFromContext(ctx)
		if !ok {
			return invoker(ctx, method, req, reply, conn, callOpts...)
		}

		// A key the caller already set wins: they are managing keys themselves
		// and should not be overridden.
		if md, mdOK := metadata.FromOutgoingContext(ctx); mdOK && len(md.Get(cfg.metadataKey)) > 0 {
			return invoker(ctx, method, req, reply, conn, callOpts...)
		}

		return invoker(
			metadata.AppendToOutgoingContext(ctx, cfg.metadataKey, string(key)),
			method, req, reply, conn, callOpts...,
		)
	}
}

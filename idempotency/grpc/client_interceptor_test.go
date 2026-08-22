package grpc

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// recordingInvoker captures the context each call was invoked with, which is
// where the outgoing metadata lives.
type recordingInvoker struct {
	err   error
	seen  []context.Context
	calls atomic.Int64
}

func (r *recordingInvoker) invoke(
	ctx context.Context,
	_ string,
	_, _ any,
	_ *grpc.ClientConn,
	_ ...grpc.CallOption,
) error {
	r.calls.Add(1)
	r.seen = append(r.seen, ctx)

	return r.err
}

// sentKey reads the key from the nth call's outgoing metadata.
func (r *recordingInvoker) sentKey(n int) string {
	md, ok := metadata.FromOutgoingContext(r.seen[n])
	if !ok {
		return ""
	}

	values := md.Get(MetadataKey)
	if len(values) == 0 {
		return ""
	}

	return values[0]
}

func TestClientInterceptor(T *testing.T) {
	T.Parallel()

	T.Run("stamps the key carried by the context", func(t *testing.T) {
		t.Parallel()

		invoker := &recordingInvoker{}
		ctx, key := idempotency.WithNewKey(t.Context())

		err := NewUnaryClientInterceptor()(ctx, testMethod, str("req"), str("res"), nil, invoker.invoke)
		must.NoError(t, err)

		test.EqOp(t, string(key), invoker.sentKey(0))
	})

	// The safety rule: an interceptor cannot tell a retry from a deliberate
	// second call, so inventing a key would look like protection and give none.
	T.Run("stamps nothing when the context carries no key", func(t *testing.T) {
		t.Parallel()

		invoker := &recordingInvoker{}

		err := NewUnaryClientInterceptor()(t.Context(), testMethod, str("req"), str("res"), nil, invoker.invoke)
		must.NoError(t, err)

		test.EqOp(t, "", invoker.sentKey(0))
	})

	T.Run("never overwrites a key the caller set", func(t *testing.T) {
		t.Parallel()

		invoker := &recordingInvoker{}
		ctx, _ := idempotency.WithNewKey(t.Context())
		ctx = metadata.AppendToOutgoingContext(ctx, MetadataKey, "caller-owned")

		err := NewUnaryClientInterceptor()(ctx, testMethod, str("req"), str("res"), nil, invoker.invoke)
		must.NoError(t, err)

		test.EqOp(t, "caller-owned", invoker.sentKey(0))
	})

	T.Run("keeps unrelated outgoing metadata", func(t *testing.T) {
		t.Parallel()

		invoker := &recordingInvoker{}
		ctx, key := idempotency.WithNewKey(t.Context())
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "bearer abc")

		err := NewUnaryClientInterceptor()(ctx, testMethod, str("req"), str("res"), nil, invoker.invoke)
		must.NoError(t, err)

		md, ok := metadata.FromOutgoingContext(invoker.seen[0])
		must.True(t, ok)
		test.EqOp(t, string(key), md.Get(MetadataKey)[0])
		test.EqOp(t, "bearer abc", md.Get("authorization")[0])
	})

	T.Run("every attempt under one context sends the same key", func(t *testing.T) {
		t.Parallel()

		invoker := &recordingInvoker{}
		interceptor := NewUnaryClientInterceptor()
		ctx, key := idempotency.WithNewKey(t.Context())

		for range 3 {
			must.NoError(t, interceptor(ctx, testMethod, str("req"), str("res"), nil, invoker.invoke))
		}

		must.SliceLen(t, 3, invoker.seen)
		for i := range invoker.seen {
			test.EqOp(t, string(key), invoker.sentKey(i))
		}
	})

	T.Run("a filtered-out method is left alone", func(t *testing.T) {
		t.Parallel()

		invoker := &recordingInvoker{}
		ctx, _ := idempotency.WithNewKey(t.Context())

		interceptor := NewUnaryClientInterceptor(WithClientMethodFilter(func(m string) bool {
			return strings.HasSuffix(m, "/Delete")
		}))

		must.NoError(t, interceptor(ctx, testMethod, str("req"), str("res"), nil, invoker.invoke))

		test.EqOp(t, "", invoker.sentKey(0))
	})

	T.Run("options override the metadata key", func(t *testing.T) {
		t.Parallel()

		invoker := &recordingInvoker{}
		ctx, key := idempotency.WithNewKey(t.Context())

		interceptor := NewUnaryClientInterceptor(WithClientMetadataKey("x-idem"))
		must.NoError(t, interceptor(ctx, testMethod, str("req"), str("res"), nil, invoker.invoke))

		md, ok := metadata.FromOutgoingContext(invoker.seen[0])
		must.True(t, ok)
		test.EqOp(t, string(key), md.Get("x-idem")[0])
	})

	T.Run("calls the invoker once and passes its error through", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("connection refused")
		invoker := &recordingInvoker{err: boom}
		ctx, _ := idempotency.WithNewKey(t.Context())

		err := NewUnaryClientInterceptor()(ctx, testMethod, str("req"), str("res"), nil, invoker.invoke)

		test.ErrorIs(t, err, boom)
		test.EqOp(t, int64(1), invoker.calls.Load())
	})

	T.Run("ignores nil options", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, NewUnaryClientInterceptor(nil))
	})
}

// TestClientServerHandoff is the test neither side can do alone: it feeds what
// the client interceptor put on the wire straight into the server interceptor,
// so a disagreement about the metadata key fails here rather than in
// production.
func TestClientServerHandoff(T *testing.T) {
	T.Parallel()

	// asIncoming converts a client's outgoing metadata into the incoming
	// metadata a server sees, which is what gRPC's transport does between them.
	asIncoming := func(ctx context.Context) context.Context {
		md, ok := metadata.FromOutgoingContext(ctx)
		must.True(T, ok)

		return metadata.NewIncomingContext(T.Context(), md)
	}

	T.Run("a retry through both halves runs the handler once", func(t *testing.T) {
		t.Parallel()

		var (
			invoker = &recordingInvoker{}
			client  = NewUnaryClientInterceptor()
			server  = newTestInterceptor(t)
			handler = newCountingHandler(str("ch_1"))
		)

		ctx, _ := idempotency.WithNewKey(t.Context())

		var last any
		for range 3 {
			// Client side: stamp the key.
			must.NoError(t, client(ctx, testMethod, str("req"), str("res"), nil, invoker.invoke))

			// Server side: read it back off the wire.
			reply, err := server(asIncoming(invoker.seen[len(invoker.seen)-1]), str("req"), info(), handler.handle)
			must.NoError(t, err)
			last = reply
		}

		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, "ch_1", last.(*wrapperspb.StringValue).Value)
	})

	// Without a minted key the client stamps nothing, so the server has nothing
	// to deduplicate on. This is the failure mode a caller creates by minting
	// inside their retry loop, and no library can detect it for them.
	T.Run("without a minted key every attempt runs", func(t *testing.T) {
		t.Parallel()

		var (
			invoker = &recordingInvoker{}
			client  = NewUnaryClientInterceptor()
			server  = newTestInterceptor(t)
			handler = newCountingHandler(str("ch_1"))
		)

		for range 3 {
			must.NoError(t, client(t.Context(), testMethod, str("req"), str("res"), nil, invoker.invoke))

			ctx := t.Context()
			if md, ok := metadata.FromOutgoingContext(invoker.seen[len(invoker.seen)-1]); ok {
				ctx = metadata.NewIncomingContext(ctx, md)
			}

			_, err := server(ctx, str("req"), info(), handler.handle)
			must.NoError(t, err)
		}

		test.EqOp(t, int64(3), handler.Calls())
	})

	T.Run("a second, different request under one key is refused", func(t *testing.T) {
		t.Parallel()

		var (
			invoker = &recordingInvoker{}
			client  = NewUnaryClientInterceptor()
			server  = newTestInterceptor(t)
			handler = newCountingHandler(str("ch_1"))
		)

		ctx, _ := idempotency.WithNewKey(t.Context())

		must.NoError(t, client(ctx, testMethod, str("charge-10"), str("res"), nil, invoker.invoke))
		_, err := server(asIncoming(invoker.seen[0]), str("charge-10"), info(), handler.handle)
		must.NoError(t, err)

		must.NoError(t, client(ctx, testMethod, str("charge-1000"), str("res"), nil, invoker.invoke))
		_, err = server(asIncoming(invoker.seen[1]), str("charge-1000"), info(), handler.handle)

		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

package grpc

import (
	"context"
	stderrors "errors"
	"strings"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v13/cache"
	cachemock "github.com/primandproper/platform-go/v13/cache/mock"
	"github.com/primandproper/platform-go/v13/distributedlock"
	dlmemory "github.com/primandproper/platform-go/v13/distributedlock/memory"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestNewUnaryServerInterceptor(T *testing.T) {
	T.Parallel()

	T.Run("rejects a nil manager", func(t *testing.T) {
		t.Parallel()

		_, err := NewUnaryServerInterceptor(nil)
		test.ErrorIs(t, err, ErrNilManager)
	})
}

func TestInterceptor_PassThrough(T *testing.T) {
	T.Parallel()

	T.Run("a call without the key is untouched", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(str("ok"))
		interceptor := newTestInterceptor(t)

		for range 2 {
			_, err := interceptor(t.Context(), str("req"), info(), handler.handle)
			must.NoError(t, err)
		}

		test.EqOp(t, int64(2), handler.Calls())
	})

	T.Run("a filtered-out method is untouched", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(str("ok"))
		interceptor := newTestInterceptor(t, WithMethodFilter(func(m string) bool {
			return strings.HasSuffix(m, "/Delete")
		}))

		ctx := keyed(t.Context(), testKey)
		for range 2 {
			_, err := interceptor(ctx, str("req"), info(), handler.handle)
			must.NoError(t, err)
		}

		test.EqOp(t, int64(2), handler.Calls())
	})

	// grpc-go permits non-proto codecs. Such a call cannot be fingerprinted,
	// and refusing it would break a service that never asked for any of this.
	T.Run("a non-proto request runs unguarded", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler("plain string reply")
		interceptor := newTestInterceptor(t)

		ctx := keyed(t.Context(), testKey)
		for range 2 {
			_, err := interceptor(ctx, "plain string request", info(), handler.handle)
			must.NoError(t, err)
		}

		test.EqOp(t, int64(2), handler.Calls())
	})

	T.Run("a non-proto reply runs unguarded", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler("plain string reply")
		interceptor := newTestInterceptor(t)

		ctx := keyed(t.Context(), testKey)
		reply, err := interceptor(ctx, str("req"), info(), handler.handle)

		must.NoError(t, err)
		test.EqOp(t, "plain string reply", reply)
	})
}

func TestInterceptor_Replay(T *testing.T) {
	T.Parallel()

	T.Run("replays the reply without running the handler", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(str("ch_1"))
		interceptor := newTestInterceptor(t)
		ctx := keyed(t.Context(), testKey)

		first, err := interceptor(ctx, str("req"), info(), handler.handle)
		must.NoError(t, err)

		second, err := interceptor(ctx, str("req"), info(), handler.handle)
		must.NoError(t, err)

		test.EqOp(t, int64(1), handler.Calls())

		// Rebuilt from the registry, so it is an equal message rather than the
		// same pointer.
		firstMsg, ok := first.(proto.Message)
		must.True(t, ok)
		secondMsg, ok := second.(proto.Message)
		must.True(t, ok)

		test.True(t, proto.Equal(firstMsg, secondMsg))
		test.EqOp(t, "ch_1", secondMsg.(*wrapperspb.StringValue).Value)
	})

	T.Run("replays a client-fault error", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(nil)
		handler.err = status.Error(codes.InvalidArgument, "bad amount")
		interceptor := newTestInterceptor(t)
		ctx := keyed(t.Context(), testKey)

		_, err := interceptor(ctx, str("req"), info(), handler.handle)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		_, err = interceptor(ctx, str("req"), info(), handler.handle)

		// Stable answer, so it replays rather than re-running.
		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
		test.EqOp(t, "bad amount", status.Convert(err).Message())
	})

	// A server-fault code usually means the work never landed, so pinning it
	// for the whole TTL would leave the caller unable to ever succeed.
	T.Run("does not record a server-fault error", func(t *testing.T) {
		t.Parallel()

		var mu sync.Mutex
		failing := true

		handler := &countingHandler{}
		interceptor := newTestInterceptor(t)
		ctx := keyed(t.Context(), testKey)

		handle := func(hctx context.Context, req any) (any, error) {
			mu.Lock()
			shouldFail := failing
			mu.Unlock()

			handler.calls.Add(1)
			if shouldFail {
				return nil, status.Error(codes.Unavailable, "downstream down")
			}

			return str("ch_1"), nil
		}

		_, err := interceptor(ctx, str("req"), info(), handle)
		test.EqOp(t, codes.Unavailable, status.Code(err))

		mu.Lock()
		failing = false
		mu.Unlock()

		reply, err := interceptor(ctx, str("req"), info(), handle)
		must.NoError(t, err)
		test.EqOp(t, int64(2), handler.Calls())
		test.EqOp(t, "ch_1", reply.(*wrapperspb.StringValue).Value)
	})
}

func TestInterceptor_Conflict(T *testing.T) {
	T.Parallel()

	T.Run("answers Aborted while the handler is still running", func(t *testing.T) {
		t.Parallel()

		var (
			started = make(chan struct{})
			release = make(chan struct{})
			once    sync.Once
		)

		interceptor := newTestInterceptor(t)
		ctx := keyed(t.Context(), testKey)

		handle := func(context.Context, any) (any, error) {
			once.Do(func() { close(started) })
			<-release

			return str("ch_1"), nil
		}

		go func() {
			_, _ = interceptor(ctx, str("req"), info(), handle)
		}()

		<-started

		_, err := interceptor(ctx, str("req"), info(), handle)
		close(release)

		// Aborted is gRPC's concurrency-conflict code, and its documented
		// advice — retry at a higher level — is right here.
		test.EqOp(t, codes.Aborted, status.Code(err))
	})
}

func TestInterceptor_Mismatch(T *testing.T) {
	T.Parallel()

	T.Run("a different request is InvalidArgument", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(str("ch_1"))
		interceptor := newTestInterceptor(t)
		ctx := keyed(t.Context(), testKey)

		_, err := interceptor(ctx, str("charge-10"), info(), handler.handle)
		must.NoError(t, err)

		_, err = interceptor(ctx, str("charge-1000"), info(), handler.handle)

		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("a different method is InvalidArgument", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(str("ch_1"))
		interceptor := newTestInterceptor(t)
		ctx := keyed(t.Context(), testKey)

		_, err := interceptor(ctx, str("req"), info(), handler.handle)
		must.NoError(t, err)

		_, err = interceptor(ctx, str("req"), infoFor("/test.Charges/Refund"), handler.handle)

		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("a different principal is InvalidArgument", func(t *testing.T) {
		t.Parallel()

		type userKey struct{}

		handler := newCountingHandler(str("ch_1"))
		interceptor := newTestInterceptor(t, WithPrincipalExtractor(func(ctx context.Context) (string, error) {
			user, _ := ctx.Value(userKey{}).(string)

			return user, nil
		}))

		alice := context.WithValue(keyed(t.Context(), testKey), userKey{}, "alice")
		bob := context.WithValue(keyed(t.Context(), testKey), userKey{}, "bob")

		_, err := interceptor(alice, str("req"), info(), handler.handle)
		must.NoError(t, err)

		// Without the principal in the fingerprint bob would be handed alice's
		// reply.
		_, err = interceptor(bob, str("req"), info(), handler.handle)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	// Map fields serialize in a random order unless marshaling is
	// deterministic, so without that an ordinary retry would look like reuse.
	T.Run("a message with map fields is stable across attempts", func(t *testing.T) {
		t.Parallel()

		build := func() *structpb.Struct {
			s, err := structpb.NewStruct(map[string]any{
				"a": "1", "b": "2", "c": "3", "d": "4", "e": "5",
				"f": "6", "g": "7", "h": "8", "i": "9", "j": "10",
			})
			must.NoError(t, err)

			return s
		}

		handler := newCountingHandler(str("ch_1"))
		interceptor := newTestInterceptor(t)
		ctx := keyed(t.Context(), testKey)

		_, err := interceptor(ctx, build(), info(), handler.handle)
		must.NoError(t, err)

		for range 20 {
			_, err = interceptor(ctx, build(), info(), handler.handle)
			must.NoError(t, err)
		}

		test.EqOp(t, int64(1), handler.Calls())
	})
}

func TestInterceptor_Truncation(T *testing.T) {
	T.Parallel()

	// The call is known to have succeeded, so re-running it is not an option.
	// Reporting the reply as gone preserves the guarantee and is honest.
	T.Run("records an over-sized reply and refuses to replay it", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(str(strings.Repeat("a", 512)))
		interceptor := newTestInterceptor(t, WithMaxResponseBytes(16))
		ctx := keyed(t.Context(), testKey)

		first, err := interceptor(ctx, str("req"), info(), handler.handle)
		must.NoError(t, err)
		test.EqOp(t, 512, len(first.(*wrapperspb.StringValue).Value))

		_, err = interceptor(ctx, str("req"), info(), handler.handle)

		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, codes.ResourceExhausted, status.Code(err))
	})
}

func TestInterceptor_KeyValidation(T *testing.T) {
	T.Parallel()

	T.Run("an invalid key is InvalidArgument", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(str("ch_1"))
		interceptor := newTestInterceptor(t)

		for _, key := range []string{strings.Repeat("k", 300), "has space"} {
			_, err := interceptor(keyed(t.Context(), key), str("req"), info(), handler.handle)
			test.EqOp(t, codes.InvalidArgument, status.Code(err))
		}

		test.EqOp(t, int64(0), handler.Calls())
	})
}

func TestInterceptor_StoreFailure(T *testing.T) {
	T.Parallel()

	T.Run("fails closed without running the handler", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(str("ch_1"))
		interceptor := newInterceptorFor(t, newFailingStoreManager(t))

		_, err := interceptor(keyed(t.Context(), testKey), str("req"), info(), handler.handle)

		test.EqOp(t, codes.Internal, status.Code(err))
		test.EqOp(t, int64(0), handler.Calls())
	})

	T.Run("fails open by running the handler when configured to", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(str("ch_1"))
		interceptor := newInterceptorFor(t, newFailingStoreManager(t,
			idempotency.WithStoreFailurePolicy(idempotency.FailOpen),
		))

		reply, err := interceptor(keyed(t.Context(), testKey), str("req"), info(), handler.handle)

		must.NoError(t, err)
		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, "ch_1", reply.(*wrapperspb.StringValue).Value)
	})
}

func TestRecordable(T *testing.T) {
	T.Parallel()

	recorded := []codes.Code{
		codes.OK, codes.InvalidArgument, codes.NotFound, codes.AlreadyExists,
		codes.PermissionDenied, codes.Unauthenticated, codes.FailedPrecondition, codes.OutOfRange,
	}
	refused := []codes.Code{
		codes.Internal, codes.Unavailable, codes.Unknown, codes.DeadlineExceeded,
		codes.ResourceExhausted, codes.Aborted, codes.DataLoss, codes.Canceled, codes.Unimplemented,
	}

	T.Run("records client-fault outcomes", func(t *testing.T) {
		t.Parallel()

		for _, code := range recorded {
			test.True(t, Recordable(&Response{StatusCode: uint32(code)}))
		}
	})

	T.Run("refuses server-fault outcomes", func(t *testing.T) {
		t.Parallel()

		for _, code := range refused {
			test.False(t, Recordable(&Response{StatusCode: uint32(code)}))
		}
	})
}

// badUTF8 is a message proto3 refuses to marshal: its string field is not
// valid UTF-8. It stands in for any message that cannot be serialized.
func badUTF8() *wrapperspb.StringValue {
	return wrapperspb.String("\xff\xfe")
}

func TestFingerprint(T *testing.T) {
	T.Parallel()

	T.Run("is stable for the same call", func(t *testing.T) {
		t.Parallel()

		first, err := fingerprint(testMethod, "alice", str("req"))
		must.NoError(t, err)

		second, err := fingerprint(testMethod, "alice", str("req"))
		must.NoError(t, err)

		test.EqOp(t, first, second)
	})

	// Length-prefixing each part is what stops them running together: without
	// it these two calls would hash identically.
	T.Run("does not confuse adjacent parts", func(t *testing.T) {
		t.Parallel()

		first, err := fingerprint("/a", "bc", str("req"))
		must.NoError(t, err)

		second, err := fingerprint("/ab", "c", str("req"))
		must.NoError(t, err)

		test.NotEqOp(t, first, second)
	})

	T.Run("surfaces a marshaling failure", func(t *testing.T) {
		t.Parallel()

		_, err := fingerprint(testMethod, "", badUTF8())
		test.Error(t, err)
	})
}

func TestRecord(T *testing.T) {
	T.Parallel()

	T.Run("surfaces a marshaling failure", func(t *testing.T) {
		t.Parallel()

		_, err := record(badUTF8(), nil, 0)
		test.Error(t, err)
	})
}

func TestReplay(T *testing.T) {
	T.Parallel()

	// A reply whose type this binary cannot find cannot be rebuilt. That
	// happens when a record outlives the deploy that wrote it.
	T.Run("reports an unknown message type", func(t *testing.T) {
		t.Parallel()

		_, err := replay(&Response{MessageName: "not.A.Real.Message", Payload: []byte{}})
		test.ErrorIs(t, err, ErrUnknownMessageType)
	})

	T.Run("reports a corrupt payload", func(t *testing.T) {
		t.Parallel()

		_, err := replay(&Response{
			MessageName: string(str("").ProtoReflect().Descriptor().FullName()),
			Payload:     []byte{0xff, 0xff, 0xff},
		})
		test.Error(t, err)
		test.False(t, stderrors.Is(err, ErrUnknownMessageType))
	})
}

func TestInterceptor_ReplayFailure(T *testing.T) {
	T.Parallel()

	// A record that cannot be rebuilt must not fall back to running the
	// handler: the work already happened, and re-running it is the duplicate
	// this package exists to prevent.
	T.Run("an unrebuildable record is Internal, not a re-run", func(t *testing.T) {
		t.Parallel()

		store := &cachemock.CacheMock[idempotency.Record[Response]]{
			GetFunc: func(context.Context, string) (*idempotency.Record[Response], error) {
				fp, fpErr := fingerprint(testMethod, "", str("req"))
				must.NoError(t, fpErr)

				return &idempotency.Record[Response]{
					Fingerprint: fp,
					ClaimID:     "seeded",
					Version:     1,
					State:       idempotency.StateCompleted,
					Value:       &Response{MessageName: "not.A.Real.Message"},
				}, nil
			},
			SetFunc: func(context.Context, string, *idempotency.Record[Response], ...cache.WriteOption) error {
				return nil
			},
			DeleteFunc: func(context.Context, string) error { return nil },
		}

		locker, err := dlmemory.NewLocker()
		must.NoError(t, err)

		scoped, err := distributedlock.NewScopedLocker(locker)
		must.NoError(t, err)

		manager, err := NewManager(store, scoped)
		must.NoError(t, err)

		handler := newCountingHandler(str("ch_1"))
		interceptor := newInterceptorFor(t, manager)

		_, err = interceptor(keyed(t.Context(), testKey), str("req"), info(), handler.handle)

		test.EqOp(t, codes.Internal, status.Code(err))
		test.EqOp(t, int64(0), handler.Calls())
	})
}

func TestInterceptor_Options(T *testing.T) {
	T.Parallel()

	T.Run("WithMetadataKey changes the entry read", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(str("ch_1"))
		interceptor := newTestInterceptor(t, WithMetadataKey("x-idem"))

		// The default entry no longer participates.
		for range 2 {
			_, err := interceptor(keyed(t.Context(), testKey), str("req"), info(), handler.handle)
			must.NoError(t, err)
		}
		test.EqOp(t, int64(2), handler.Calls())

		ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("x-idem", testKey))
		for range 2 {
			_, err := interceptor(ctx, str("req"), info(), handler.handle)
			must.NoError(t, err)
		}
		test.EqOp(t, int64(3), handler.Calls())
	})

	// Metadata carrying the key with an empty value is the same as no key: an
	// empty string would be a single shared record for every caller.
	T.Run("an empty key value is treated as absent", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(str("ch_1"))
		interceptor := newTestInterceptor(t)
		ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(MetadataKey, ""))

		for range 2 {
			_, err := interceptor(ctx, str("req"), info(), handler.handle)
			must.NoError(t, err)
		}

		test.EqOp(t, int64(2), handler.Calls())
	})

	T.Run("a principal extractor failure does not run the handler", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(str("ch_1"))
		interceptor := newTestInterceptor(t, WithPrincipalExtractor(func(context.Context) (string, error) {
			return "", platformerrors.New("no principal")
		}))

		_, err := interceptor(keyed(t.Context(), testKey), str("req"), info(), handler.handle)

		test.Error(t, err)
		test.EqOp(t, int64(0), handler.Calls())
	})

	// A request that cannot be marshaled is not the same as one that is not a
	// proto message: the latter runs unguarded, this one is refused.
	T.Run("an unmarshalable request is refused", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(str("ch_1"))
		interceptor := newTestInterceptor(t)

		_, err := interceptor(keyed(t.Context(), testKey), badUTF8(), info(), handler.handle)

		test.Error(t, err)
		test.EqOp(t, int64(0), handler.Calls())
	})

	T.Run("accepts observability options", func(t *testing.T) {
		t.Parallel()

		interceptor := newTestInterceptor(t,
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metrics.EnsureMetricsProvider(nil)),
			nil,
		)

		handler := newCountingHandler(str("ch_1"))
		_, err := interceptor(keyed(t.Context(), testKey), str("req"), info(), handler.handle)

		must.NoError(t, err)
	})

	T.Run("surfaces a failure to build an instrument", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("no meter")

		for _, failing := range []string{
			"idempotency_grpc_unsupported_calls",
			"idempotency_grpc_replies_truncated",
		} {
			provider := &metricsmock.ProviderMock{
				NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
					if name == failing {
						return nil, boom
					}

					return nil, nil
				},
			}

			_, err := NewUnaryServerInterceptor(newTestManager(t), WithMetricsProvider(provider))
			test.ErrorIs(t, err, boom, test.Sprintf("building %s", failing))
		}
	})
}

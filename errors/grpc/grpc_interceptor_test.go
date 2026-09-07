package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v14/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestDecodeErrorFromStatus(T *testing.T) {
	T.Parallel()

	T.Run("nil error returns nil", func(t *testing.T) {
		t.Parallel()
		test.Nil(t, DecodeErrorFromStatus(context.Background(), nil))
	})

	T.Run("non-status error returned as-is", func(t *testing.T) {
		t.Parallel()
		original := errors.New("plain error")
		result := DecodeErrorFromStatus(context.Background(), original)
		test.ErrorIs(t, result, original)
	})

	T.Run("status error without details returns original", func(t *testing.T) {
		t.Parallel()
		st := status.New(codes.NotFound, "not found")
		err := st.Err()
		result := DecodeErrorFromStatus(context.Background(), err)
		test.Error(t, result)
	})

	T.Run("round-trips a platform sentinel error through encode/decode", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		original := platformerrors.ErrNilInputParameter

		// Encode using the interceptor helper
		detail := encodeErrorToDetails(ctx, original)
		must.NotNil(t, detail)

		// Build a status with details
		st := status.New(codes.InvalidArgument, original.Error())
		stWithDetails, err := st.WithDetails(detail)
		must.NoError(t, err)

		// Decode - the decoded error should contain the original message
		decoded := DecodeErrorFromStatus(ctx, stWithDetails.Err())
		must.Error(t, decoded)
		test.StrContains(t, decoded.Error(), "nil")
	})
}

func TestEncodeErrorToDetails(T *testing.T) {
	T.Parallel()

	T.Run("encodes a platform error", func(t *testing.T) {
		t.Parallel()
		detail := encodeErrorToDetails(context.Background(), platformerrors.ErrNilInputParameter)
		test.NotNil(t, detail)
		test.EqOp(t, encodedErrorTypeURL, detail.TypeUrl)
	})

	T.Run("encodes a wrapped error", func(t *testing.T) {
		t.Parallel()
		wrapped := platformerrors.Wrap(platformerrors.ErrInvalidIDProvided, "context")
		detail := encodeErrorToDetails(context.Background(), wrapped)
		test.NotNil(t, detail)
	})

	T.Run("encodes a simple error", func(t *testing.T) {
		t.Parallel()
		detail := encodeErrorToDetails(context.Background(), errors.New("simple"))
		// Even simple errors should encode (cockroachdb/errors handles them)
		test.NotNil(t, detail)
	})
}

func TestUnaryErrorEncodingInterceptor(T *testing.T) {
	T.Parallel()

	T.Run("returns response when handler succeeds", func(t *testing.T) {
		t.Parallel()

		interceptor := UnaryErrorEncodingInterceptor()
		handler := func(ctx context.Context, req any) (any, error) {
			return "ok", nil
		}

		resp, err := interceptor(context.Background(), "req", &grpc.UnaryServerInfo{}, handler)
		test.NoError(t, err)
		test.Eq[any](t, "ok", resp)
	})

	T.Run("encodes platform error into status details", func(t *testing.T) {
		t.Parallel()

		interceptor := UnaryErrorEncodingInterceptor()
		handler := func(ctx context.Context, req any) (any, error) {
			return nil, platformerrors.ErrNilInputParameter
		}

		resp, err := interceptor(context.Background(), "req", &grpc.UnaryServerInfo{}, handler)
		test.Nil(t, resp)
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, codes.InvalidArgument, st.Code())
		test.SliceNotEmpty(t, st.Details())
	})

	T.Run("preserves existing status code for known errors", func(t *testing.T) {
		t.Parallel()

		interceptor := UnaryErrorEncodingInterceptor()
		handler := func(ctx context.Context, req any) (any, error) {
			return nil, sql.ErrNoRows
		}

		_, err := interceptor(context.Background(), "req", &grpc.UnaryServerInfo{}, handler)
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, codes.NotFound, st.Code())
	})

	T.Run("handler returning status error preserves message", func(t *testing.T) {
		t.Parallel()

		interceptor := UnaryErrorEncodingInterceptor()
		handler := func(ctx context.Context, req any) (any, error) {
			return nil, status.Error(codes.FailedPrecondition, "custom message")
		}

		_, err := interceptor(context.Background(), "req", &grpc.UnaryServerInfo{}, handler)
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, "custom message", st.Message())
	})

	T.Run("a wrapped status keeps the handler's message, not the chain", func(t *testing.T) {
		t.Parallel()

		// A consumer interceptor between the handler and this one that wraps
		// with %w is the realistic case. status.FromError on that wrapper
		// rebuilds the status with err.Error() as the message, which would put
		// "outer: rpc error: code = ..." on the wire; the handler chose "chosen".
		interceptor := UnaryErrorEncodingInterceptor()
		handler := func(ctx context.Context, req any) (any, error) {
			return nil, fmt.Errorf("outer: %w", status.Error(codes.FailedPrecondition, "chosen"))
		}

		_, err := interceptor(context.Background(), "req", &grpc.UnaryServerInfo{}, handler)
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, codes.FailedPrecondition, st.Code())
		test.EqOp(t, "chosen", st.Message())
	})

	T.Run("unknown error uses codes.Unknown", func(t *testing.T) {
		t.Parallel()

		interceptor := UnaryErrorEncodingInterceptor()
		handler := func(ctx context.Context, req any) (any, error) {
			return nil, errors.New("something unexpected")
		}

		_, err := interceptor(context.Background(), "req", &grpc.UnaryServerInfo{}, handler)
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, codes.Unknown, st.Code())
	})
}

// mockServerStream implements grpc.ServerStream for testing.
type mockServerStream struct {
	ctx context.Context
}

func (m *mockServerStream) SetHeader(metadata.MD) error  { return nil }
func (m *mockServerStream) SendHeader(metadata.MD) error { return nil }
func (m *mockServerStream) SetTrailer(metadata.MD)       {}
func (m *mockServerStream) Context() context.Context     { return m.ctx }
func (m *mockServerStream) SendMsg(any) error            { return nil }
func (m *mockServerStream) RecvMsg(any) error            { return nil }

func TestStreamErrorEncodingInterceptor(T *testing.T) {
	T.Parallel()

	T.Run("returns nil when handler succeeds", func(t *testing.T) {
		t.Parallel()

		interceptor := StreamErrorEncodingInterceptor()
		handler := func(srv any, stream grpc.ServerStream) error {
			return nil
		}

		ss := &mockServerStream{ctx: context.Background()}
		err := interceptor(nil, ss, &grpc.StreamServerInfo{}, handler)
		test.NoError(t, err)
	})

	T.Run("encodes platform error into status details", func(t *testing.T) {
		t.Parallel()

		interceptor := StreamErrorEncodingInterceptor()
		handler := func(srv any, stream grpc.ServerStream) error {
			return platformerrors.ErrInvalidIDProvided
		}

		ss := &mockServerStream{ctx: context.Background()}
		err := interceptor(nil, ss, &grpc.StreamServerInfo{}, handler)
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, codes.InvalidArgument, st.Code())
		test.SliceNotEmpty(t, st.Details())
	})

	T.Run("unknown error uses codes.Unknown", func(t *testing.T) {
		t.Parallel()

		interceptor := StreamErrorEncodingInterceptor()
		handler := func(srv any, stream grpc.ServerStream) error {
			return errors.New("stream failure")
		}

		ss := &mockServerStream{ctx: context.Background()}
		err := interceptor(nil, ss, &grpc.StreamServerInfo{}, handler)
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, codes.Unknown, st.Code())
	})

	T.Run("handler returning status error preserves message", func(t *testing.T) {
		t.Parallel()

		interceptor := StreamErrorEncodingInterceptor()
		handler := func(srv any, stream grpc.ServerStream) error {
			return status.Error(codes.Unauthenticated, "not authed")
		}

		ss := &mockServerStream{ctx: context.Background()}
		err := interceptor(nil, ss, &grpc.StreamServerInfo{}, handler)
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, "not authed", st.Message())
	})

	T.Run("a wrapped status keeps the handler's message, not the chain", func(t *testing.T) {
		t.Parallel()

		interceptor := StreamErrorEncodingInterceptor()
		handler := func(srv any, stream grpc.ServerStream) error {
			return fmt.Errorf("outer: %w", status.Error(codes.FailedPrecondition, "chosen"))
		}

		ss := &mockServerStream{ctx: context.Background()}
		err := interceptor(nil, ss, &grpc.StreamServerInfo{}, handler)
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, codes.FailedPrecondition, st.Code())
		test.EqOp(t, "chosen", st.Message())
	})
}

func TestClientMessage_registeredSentinels(T *testing.T) {
	T.Parallel()

	// The domain half of the client-safe list is registered rather than
	// imported, because this package is a primitive and the packages whose
	// wording is meant for a person — links, whose four redemption outcomes
	// exist precisely so a client is told which one happened — are built on it.
	// A sentinel nobody registered gets the code's name, which is the whole
	// point of registering one.
	safe := platformerrors.New("this link has expired")
	unsafe := platformerrors.New("update users set x = 1 where tenant = 'acme'")

	RegisterClientSafeSentinels(safe)

	T.Run("a registered sentinel speaks for itself", func(t *testing.T) {
		t.Parallel()

		// Wrapped, because that is how one arrives from a handler.
		msg := clientMessage(codes.FailedPrecondition, platformerrors.Wrap(safe, "redeeming action link"), "")

		test.EqOp(t, safe.Error(), msg)
		test.NotEqOp(t, codes.FailedPrecondition.String(), msg)
	})

	T.Run("an unregistered one gets the code's name", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, codes.Internal.String(), clientMessage(codes.Internal, unsafe, ""))
	})

	T.Run("a handler's description stands in for the code's name", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "redeeming action link", clientMessage(codes.Internal, unsafe, "redeeming action link"))
	})

	T.Run("a registered sentinel outranks the description", func(t *testing.T) {
		t.Parallel()

		msg := clientMessage(codes.FailedPrecondition, platformerrors.Wrap(safe, "redeeming action link"), "redeeming action link")

		test.EqOp(t, safe.Error(), msg)
	})

	T.Run("the exported lookup says which it was", func(t *testing.T) {
		t.Parallel()

		// A handler shaping its own status asks this rather than the code, so
		// it has to be able to tell "the sentinel's words" from "no answer".
		msg, ok := ClientSafeMessage(platformerrors.Wrap(safe, "redeeming action link"))
		test.True(t, ok)
		test.EqOp(t, safe.Error(), msg)

		msg, ok = ClientSafeMessage(unsafe)
		test.False(t, ok)
		test.EqOp(t, "", msg)

		_, ok = ClientSafeMessage(nil)
		test.False(t, ok)
	})
}

// TestClientSafeMessage_outermostNodeWins pins the ordering rule ClientSafeMessage
// documents: the chain decides, not the lists. A domain sentinel declared by
// wrapping a platform sentinel is more specific than what it wraps, and a client
// is owed the specific words; a lookup that scanned the platform list first
// would answer with the platform sentinel's text every time and the
// registration would be dead.
func TestClientSafeMessage_outermostNodeWins(T *testing.T) {
	T.Parallel()

	wrapper := platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "bad thing")
	RegisterClientSafeSentinels(wrapper)

	T.Run("a registered wrapper outranks the platform sentinel inside it", func(t *testing.T) {
		t.Parallel()

		msg, ok := ClientSafeMessage(platformerrors.Wrap(wrapper, "ctx"))
		must.True(t, ok)
		test.EqOp(t, wrapper.Error(), msg)
		test.NotEqOp(t, platformerrors.ErrUnrecognizedInputValue.Error(), msg)

		// And std errors.Is still sees both, so nothing about matching moved.
		test.ErrorIs(t, platformerrors.Wrap(wrapper, "ctx"), platformerrors.ErrUnrecognizedInputValue)
	})

	T.Run("a bare platform sentinel is unchanged", func(t *testing.T) {
		t.Parallel()

		msg, ok := ClientSafeMessage(platformerrors.ErrUnrecognizedInputValue)
		must.True(t, ok)
		test.EqOp(t, platformerrors.ErrUnrecognizedInputValue.Error(), msg)

		msg, ok = ClientSafeMessage(platformerrors.Wrap(platformerrors.ErrPermissionDenied, "listing users"))
		must.True(t, ok)
		test.EqOp(t, platformerrors.ErrPermissionDenied.Error(), msg)
	})

	T.Run("a join is walked depth-first in join order", func(t *testing.T) {
		t.Parallel()

		// The first branch has no client-safe node at any depth, so the walk
		// has to come back up and take the second one.
		joined := platformerrors.Join(
			platformerrors.Wrap(errors.New("update users set x = 1"), "unsafe branch"),
			platformerrors.Wrap(wrapper, "safe branch"),
		)
		msg, ok := ClientSafeMessage(joined)
		must.True(t, ok)
		test.EqOp(t, wrapper.Error(), msg)
	})
}

// TestUnaryErrorDecodingInterceptorKeepsBothIdioms is the property the
// interceptor exists for, and it is two properties because a client uses both
// and each is easy to break in service of the other.
//
// A decode that returned what DecodeErrorFromStatus returns would answer
// errors.Is and report codes.Unknown; one that returned the status untouched
// would answer the code and never match a sentinel. Callers of the first kind
// silently take a "something went wrong" branch on an error the server named
// precisely, and that is the failure this asserts against.
func TestUnaryErrorDecodingInterceptorKeepsBothIdioms(T *testing.T) {
	T.Parallel()

	// A sentinel PlatformMapper claims, so the code assertion below is about a
	// mapping that survived the trip rather than about the default.
	sentinel := platformerrors.ErrPermissionDenied

	// The server side, in one line: map, encode into the details, send.
	server := UnaryErrorEncodingInterceptor()

	sent, err := server(context.Background(), nil, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) {
			return nil, platformerrors.Wrap(sentinel, "creating identity user")
		})
	test.Nil(T, sent)
	must.Error(T, err)

	// The client side.
	decode := UnaryErrorDecodingInterceptor()

	got := decode(context.Background(), "/svc/Method", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			return err
		})
	must.Error(T, got)

	// std errors.Is, which is what every caller will actually write. It works
	// only because the returned error implements Is: what survives the wire is
	// the error's cockroachdb mark, not the sentinel's identity.
	test.True(T, errors.Is(got, sentinel), test.Sprintf(
		"the sentinel did not survive the round trip: %v", got))

	// And the status is still readable, carrying the code the mapper chose, so a
	// caller switching on the code is not broken by the decode having happened.
	st, ok := status.FromError(got)
	must.True(T, ok, must.Sprint("the decoded error is no longer a status"))
	test.EqOp(T, codes.PermissionDenied, st.Code())

	// The third property, and the one a caller sees first: what the error prints
	// is the chain the server sent rather than the status's own rendering, so a
	// log line names the failure and not "rpc error: code = PermissionDenied".
	test.StrContains(T, got.Error(), sentinel.Error())
	test.StrNotContains(T, got.Error(), "rpc error")

	// And it unwraps to the decoded chain — which is exactly the error std
	// errors.Is cannot match on its own, since what crossed the wire is the
	// mark and not the sentinel's identity. That is the whole reason the
	// returned error is a type with an Is method rather than the decoded chain
	// itself, and unwrapping past it is how a caller loses the match.
	unwrapped := errors.Unwrap(got)
	must.Error(T, unwrapped)
	test.False(T, errors.Is(unwrapped, sentinel))
	test.True(T, platformerrors.Is(unwrapped, sentinel))
}

// TestUnaryErrorDecodingInterceptorPassesThroughAPlainStatus covers the other
// branch: a status carrying no encoded detail is returned exactly as it arrived,
// rather than wrapped in something that adds nothing.
func TestUnaryErrorDecodingInterceptorPassesThroughAPlainStatus(T *testing.T) {
	T.Parallel()

	original := status.New(codes.NotFound, "not found").Err()

	got := UnaryErrorDecodingInterceptor()(context.Background(), "/svc/Method", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			return original
		})

	test.EqOp(T, original, got)
}

// TestUnaryErrorDecodingInterceptorPassesThroughSuccess is the case that must
// cost nothing.
func TestUnaryErrorDecodingInterceptorPassesThroughSuccess(T *testing.T) {
	T.Parallel()

	got := UnaryErrorDecodingInterceptor()(context.Background(), "/svc/Method", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			return nil
		})

	test.NoError(T, got)
}

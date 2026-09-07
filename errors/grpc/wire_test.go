package grpc

import (
	"context"
	stderrors "errors"
	"net"
	"strings"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v14/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// The wire tests run over a real gRPC connection, because the property they are
// about only exists there. A handler shapes an error, the encoding interceptor
// puts it in the status details, the transport serializes it, and the decoding
// interceptor reads it back — and every assertion here is about what survives
// that round trip. Testing the encoder against the decoder in-process would
// assert that two functions agree, which they did before this package could
// carry a sentinel at all.
//
// The health service is the vehicle. It is a generated service that ships with
// grpc-go, so this package gets a real unary RPC without a proto of its own and
// without importing a domain package, which as a primitive it may not do.

// errWireWidgetMissing stands in for a domain sentinel: declared by a package that owns
// it, claimed by a mapper that package registered.
var errWireWidgetMissing = platformerrors.New("the requested widget is not in this catalog")

// wireMapper is the registered-mapper half, so these tests exercise the path a
// domain transport actually takes rather than PlatformMapper's shortcut.
type wireMapper struct{}

func (wireMapper) Map(err error) (codes.Code, bool) {
	if stderrors.Is(err, errWireWidgetMissing) {
		return codes.FailedPrecondition, true
	}

	return codes.OK, false
}

// failingHealthServer answers every Check with the error it was built with.
type failingHealthServer struct {
	grpc_health_v1.UnimplementedHealthServer

	err error
}

func (s *failingHealthServer) Check(context.Context, *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	return nil, s.err
}

// serveHealth stands a server up on a bufconn with the encoding interceptor
// installed, and returns a client with the decoding interceptor installed.
func serveHealth(t *testing.T, handlerErr error) grpc_health_v1.HealthClient {
	t.Helper()

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(UnaryErrorEncodingInterceptor()))
	grpc_health_v1.RegisterHealthServer(server, &failingHealthServer{err: handlerErr})

	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(UnaryErrorDecodingInterceptor()),
	)
	must.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return grpc_health_v1.NewHealthClient(conn)
}

func TestPrepareAndLogGRPCStatus_OverTheWire(T *testing.T) {
	T.Parallel()

	RegisterGRPCErrorMapper(wireMapper{})

	// The chain a handler would really return: a sentinel, wrapped by the store
	// with the table it was reading, wrapped again by the description.
	fromTheStore := platformerrors.Wrap(errWireWidgetMissing, "scanning widget_catalog_entries")

	T.Run("the sentinel survives the round trip", func(t *testing.T) {
		t.Parallel()

		client := serveHealth(t, PrepareAndLogGRPCStatus(fromTheStore, nil, nil, codes.Internal, "fetching the widget"))

		_, err := client.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
		must.Error(t, err)

		// Std errors.Is, not cockroachdb's: the point of the whole exercise is
		// that a caller writes the obvious line and it is true.
		test.True(t, stderrors.Is(err, errWireWidgetMissing))
	})

	T.Run("the registered mapper's code arrives with it", func(t *testing.T) {
		t.Parallel()

		client := serveHealth(t, PrepareAndLogGRPCStatus(fromTheStore, nil, nil, codes.Internal, "fetching the widget"))

		_, err := client.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
		must.Error(t, err)

		// FailedPrecondition is the mapper's answer. Internal was the default
		// the call site guessed, and it does not win.
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
	})

	T.Run("the message is the handler's description, and the chain is not in it", func(t *testing.T) {
		t.Parallel()

		client := serveHealth(t, PrepareAndLogGRPCStatus(fromTheStore, nil, nil, codes.Internal, "fetching the widget"))

		_, err := client.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
		must.Error(t, err)

		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, "fetching the widget", st.Message())

		// The table name is in the chain, which is what the details carry. It
		// is not in the message, which is what a client that decodes nothing
		// reads and what an operator is likeliest to paste into a ticket.
		test.False(t, strings.Contains(st.Message(), "widget_catalog_entries"))
	})

	T.Run("a client that does not decode still reads a status", func(t *testing.T) {
		t.Parallel()

		// The same server, reached without the decoding interceptor: the
		// sentinel is gone for this caller — it is in the details it never
		// reads — but the code and the message are the ones above.
		listener := bufconn.Listen(1 << 20)
		server := grpc.NewServer(grpc.UnaryInterceptor(UnaryErrorEncodingInterceptor()))
		grpc_health_v1.RegisterHealthServer(server, &failingHealthServer{
			err: PrepareAndLogGRPCStatus(fromTheStore, nil, nil, codes.Internal, "fetching the widget"),
		})

		go func() { _ = server.Serve(listener) }()
		t.Cleanup(server.Stop)

		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return listener.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		must.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		_, checkErr := grpc_health_v1.NewHealthClient(conn).Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
		must.Error(t, checkErr)

		test.EqOp(t, codes.FailedPrecondition, status.Code(checkErr))
		test.EqOp(t, "fetching the widget", status.Convert(checkErr).Message())

		// And the chain is genuinely in the details this caller ignored:
		// DecodeErrorFromStatus finds the sentinel in it.
		//
		// Under cockroachdb's matcher, not the standard library's. The function
		// hands back the decoded chain bare, and a decoded sentinel carries the
		// original's mark rather than its identity — which is exactly what
		// UnaryErrorDecodingInterceptor's decodedError.Is exists to bridge, and
		// why the subtests above can write the obvious line and this one cannot.
		test.True(t, platformerrors.Is(DecodeErrorFromStatus(t.Context(), checkErr), errWireWidgetMissing))
	})
}

func TestPrepareAndLogGRPCStatus_Shape(T *testing.T) {
	T.Parallel()

	T.Run("a nil error is nothing to report", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, PrepareAndLogGRPCStatus(nil, nil, nil, codes.Internal, "fetching the widget"))
	})

	T.Run("the error is still the error", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("a locally declared failure")

		err := PrepareAndLogGRPCStatus(sentinel, nil, nil, codes.Internal, "fetching the widget")
		must.Error(t, err)

		// In-process, before any encoding: the chain is intact and the status
		// is on it. This is what the interceptor is handed.
		test.True(t, stderrors.Is(err, sentinel))
		test.EqOp(t, codes.Internal, status.Code(err))
		test.EqOp(t, "fetching the widget", status.Convert(err).Message())
	})

	T.Run("a registered client-safe sentinel outranks the description", func(t *testing.T) {
		t.Parallel()

		safe := platformerrors.New("this catalog is closed to new widgets")
		RegisterClientSafeSentinels(safe)

		err := PrepareAndLogGRPCStatus(platformerrors.Wrap(safe, "scanning widget_catalog_entries"),
			nil, nil, codes.FailedPrecondition, "fetching the widget")
		must.Error(t, err)

		test.EqOp(t, safe.Error(), status.Convert(err).Message())
	})

	T.Run("with no description the code names itself", func(t *testing.T) {
		t.Parallel()

		err := PrepareAndLogGRPCStatus(platformerrors.New("unmapped"), nil, nil, codes.DataLoss, "")
		must.Error(t, err)

		test.EqOp(t, codes.DataLoss.String(), status.Convert(err).Message())
	})
}

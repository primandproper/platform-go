package client_test

import (
	"context"
	"net"
	"slices"
	"sync"
	"testing"

	settingsclient "github.com/primandproper/platform-go/v14/settings/grpc/client"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/idempotency"
	idempotencygrpc "github.com/primandproper/primitives-go/v2/idempotency/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

func TestNewRefusesAnEmptyTarget(T *testing.T) {
	T.Parallel()

	c, err := settingsclient.New("")
	test.Nil(T, c)
	must.Error(T, err)

	// Both spellings match, which is what the sentinel wrapping is for: a
	// consumer checking the platform sentinel and one checking this package's
	// own name are asking the same question.
	test.ErrorIs(T, err, settingsclient.ErrEmptyTarget)
	test.True(T, platformerrors.Is(err, platformerrors.ErrEmptyInputParameter))
}

// TestNewSuppliesNoTransportSecurity is the failure grpc.NewClient already has,
// left in place.
//
// Every request on this service names a person and most of them carry something
// they chose about themselves. A default of insecure credentials would be this
// package deciding to put that on the wire in the clear on somebody's behalf.
func TestNewSuppliesNoTransportSecurity(T *testing.T) {
	T.Parallel()

	c, err := settingsclient.New("passthrough:///nowhere")
	if c != nil {
		T.Cleanup(func() { _ = c.Close() })
	}

	test.Error(T, err)
}

func TestNewDialsWithCredentials(T *testing.T) {
	T.Parallel()

	c, err := settingsclient.New("passthrough:///nowhere",
		settingsclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
	must.NoError(T, err)
	must.NotNil(T, c)

	// Every RPC is a method on the result, by embedding, which is the whole
	// reason this package adds none of its own.
	var _ settingspb.SettingsServiceClient = c

	test.NoError(T, c.Close())
}

// TestCloseOnAWrappedClientClosesNothing is the difference between New and Wrap,
// and it matters here for the same reason it matters for the directory: a
// consumer with one connection to a process serving several of this module's
// services would otherwise close it by tidying up one client.
func TestCloseOnAWrappedClientClosesNothing(T *testing.T) {
	T.Parallel()

	conn, err := grpc.NewClient("passthrough:///nowhere",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	must.NoError(T, err)
	T.Cleanup(func() { _ = conn.Close() })

	c := settingsclient.Wrap(conn)
	must.NotNil(T, c)

	test.NoError(T, c.Close())

	// The connection is still usable, which is what "closes nothing" means.
	test.NoError(T, conn.Close())
}

func TestDefaultInterceptorsIsADialOption(T *testing.T) {
	T.Parallel()

	test.NotNil(T, settingsclient.DefaultInterceptors())
}

// TestWithoutDefaultInterceptorsBuildsAClient is the escape hatch for a caller
// assembling their own chain. What declining the defaults costs them is
// asserted below, over a connection; what is checked here is that declining
// them is not declining the client.
func TestWithoutDefaultInterceptorsBuildsAClient(T *testing.T) {
	T.Parallel()

	c, err := settingsclient.New("passthrough:///nowhere",
		settingsclient.WithoutDefaultInterceptors(),
		settingsclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
	must.NoError(T, err)
	must.NotNil(T, c)

	test.NoError(T, c.Close())
}

// TestNilOptionsAreIgnored: a caller building an option list conditionally ends
// up with a nil in it, and a constructor that panicked on one would make the
// conditional the caller's problem.
func TestNilOptionsAreIgnored(T *testing.T) {
	T.Parallel()

	c, err := settingsclient.New("passthrough:///nowhere",
		nil,
		settingsclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
	must.NoError(T, err)
	must.NotNil(T, c)

	test.NoError(T, c.Close())
}

// The bufconn half of this file: a server that answers ClearValue by recording
// the metadata the call arrived with.
//
// It exists because the interceptor under test is a dial option, so what it
// does is only observable on the far side of a connection. The settings/grpc
// suite runs the real server in process and therefore has no connection to
// assert this over; this one has no store and answers nothing, because what is
// under test is what leaves the client rather than what the server decides.

// recordingServer answers ClearValue with an empty reply and keeps the
// idempotency keys the calls carried.
type recordingServer struct {
	settingspb.UnimplementedSettingsServiceServer

	keys []string

	mu sync.Mutex
}

// ClearValue is the RPC these tests call. It is chosen because it is one of the
// two the package documentation names: a second clear of the same value is
// settings.ErrValueNotFound, which is the answer the interceptor exists to keep
// a retry from reading.
func (s *recordingServer) ClearValue(
	ctx context.Context,
	_ *settingspb.ClearValueRequest,
) (*settingspb.ClearValueResponse, error) {
	var key string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get(idempotencygrpc.MetadataKey); len(values) > 0 {
			key = values[0]
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)

	return &settingspb.ClearValueResponse{}, nil
}

// seen is the keys the server has been sent, oldest first.
func (s *recordingServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.keys)
}

// serve stands a recordingServer up over a bufconn and hands back the dial
// option that reaches it, so a test dials through New rather than around it.
func serve(tb testing.TB) (*recordingServer, grpc.DialOption) {
	tb.Helper()

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	recorder := &recordingServer{}
	settingspb.RegisterSettingsServiceServer(server, recorder)

	go func() { _ = server.Serve(listener) }()

	tb.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	return recorder, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	})
}

// clearOnce is the one call every test below makes.
func clearOnce(tb testing.TB, ctx context.Context, c *settingsclient.Client) {
	tb.Helper()

	_, err := c.ClearValue(ctx, &settingspb.ClearValueRequest{
		Subject: &settingspb.SettingSubject{Type: "user", Id: "user-1"},
		Name:    "notifications.digest",
	})
	must.NoError(tb, err)
}

// TestTheIdempotencyKeyIsStampedByDefault is the ticket this package's
// documentation used to get wrong in the direction that costs the caller.
//
// A client retrying a reply it never received clears a value that is already
// cleared, and settings answers ErrValueNotFound — NotFound for work that
// succeeded. The interceptor sends the caller's key so a server holding one can
// hand back the first attempt's reply instead, and what is checked here is that
// the key leaves this client without anybody asking for it.
func TestTheIdempotencyKeyIsStampedByDefault(T *testing.T) {
	T.Parallel()

	recorder, dialer := serve(T)

	c, err := settingsclient.New("passthrough:///bufnet",
		settingsclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials()), dialer))
	must.NoError(T, err)
	T.Cleanup(func() { _ = c.Close() })

	ctx, key := idempotency.WithNewKey(T.Context())
	test.NotEq(T, idempotency.Key(""), key)

	// Twice, on the one context, because that is what a retry of a lost reply
	// is: the same logical operation, so the same key.
	clearOnce(T, ctx, c)
	clearOnce(T, ctx, c)

	test.Eq(T, []string{string(key), string(key)}, recorder.seen())
}

// TestNoKeyOnTheContextStampsNothing is the other half of the default, and it
// is why the default is safe to have: the interceptor never mints a key of its
// own.
//
// A client that generated one per call would make every call look idempotent
// and none of them be, since a retry would carry a fresh key. A caller who
// starts no operation sends exactly what they would have sent without the
// interceptor.
func TestNoKeyOnTheContextStampsNothing(T *testing.T) {
	T.Parallel()

	recorder, dialer := serve(T)

	c, err := settingsclient.New("passthrough:///bufnet",
		settingsclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials()), dialer))
	must.NoError(T, err)
	T.Cleanup(func() { _ = c.Close() })

	clearOnce(T, T.Context(), c)

	test.Eq(T, []string{""}, recorder.seen())
}

// TestWithoutDefaultInterceptorsStampsNothing is what declining the defaults
// costs, asserted rather than described: the key is on the context and it does
// not leave.
func TestWithoutDefaultInterceptorsStampsNothing(T *testing.T) {
	T.Parallel()

	recorder, dialer := serve(T)

	c, err := settingsclient.New("passthrough:///bufnet",
		settingsclient.WithoutDefaultInterceptors(),
		settingsclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials()), dialer))
	must.NoError(T, err)
	T.Cleanup(func() { _ = c.Close() })

	ctx, _ := idempotency.WithNewKey(T.Context())
	clearOnce(T, ctx, c)

	test.Eq(T, []string{""}, recorder.seen())
}

// TestWrapStampsNothing is the reminder on Wrap's own documentation, asserted.
//
// The interceptors are dial options and the connection is already dialed, so a
// consumer sharing one connection across several of this module's services
// passes DefaultInterceptors when they dial it — which is the second half of
// this test.
func TestWrapStampsNothing(T *testing.T) {
	T.Parallel()

	recorder, dialer := serve(T)

	bare, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()), dialer)
	must.NoError(T, err)
	T.Cleanup(func() { _ = bare.Close() })

	ctx, key := idempotency.WithNewKey(T.Context())
	clearOnce(T, ctx, settingsclient.Wrap(bare))

	test.Eq(T, []string{""}, recorder.seen())

	// And the same connection dialed with the exported chain does stamp, which
	// is what makes the assertion above about the dial option rather than about
	// Wrap losing the context.
	chained, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()), dialer,
		settingsclient.DefaultInterceptors())
	must.NoError(T, err)
	T.Cleanup(func() { _ = chained.Close() })

	clearOnce(T, ctx, settingsclient.Wrap(chained))

	test.Eq(T, []string{"", string(key)}, recorder.seen())
}

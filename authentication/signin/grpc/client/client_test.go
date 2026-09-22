package client_test

import (
	"context"
	"net"
	"slices"
	"sync"
	"testing"

	signinclient "github.com/primandproper/platform-go/v14/authentication/signin/grpc/client"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	"github.com/primandproper/primitives-go/v2/idempotency"
	idempotencygrpc "github.com/primandproper/primitives-go/v2/idempotency/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// The bufconn half of this file: a server that answers two RPCs by recording the
// metadata each call arrived with.
//
// It exists because the interceptor under test is a dial option, so what it does
// is only observable on the far side of a connection. The signin/grpc suite runs
// the real server in process and therefore has no connection to assert this
// over; this one has no service behind it and answers nothing, because what is
// under test is what leaves the client rather than what the server decides.

// recordingServer answers the two RPCs these tests call and keeps the
// idempotency keys they carried, per RPC.
type recordingServer struct {
	signinpb.UnimplementedSignInServiceServer

	keys map[string][]string

	mu sync.Mutex
}

// ExchangeRefreshToken is the one RPC a key is meant to reach. It answers with
// an empty reply: a real one carries a live token, which is the reason this
// service reads the key itself rather than being wrapped in a Manager that would
// record the reply.
func (s *recordingServer) ExchangeRefreshToken(
	ctx context.Context,
	_ *signinpb.ExchangeRefreshTokenRequest,
) (*signinpb.ExchangeRefreshTokenResponse, error) {
	s.record(ctx, "exchange")

	return &signinpb.ExchangeRefreshTokenResponse{}, nil
}

// LoginForToken is the other side of the filter, and it is the RPC whose reply
// must never be recorded anywhere.
func (s *recordingServer) LoginForToken(
	ctx context.Context,
	_ *signinpb.LoginForTokenRequest,
) (*signinpb.LoginForTokenResponse, error) {
	s.record(ctx, "login")

	return &signinpb.LoginForTokenResponse{}, nil
}

func (s *recordingServer) record(ctx context.Context, rpc string) {
	var key string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get(idempotencygrpc.MetadataKey); len(values) > 0 {
			key = values[0]
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[rpc] = append(s.keys[rpc], key)
}

// seen is the keys one RPC has been sent, oldest first.
func (s *recordingServer) seen(rpc string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.keys[rpc])
}

// serve stands a recordingServer up over a bufconn and hands back the dial
// option that reaches it, so a test dials through New rather than around it.
func serve(tb testing.TB) (*recordingServer, grpc.DialOption) {
	tb.Helper()

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	recorder := &recordingServer{keys: map[string][]string{}}
	signinpb.RegisterSignInServiceServer(server, recorder)

	go func() { _ = server.Serve(listener) }()

	tb.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	return recorder, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	})
}

// dial builds a client over the bufconn, with whatever options the test adds.
func dial(tb testing.TB, dialer grpc.DialOption, opts ...signinclient.Option) *signinclient.Client {
	tb.Helper()

	c, err := signinclient.New("passthrough:///bufnet",
		append(opts, signinclient.WithDialOptions(
			grpc.WithTransportCredentials(insecure.NewCredentials()), dialer))...)
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = c.Close() })

	return c
}

// exchangeOnce and loginOnce are the two calls every test below makes.
func exchangeOnce(tb testing.TB, ctx context.Context, c *signinclient.Client) {
	tb.Helper()

	_, err := c.ExchangeRefreshToken(ctx, &signinpb.ExchangeRefreshTokenRequest{RefreshToken: "secret"})
	must.NoError(tb, err)
}

func loginOnce(tb testing.TB, ctx context.Context, c *signinclient.Client) {
	tb.Helper()

	_, err := c.LoginForToken(ctx, &signinpb.LoginForTokenRequest{
		Credentials: &signinpb.Credentials{Username: "user", Password: "password"},
	})
	must.NoError(tb, err)
}

// TestTheIdempotencyKeyReachesTheExchange is the whole reason this client stamps
// one, asserted rather than described.
//
// A client whose exchange timed out holds no successor to retry with, and
// re-sending the token it has reads as a replay and ends the login. The key is
// what makes the retry recognizable, and it is worth nothing if it does not
// leave the process — so what is checked here is that it does, twice on the one
// context, because that is what a retry of a lost reply is.
func TestTheIdempotencyKeyReachesTheExchange(T *testing.T) {
	T.Parallel()

	recorder, dialer := serve(T)
	c := dial(T, dialer)

	ctx, key := idempotency.WithNewKey(T.Context())
	test.NotEq(T, idempotency.Key(""), key)

	exchangeOnce(T, ctx, c)
	exchangeOnce(T, ctx, c)

	test.Eq(T, []string{string(key), string(key)}, recorder.seen("exchange"))
}

// TestTheIdempotencyKeyReachesNothingElse is the filter, and it is the half of
// this default that is a refusal rather than a feature.
//
// A key stamped on a sign-in is a key nothing on the far side reads, and a live
// token waiting in the store of any consumer who wrapped this service in a
// Manager of their own. The same context carries a key to both calls; only one
// of them sends it.
func TestTheIdempotencyKeyReachesNothingElse(T *testing.T) {
	T.Parallel()

	recorder, dialer := serve(T)
	c := dial(T, dialer)

	ctx, key := idempotency.WithNewKey(T.Context())

	loginOnce(T, ctx, c)
	exchangeOnce(T, ctx, c)

	test.Eq(T, []string{""}, recorder.seen("login"))
	test.Eq(T, []string{string(key)}, recorder.seen("exchange"))
}

// TestNoKeyOnTheContextStampsNothing is why the default is safe to have: the
// interceptor never mints a key of its own.
//
// A client that generated one per call would make every call look idempotent and
// none of them be, since a retry would carry a fresh key. A caller who starts no
// operation sends exactly what they would have sent without the interceptor.
func TestNoKeyOnTheContextStampsNothing(T *testing.T) {
	T.Parallel()

	recorder, dialer := serve(T)
	c := dial(T, dialer)

	exchangeOnce(T, T.Context(), c)

	test.Eq(T, []string{""}, recorder.seen("exchange"))
}

// TestWithoutDefaultInterceptorsStampsNothing is what declining the defaults
// costs, asserted rather than described: the key is on the context and it does
// not leave, so the exchange goes back to being unretryable.
func TestWithoutDefaultInterceptorsStampsNothing(T *testing.T) {
	T.Parallel()

	recorder, dialer := serve(T)
	c := dial(T, dialer, signinclient.WithoutDefaultInterceptors())

	ctx, _ := idempotency.WithNewKey(T.Context())
	exchangeOnce(T, ctx, c)

	test.Eq(T, []string{""}, recorder.seen("exchange"))
}

// TestWrapStampsNothing is the reminder on Wrap's own documentation, asserted.
//
// The interceptors are dial options and the connection is already dialed, so a
// consumer sharing one connection across several of this module's services
// passes DefaultInterceptors when they dial it — which is the second half of
// this test, and is what makes the first half an assertion about the dial option
// rather than about Wrap losing the context.
func TestWrapStampsNothing(T *testing.T) {
	T.Parallel()

	recorder, dialer := serve(T)

	bare, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()), dialer)
	must.NoError(T, err)
	T.Cleanup(func() { _ = bare.Close() })

	ctx, key := idempotency.WithNewKey(T.Context())
	exchangeOnce(T, ctx, signinclient.Wrap(bare))

	test.Eq(T, []string{""}, recorder.seen("exchange"))

	chained, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()), dialer,
		signinclient.DefaultInterceptors())
	must.NoError(T, err)
	T.Cleanup(func() { _ = chained.Close() })

	exchangeOnce(T, ctx, signinclient.Wrap(chained))

	test.Eq(T, []string{"", string(key)}, recorder.seen("exchange"))
}

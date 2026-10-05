package grpc_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/errormappers"
	"github.com/primandproper/platform-go/v15/mediaregistry"
	mediaregistrygrpc "github.com/primandproper/platform-go/v15/mediaregistry/grpc"
	mediaregistryclient "github.com/primandproper/platform-go/v15/mediaregistry/grpc/client"
	mediaregistryhttp "github.com/primandproper/platform-go/v15/mediaregistry/http"
	"github.com/primandproper/platform-go/v15/mediaregistry/mediaregistrypb"
	"github.com/primandproper/platform-go/v15/mediaregistry/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"
	"github.com/primandproper/primitives-go/v2/uploads"

	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// The suite runs the real server over a bufconn, against a real SQLite
// database and a real mediaregistry.SQLStore.
//
// Over a connection rather than in process, unlike most of this module's gRPC
// suites, because UploadObject is a client stream: what is under test is what
// the server does with a stream it reads a chunk at a time, and in process
// there is no stream — only a double of one, which answers that question
// itself. The encoding interceptors are installed as a consumer's main installs
// them, and the client is this package's own, so a refusal is asserted as a
// client reads it.

// TestMain registers the domain tier's error mappers once for the binary, which
// is the call a consumer owes at their composition root.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// The tenants these tests work in, and the people in them.
var (
	testScope  = tenancy.Of("tenant_1")
	otherScope = tenancy.Of("tenant_2")
)

const (
	alice = "user_alice"
	bob   = "user_bob"

	// aliceHome is the part of the bucket DefaultKeyFunc gives alice in
	// testScope.
	aliceHome = "tenants/tenant_1/" + alice

	pngType = "image/png"
)

// testClientConfig is the minimal database.ClientConfig these tests dial with.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

var prefixCounter atomic.Uint64

// bucket is an UploadManager over objects held in memory, with the optional
// Attributer capability a registration reads a size through.
//
// saved counts the bytes every Save read from its reader, including the ones a
// failed Save read before failing — which is how the cap is asserted to stop
// the read rather than merely refuse its result.
type bucket struct {
	objects map[string][]byte
	types   map[string]string
	saved   atomic.Int64
	mu      sync.Mutex
}

var (
	_ uploads.UploadManager = (*bucket)(nil)
	_ uploads.Attributer    = (*bucket)(nil)
)

func newBucket() *bucket {
	return &bucket{objects: map[string][]byte{}, types: map[string]string{}}
}

func (b *bucket) Save(_ context.Context, key string, r io.Reader, opts ...uploads.SaveOption) error {
	var buf bytes.Buffer

	n, err := io.Copy(&buf, r)
	b.saved.Add(n)

	if err != nil {
		// A provider whose copy failed writes nothing, which is what
		// objectstorage.Uploader does by cancelling its writer.
		return platformerrors.Wrap(err, "writing object content")
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.objects[key] = buf.Bytes()
	b.types[key] = uploads.BuildSaveOptions(opts...).ContentType

	return nil
}

func (b *bucket) Open(_ context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	content, ok := b.objects[key]
	if !ok {
		return nil, platformerrors.New("no such object")
	}

	return io.NopCloser(bytes.NewReader(content)), nil
}

func (b *bucket) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.objects, key)

	return nil
}

func (b *bucket) Exists(_ context.Context, key string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	_, ok := b.objects[key]

	return ok, nil
}

func (b *bucket) Attributes(_ context.Context, key string) (*uploads.Attributes, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	content, ok := b.objects[key]
	if !ok {
		return nil, platformerrors.New("no such object")
	}

	return &uploads.Attributes{Size: int64(len(content)), ContentType: b.types[key]}, nil
}

func (b *bucket) Close() error { return nil }

// put writes bytes straight into the bucket, standing in for a client that
// uploaded through a signed URL.
func (b *bucket) put(key string, content []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.objects[key] = content
}

func (b *bucket) holds(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	_, ok := b.objects[key]

	return ok
}

// The metadata a test's caller travels in, standing in for whatever a
// consumer's authentication interceptor reads off a request.
const (
	principalHeader = "x-test-principal"
	tenantHeader    = "x-test-tenant"
)

type callerKey struct{}

// fromMetadata is the suite's authentication interceptor: it puts the caller
// the metadata names on the context, or nobody.
func fromMetadata(ctx context.Context) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)

	principals, tenants := md.Get(principalHeader), md.Get(tenantHeader)
	if len(principals) == 0 || len(tenants) == 0 {
		return ctx
	}

	return context.WithValue(ctx, callerKey{}, mediaregistryhttp.Caller{
		PrincipalID: principals[0],
		Scope:       tenancy.Of(tenants[0]),
	})
}

// resolveCaller is the resolver the server is built with.
func resolveCaller(ctx context.Context) (mediaregistryhttp.Caller, error) {
	caller, ok := ctx.Value(callerKey{}).(mediaregistryhttp.Caller)
	if !ok {
		return mediaregistryhttp.Caller{}, callers.ErrNoPrincipal
	}

	return caller, nil
}

// contextStream is a server stream whose context is the one the suite's
// interceptor derived.
type contextStream struct {
	grpc.ServerStream

	ctx context.Context
}

func (s *contextStream) Context() context.Context { return s.ctx }

// harness is one database, one store, one bucket and one server over them,
// reached through the package's own client.
type harness struct {
	db     database.Client
	store  *mediaregistry.SQLStore
	bucket *bucket
	client *mediaregistryclient.Client
}

// newHarness migrates a uniquely prefixed table and serves the surface over it.
func newHarness(tb testing.TB, opts ...mediaregistrygrpc.Option) *harness {
	tb.Helper()

	db, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "mediaregistry.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("mrg_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(tb, err)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := mediaregistry.NewSQLStore(db, mediaregistry.WithTablePrefix(prefix))
	must.NoError(tb, err)

	objects := newBucket()

	srv, err := mediaregistrygrpc.NewServer(store, db, objects,
		append([]mediaregistrygrpc.Option{mediaregistrygrpc.WithCallerResolver(resolveCaller)}, opts...)...)
	must.NoError(tb, err)

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			grpcerrors.UnaryErrorEncodingInterceptor(),
			func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				return handler(fromMetadata(ctx), req)
			},
		),
		grpc.ChainStreamInterceptor(
			grpcerrors.StreamErrorEncodingInterceptor(),
			func(s any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
				return handler(s, &contextStream{ServerStream: ss, ctx: fromMetadata(ss.Context())})
			},
		),
	)
	srv.RegisterOn(server)

	go func() { _ = server.Serve(listener) }()

	tb.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	client, err := mediaregistryclient.New("passthrough:///bufnet",
		mediaregistryclient.WithDialOptions(
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return listener.DialContext(ctx)
			}),
		))
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	return &harness{db: db, store: store, bucket: objects, client: client}
}

// as is a request context carrying the named caller in scope.
func as(tb testing.TB, principal string, scope tenancy.Scope) context.Context {
	tb.Helper()

	return metadata.AppendToOutgoingContext(tb.Context(), principalHeader, principal, tenantHeader, scope.String())
}

// upload sends one object through the surface as ctx's caller, in chunks of
// chunkSize, and answers with what the server said.
func (h *harness) upload(
	tb testing.TB,
	ctx context.Context,
	header *mediaregistrypb.UploadObjectHeader,
	content []byte,
	chunkSize int,
) (*mediaregistrypb.Object, error) {
	tb.Helper()

	stream, err := h.client.UploadObject(ctx)
	must.NoError(tb, err)

	if header != nil {
		if err = stream.Send(&mediaregistrypb.UploadObjectRequest{
			Part: &mediaregistrypb.UploadObjectRequest_Header{Header: header},
		}); err != nil {
			return closeAndRecv(stream)
		}
	}

	for start := 0; start < len(content); start += chunkSize {
		end := min(start+chunkSize, len(content))

		// A send fails with io.EOF once the server has answered, which it does
		// the moment it refuses; the answer is what CloseAndRecv reports.
		if err = stream.Send(&mediaregistrypb.UploadObjectRequest{
			Part: &mediaregistrypb.UploadObjectRequest_Chunk{Chunk: content[start:end]},
		}); err != nil {
			break
		}
	}

	return closeAndRecv(stream)
}

func closeAndRecv(
	stream grpc.ClientStreamingClient[mediaregistrypb.UploadObjectRequest, mediaregistrypb.UploadObjectResponse],
) (*mediaregistrypb.Object, error) {
	res, err := stream.CloseAndRecv()
	if err != nil {
		return nil, err
	}

	return res.GetResult(), nil
}

// uploadPNG uploads a small PNG as ctx's caller, attached to nothing, and
// fails the test if it is refused.
func (h *harness) uploadPNG(tb testing.TB, ctx context.Context) *mediaregistrypb.Object {
	tb.Helper()

	object, err := h.upload(tb, ctx, &mediaregistrypb.UploadObjectHeader{Name: "photo.png", ContentType: pngType}, pngBytes(), 3)
	must.NoError(tb, err)
	must.NotNil(tb, object)

	return object
}

// pngBytes is what every uploaded object here contains, when what it contains
// is immaterial.
func pngBytes() []byte { return []byte("\x89PNG not really a png") }

// seed registers a row straight through the store, for the tests about what a
// caller reaches that must not reach it through the surface under test.
//
//nolint:gocritic // hugeParam: ObjectInput is taken by value to match Store.RecordObject
func (h *harness) seed(tb testing.TB, scope tenancy.Scope, in mediaregistry.ObjectInput) *mediaregistry.Object {
	tb.Helper()

	var recorded *mediaregistry.Object

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var err error
		recorded, err = h.store.RecordObject(tb.Context(), tx, scope, in)

		return err
	}))

	return recorded
}

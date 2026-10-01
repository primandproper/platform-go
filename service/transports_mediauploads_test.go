package service

import (
	"context"
	"net"
	"testing"

	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistrygrpc "github.com/primandproper/platform-go/v14/mediaregistry/grpc"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	"github.com/primandproper/platform-go/v14/mediaregistry/mediaregistrypb"
	mediaregistrymock "github.com/primandproper/platform-go/v14/mediaregistry/mock"

	"github.com/primandproper/primitives-go/v2/database"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
	"github.com/primandproper/primitives-go/v2/uploads"
	uploadsmock "github.com/primandproper/primitives-go/v2/uploads/mock"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// TestRegisterTransports_mediaUploads is the media registry's gRPC surface as
// the automatic mount builds it: over the serve route's store and manager, with
// the serve route's caller — the principal, in the tenant the application read
// off it — and the serve route's entitlement.
func TestRegisterTransports_mediaUploads(T *testing.T) {
	T.Parallel()

	const tenant = "acct_the_studio"

	caller := testPrincipal{userID: "user_1", scope: tenancy.Global(), account: tenant}
	accountOf := func(principal callers.Principal) (tenancy.Scope, error) {
		return tenancy.Of(principal.ActiveAccountID()), nil
	}

	T.Run("a read is the principal's, in the tenant the application read off them", func(t *testing.T) {
		t.Parallel()

		var (
			askedScope tenancy.Scope
			askedOwner string
		)

		store := &mediaregistrymock.StoreMock{
			ListObjectsByOwnerFunc: func(
				_ context.Context, _ database.SQLQueryExecutor, scope tenancy.Scope, ownerID string, _ *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[mediaregistry.Object], error) {
				askedScope, askedOwner = scope, ownerID

				return &filtering.QueryFilteredResult[mediaregistry.Object]{}, nil
			},
		}

		client := mediaUploadsOverBufconn(t, store, caller, &Transports{
			Extractor:   withPrincipal,
			Authorizers: allAuthorizers(),
			TenantOf:    accountOf,
		})

		_, err := client.ListMyObjects(t.Context(), &mediaregistrypb.ListMyObjectsRequest{})
		must.NoError(t, err)

		test.EqOp(t, tenancy.Of(tenant), askedScope)
		test.EqOp(t, "user_1", askedOwner)
	})

	T.Run("the serve route's entitlement is the one the reads ask", func(t *testing.T) {
		t.Parallel()

		var consulted bool

		store := &mediaregistrymock.StoreMock{
			GetObjectFunc: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, string) (*mediaregistry.Object, error) {
				return &mediaregistry.Object{ID: "obj", OwnerID: "somebody_else"}, nil
			},
		}

		authorizers := allAuthorizers()
		authorizers.MediaObjects = func(context.Context, mediaregistryhttp.Caller, *mediaregistry.Object) (bool, error) {
			consulted = true

			return true, nil
		}

		client := mediaUploadsOverBufconn(t, store, caller, &Transports{
			Extractor:   withPrincipal,
			Authorizers: authorizers,
			TenantOf:    DirectoryTenant,
		})

		got, err := client.GetObject(t.Context(), &mediaregistrypb.GetObjectRequest{ObjectId: "obj"})
		must.NoError(t, err)
		test.EqOp(t, "somebody_else", got.GetResult().GetOwnerId())
		test.True(t, consulted)
	})

	T.Run("a request with nobody on it is unauthenticated", func(t *testing.T) {
		t.Parallel()

		client := mediaUploadsOverBufconn(t, &mediaregistrymock.StoreMock{}, nil, &Transports{
			Extractor:   withPrincipal,
			Authorizers: allAuthorizers(),
			TenantOf:    DirectoryTenant,
		})

		_, err := client.ListMyObjects(t.Context(), &mediaregistrypb.ListMyObjectsRequest{})
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})

	T.Run("the application's own options reach the surface", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[uploads.UploadManager](i, &uploadsmock.UploadManagerMock{})
		do.ProvideValue[mediaregistry.Store](i, &mediaregistrymock.StoreMock{})

		// Options of its own mean no derivation, so no extractor is needed —
		// and the application's resolver is the one the surface is built with.
		RegisterTransports(i, &Transports{
			Authorizers: allAuthorizers(),
			Options: SurfaceOptions{MediaUploads: []mediaregistrygrpc.Option{
				mediaregistrygrpc.WithCallerResolver(func(context.Context) (mediaregistryhttp.Caller, error) {
					return mediaregistryhttp.Caller{PrincipalID: "user_1", Scope: tenancy.Global()}, nil
				}),
			}},
			Skip: []Surface{SurfaceMediaRegistry},
		})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)
		test.Eq(t, []string{"media uploads gRPC"}, mounted.names)
	})

	T.Run("skipping it leaves the serve route mounted", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue(i, newRouter())
		do.ProvideValue[uploads.UploadManager](i, &uploadsmock.UploadManagerMock{})
		do.ProvideValue[mediaregistry.Store](i, &mediaregistrymock.StoreMock{})

		RegisterTransports(i, &Transports{
			Extractor:   withPrincipal,
			TenantOf:    DirectoryTenant,
			Authorizers: allAuthorizers(),
			Skip:        []Surface{SurfaceMediaUploads},
		})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)
		test.Eq(t, []string{"media registry HTTP"}, mounted.names)
	})
}

// mediaUploadsOverBufconn mounts the transports over store and serves them on
// an in-process connection, returning a client for the media registry's gRPC
// surface. A non-nil caller is put on every unary request's context.
func mediaUploadsOverBufconn(
	t *testing.T,
	store mediaregistry.Store,
	caller callers.Principal,
	transports *Transports,
) mediaregistrypb.MediaRegistryServiceClient {
	t.Helper()

	i := newTransportInjector(t)

	do.ProvideValue[database.Client](i, &databasemock.ClientMock{
		ReaderFunc: func() database.SQLQueryExecutor { return &databasemock.SQLQueryExecutorMock{} },
	})
	do.ProvideValue[uploads.UploadManager](i, &uploadsmock.UploadManagerMock{})
	do.ProvideValue(i, store)

	RegisterTransports(i, transports)

	mounted, err := do.Invoke[*mountedTransports](i)
	must.NoError(t, err)
	must.SliceContains(t, mounted.names, "media uploads gRPC")

	var opts []grpc.ServerOption
	if caller != nil {
		opts = append(opts, grpc.UnaryInterceptor(
			func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				return handler(context.WithValue(ctx, principalKey{}, caller), req)
			},
		))
	}

	server := grpc.NewServer(opts...)
	for _, register := range mounted.registrations {
		register(server)
	}

	listener := bufconn.Listen(1024 * 1024)

	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	must.NoError(t, err)

	t.Cleanup(func() {
		_ = conn.Close()
		server.Stop()
		_ = listener.Close()
	})

	return mediaregistrypb.NewMediaRegistryServiceClient(conn)
}

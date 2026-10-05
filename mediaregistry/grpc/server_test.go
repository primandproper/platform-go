package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v15/mediaregistry"
	mediaregistrygrpc "github.com/primandproper/platform-go/v15/mediaregistry/grpc"
	mediaregistryhttp "github.com/primandproper/platform-go/v15/mediaregistry/http"
	mediaregistrymock "github.com/primandproper/platform-go/v15/mediaregistry/mock"

	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewServer(T *testing.T) {
	T.Parallel()

	store := &mediaregistrymock.StoreMock{}
	client := &databasemock.ClientMock{}
	objects := newBucket()
	withResolver := mediaregistrygrpc.WithCallerResolver(resolveCaller)

	T.Run("the three seams that cannot be defaulted are refused when absent", func(t *testing.T) {
		t.Parallel()

		_, err := mediaregistrygrpc.NewServer(nil, client, objects, withResolver)
		test.ErrorIs(t, err, mediaregistry.ErrNilStore)

		_, err = mediaregistrygrpc.NewServer(store, nil, objects, withResolver)
		test.ErrorIs(t, err, mediaregistry.ErrNilDatabaseClient)

		_, err = mediaregistrygrpc.NewServer(store, client, nil, withResolver)
		test.ErrorIs(t, err, mediaregistry.ErrNilUploadManager)
	})

	T.Run("the caller resolver has no default", func(t *testing.T) {
		t.Parallel()

		srv, err := mediaregistrygrpc.NewServer(store, client, objects)
		test.Nil(t, srv)
		test.ErrorIs(t, err, mediaregistrygrpc.ErrNilCallerResolver)
		test.True(t, platformerrors.Is(err, platformerrors.ErrNilInputParameter))
	})

	T.Run("every other seam has one", func(t *testing.T) {
		t.Parallel()

		srv, err := mediaregistrygrpc.NewServer(store, client, objects, withResolver, nil,
			mediaregistrygrpc.WithEntitlement(nil),
			mediaregistrygrpc.WithKeyFunc(nil),
			mediaregistrygrpc.WithContentTypePolicy(nil),
			mediaregistrygrpc.WithPillars(nil),
			mediaregistrygrpc.WithPillars(&observability.Pillars{}),
			mediaregistrygrpc.WithLogger(nil),
			mediaregistrygrpc.WithTracerProvider(nil),
			mediaregistrygrpc.WithMetricsProvider(nil),
		)
		must.NoError(t, err)
		test.NotNil(t, srv)
	})
}

func TestPolicies(T *testing.T) {
	T.Parallel()

	alicesCaller := mediaregistryhttp.Caller{PrincipalID: alice, Scope: testScope}

	T.Run("the default layout is tenant, principal, id, name", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "tenants/tenant_1/user_alice/obj/a.png", mediaregistrygrpc.DefaultKeyFunc(alicesCaller, "obj", "a.png"))
		test.EqOp(t, "global/user_alice/obj/a.png",
			mediaregistrygrpc.DefaultKeyFunc(mediaregistryhttp.Caller{PrincipalID: alice, Scope: tenancy.Global()}, "obj", "a.png"))
	})

	T.Run("the default layout keeps each identifier to one segment", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "tenants/a%2Fb/c%2F..%2Fd/obj/a.png",
			mediaregistrygrpc.DefaultKeyFunc(mediaregistryhttp.Caller{PrincipalID: "c/../d", Scope: tenancy.Of("a/b")}, "obj", "a.png"))
	})

	T.Run("keys under the layout's prefix, and only those", func(t *testing.T) {
		t.Parallel()

		policy := mediaregistrygrpc.KeysUnderPrefix(mediaregistrygrpc.DefaultKeyFunc)

		for key, want := range map[string]bool{
			"tenants/tenant_1/user_alice/1/a.png":             true,
			"tenants/tenant_1/user_alice/a.png":               true,
			"tenants/tenant_1/user_alice":                     false,
			"tenants/tenant_1/user_alice/":                    false,
			"tenants/tenant_1/user_alicex/1/a.png":            false,
			"tenants/tenant_1/user_bob/1/a.png":               false,
			"tenants/tenant_2/user_alice/1/a.png":             false,
			"global/user_alice/1/a.png":                       false,
			"user_alice/1/a.png":                              false,
			"tenants/tenant_1/user_alice/../user_bob/1/a.png": false,
			"tenants/tenant_1/user_alice//1/a.png":            false,
			"/tenants/tenant_1/user_alice/1/a.png":            false,
		} {
			got, err := policy(t.Context(), alicesCaller, key)
			must.NoError(t, err)
			test.EqOp(t, want, got, test.Sprintf("key %q", key))
		}
	})

	T.Run("a layout that puts nothing of the caller first admits nothing", func(t *testing.T) {
		t.Parallel()

		policy := mediaregistrygrpc.KeysUnderPrefix(func(_ mediaregistryhttp.Caller, objectID, name string) string {
			return objectID + "/" + name
		})

		got, err := policy(context.Background(), alicesCaller, "anything/at/all")
		must.NoError(t, err)
		test.False(t, got)
	})

	T.Run("a caller with no principal has no prefix", func(t *testing.T) {
		t.Parallel()

		policy := mediaregistrygrpc.KeysUnderPrefix(func(_ mediaregistryhttp.Caller, objectID, name string) string {
			return "uploads/" + objectID + "/" + name
		})

		got, err := policy(t.Context(), mediaregistryhttp.Caller{Scope: testScope}, "uploads/x/y")
		must.NoError(t, err)
		test.False(t, got)
	})

	T.Run("an allowlist matches the media type, whatever its case and parameters", func(t *testing.T) {
		t.Parallel()

		allow := mediaregistrygrpc.AllowContentTypes("image/png", "IMAGE/JPEG")

		test.True(t, allow("image/png"))
		test.True(t, allow("Image/PNG; charset=binary"))
		test.True(t, allow("image/jpeg"))
		test.False(t, allow("image/gif"))
		test.False(t, allow(""))
		test.False(t, allow("not a type"))
	})
}

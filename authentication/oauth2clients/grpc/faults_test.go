package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	oauth2clientsgrpc "github.com/primandproper/platform-go/v14/authentication/oauth2clients/grpc"
	oauth2clientsmock "github.com/primandproper/platform-go/v14/authentication/oauth2clients/mock"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/database"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	"github.com/primandproper/platform-go/v14/filtering"
	"github.com/primandproper/platform-go/v14/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// broken is the failure a store reports that is nobody's fault but the
// database's: not a sentinel, so no mapper claims it.
var broken = platformerrors.New("the registry is unreachable")

// faultyServer is the surface over a store that fails the way the mock is
// configured to, and over a real transaction so the writes reach it.
func faultyServer(tb testing.TB, store *oauth2clientsmock.StoreMock) *oauth2clientsgrpc.Server {
	tb.Helper()

	h := newHarness(tb)

	svc, err := oauth2clients.NewService(h.db, store)
	must.NoError(tb, err)

	srv, err := oauth2clientsgrpc.NewServer(svc, store, h.db, extractPrincipal)
	must.NoError(tb, err)

	return srv
}

// everythingFails is a store whose every method reports the same broken
// database.
func everythingFails() *oauth2clientsmock.StoreMock {
	return &oauth2clientsmock.StoreMock{
		GetClientFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*oauth2clients.Client, error) {
			return nil, broken
		},
		ListClientsFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[oauth2clients.Client], error) {
			return nil, broken
		},
		CreateClientFunc: func(context.Context, database.Tx, tenancy.Scope, *oauth2clients.Client) error {
			return broken
		},
		ArchiveClientFunc: func(context.Context, database.Tx, tenancy.Scope, string) error {
			return broken
		},
	}
}

// TestABrokenRegistryFailsEveryRPC is the failure with no considered answer, and
// the reason every handler here passes codes.Internal as its default.
//
// A store that is simply down reports something no mapper claims, so the default
// is what the caller reads — which is right: there is no field to fix and no
// retry the caller can be told to make. The property worth pinning is that it is
// not swallowed into an empty page or a nil result, which is what a handler that
// dropped the error would produce.
func TestABrokenRegistryFailsEveryRPC(T *testing.T) {
	T.Parallel()

	for name, call := range map[string]func(context.Context, *oauth2clientsgrpc.Server) error{
		"create": func(ctx context.Context, s *oauth2clientsgrpc.Server) error {
			_, err := s.CreateOAuth2Client(ctx,
				&oauth2clientspb.CreateOAuth2ClientRequest{Input: creationInput()})

			return err
		},
		"get": func(ctx context.Context, s *oauth2clientsgrpc.Server) error {
			_, err := s.GetOAuth2Client(ctx,
				&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: "row_1"})

			return err
		},
		"list": func(ctx context.Context, s *oauth2clientsgrpc.Server) error {
			_, err := s.ListOAuth2Clients(ctx, &oauth2clientspb.ListOAuth2ClientsRequest{})

			return err
		},
		"archive": func(ctx context.Context, s *oauth2clientsgrpc.Server) error {
			_, err := s.ArchiveOAuth2Client(ctx,
				&oauth2clientspb.ArchiveOAuth2ClientRequest{Oauth2ClientId: "row_1"})

			return err
		},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := faultyServer(t, everythingFails())

			err := call(withPrincipal(t.Context(), &testPrincipal{userID: testOwner, scope: testScope}), srv)
			must.Error(t, err)

			// The chain survives, which is what this process's own logs read.
			test.ErrorIs(t, err, broken)

			// And the caller reads Internal, because nothing about a store that
			// is down is theirs to correct.
			test.EqOp(t, codes.Internal, status.Code(err))
		})
	}
}

// TestASentinelFromTheStoreOutranksTheHandlersDefault is the other half of the
// same seam, and it is why no handler on this surface switches on a sentinel.
//
// Every one of them passes codes.Internal as a *default*. The registered mapper
// runs over the preserved chain first, so a refusal the package named comes back
// as the code it was given rather than as the guess made at the call site.
func TestASentinelFromTheStoreOutranksTheHandlersDefault(T *testing.T) {
	T.Parallel()

	store := everythingFails()
	store.GetClientFunc = func(
		context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
	) (*oauth2clients.Client, error) {
		return nil, oauth2clients.ErrClientNotFound
	}

	srv := faultyServer(T, store)

	res, err := srv.GetOAuth2Client(
		withPrincipal(T.Context(), &testPrincipal{userID: testOwner, scope: testScope}),
		&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: "row_1"})
	must.Error(T, err)
	test.Nil(T, res)

	// NotFound rather than the Internal the handler named, without the handler
	// having compared anything.
	test.EqOp(T, codes.NotFound, status.Code(err))
}

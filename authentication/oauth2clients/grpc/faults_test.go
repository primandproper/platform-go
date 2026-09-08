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

// callerRow is what a read answers with when a case needs the ownership check to
// pass before the write it is actually about.
func callerRow(id string) *oauth2clients.Client {
	return &oauth2clients.Client{
		Scope:         testScope,
		BelongsToUser: testOwner,
		ID:            id,
		ClientID:      "cid_" + id,
		SecretHash:    "digest",
		Name:          "test client",
		RedirectURIs:  []string{testRedirect},
	}
}

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
		ListClientsForOwnerFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[oauth2clients.Client], error) {
			return nil, broken
		},
		CreateClientFunc: func(context.Context, database.Tx, tenancy.Scope, *oauth2clients.Client) error {
			return broken
		},
		UpdateClientFunc: func(
			context.Context, database.Tx, tenancy.Scope, string, *oauth2clients.UpdateInput,
		) error {
			return broken
		},
		ArchiveClientFunc: func(context.Context, database.Tx, tenancy.Scope, string) error {
			return broken
		},
	}
}

// onlyTheWritesFail reads back the caller's own row, so the three methods that
// check ownership before writing get past that check and fail at the write.
func onlyTheWritesFail() *oauth2clientsmock.StoreMock {
	store := everythingFails()
	store.GetClientFunc = func(
		_ context.Context, _ database.SQLQueryExecutor, _ tenancy.Scope, id string,
	) (*oauth2clients.Client, error) {
		return callerRow(id), nil
	}

	return store
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
		"update": func(ctx context.Context, s *oauth2clientsgrpc.Server) error {
			_, err := s.UpdateOAuth2Client(ctx, &oauth2clientspb.UpdateOAuth2ClientRequest{
				Oauth2ClientId: "row_1",
				Input:          updateInput("renamed"),
			})

			return err
		},
		"archive": func(ctx context.Context, s *oauth2clientsgrpc.Server) error {
			_, err := s.ArchiveOAuth2Client(ctx,
				&oauth2clientspb.ArchiveOAuth2ClientRequest{Oauth2ClientId: "row_1"})

			return err
		},
		"create own": func(ctx context.Context, s *oauth2clientsgrpc.Server) error {
			_, err := s.CreateOwnOAuth2Client(ctx,
				&oauth2clientspb.CreateOwnOAuth2ClientRequest{Input: creationInput()})

			return err
		},
		"get own": func(ctx context.Context, s *oauth2clientsgrpc.Server) error {
			_, err := s.GetOwnOAuth2Client(ctx,
				&oauth2clientspb.GetOwnOAuth2ClientRequest{Oauth2ClientId: "row_1"})

			return err
		},
		"list own": func(ctx context.Context, s *oauth2clientsgrpc.Server) error {
			_, err := s.ListOwnOAuth2Clients(ctx, &oauth2clientspb.ListOwnOAuth2ClientsRequest{})

			return err
		},
		"update own": func(ctx context.Context, s *oauth2clientsgrpc.Server) error {
			_, err := s.UpdateOwnOAuth2Client(ctx, &oauth2clientspb.UpdateOwnOAuth2ClientRequest{
				Oauth2ClientId: "row_1",
				Input:          updateInput("renamed"),
			})

			return err
		},
		"archive own": func(ctx context.Context, s *oauth2clientsgrpc.Server) error {
			_, err := s.ArchiveOwnOAuth2Client(ctx,
				&oauth2clientspb.ArchiveOwnOAuth2ClientRequest{Oauth2ClientId: "row_1"})

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

// TestTheSelfServiceWritesFailAtTheWriteRatherThanAtTheCheck is the branch the
// case above cannot reach.
//
// Both of these read the row and compare its owner before they write, so a store
// that fails the read never gets to the write — and the write's own failure
// would go unexercised, in the two methods where a swallowed error means a
// caller told their registration was revised when it was not.
func TestTheSelfServiceWritesFailAtTheWriteRatherThanAtTheCheck(T *testing.T) {
	T.Parallel()

	T.Run("update own", func(t *testing.T) {
		t.Parallel()

		srv := faultyServer(t, onlyTheWritesFail())

		res, err := srv.UpdateOwnOAuth2Client(
			withPrincipal(t.Context(), &testPrincipal{userID: testOwner, scope: testScope}),
			&oauth2clientspb.UpdateOwnOAuth2ClientRequest{
				Oauth2ClientId: "row_1",
				Input:          updateInput("renamed"),
			})
		must.Error(t, err)
		test.Nil(t, res)
		test.ErrorIs(t, err, broken)
		test.EqOp(t, codes.Internal, status.Code(err))
	})

	T.Run("archive own", func(t *testing.T) {
		t.Parallel()

		srv := faultyServer(t, onlyTheWritesFail())

		res, err := srv.ArchiveOwnOAuth2Client(
			withPrincipal(t.Context(), &testPrincipal{userID: testOwner, scope: testScope}),
			&oauth2clientspb.ArchiveOwnOAuth2ClientRequest{Oauth2ClientId: "row_1"})
		must.Error(t, err)
		test.Nil(t, res)
		test.ErrorIs(t, err, broken)
		test.EqOp(t, codes.Internal, status.Code(err))
	})
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

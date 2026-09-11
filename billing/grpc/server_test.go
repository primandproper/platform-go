package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/billingpb"
	billinggrpc "github.com/primandproper/platform-go/v14/billing/grpc"
	billingmock "github.com/primandproper/platform-go/v14/billing/mock"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestNewServerRefusesItsFourDependencies pins that each of them is required and
// which one is reported, because three of the four are wrong-quietly rather than
// wrong-loudly: a server with no principal extractor answers every request
// Unauthenticated, and one with no account authorizer would have to either
// permit or refuse every account without being asked to.
func TestNewServerRefusesItsFourDependencies(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	T.Run("no store", func(t *testing.T) {
		t.Parallel()

		_, err := billinggrpc.NewServer(nil, h.db, extractPrincipal, ownAccountAuthorizer())
		test.ErrorIs(t, err, billinggrpc.ErrNilStore)
	})

	T.Run("no database client", func(t *testing.T) {
		t.Parallel()

		_, err := billinggrpc.NewServer(h.store, nil, extractPrincipal, ownAccountAuthorizer())
		test.ErrorIs(t, err, billinggrpc.ErrNilDatabaseClient)
	})

	T.Run("no principal extractor", func(t *testing.T) {
		t.Parallel()

		_, err := billinggrpc.NewServer(h.store, h.db, nil, ownAccountAuthorizer())
		test.ErrorIs(t, err, billinggrpc.ErrNilPrincipalExtractor)
	})

	T.Run("no account authorizer", func(t *testing.T) {
		t.Parallel()

		_, err := billinggrpc.NewServer(h.store, h.db, extractPrincipal, nil)
		test.ErrorIs(t, err, billinggrpc.ErrNilAccountAuthorizer)
	})
}

// TestRegisterOnMountsTheService keeps the RegistrationFunc signature honest —
// it is what a consumer passes to grpcserver.NewGRPCServer, and a change to it
// is a compile failure here rather than in their tree.
func TestRegisterOnMountsTheService(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	srv := grpc.NewServer()
	h.server.RegisterOn(srv)

	_, mounted := srv.GetServiceInfo()[billingpb.BillingService_ServiceDesc.ServiceName]
	test.True(T, mounted, test.Sprint("RegisterOn did not mount the billing service"))
}

// TestEveryRPCRefusesAnAnonymousCaller is the one refusal that has to hold on
// all eighteen, because everything else on this surface is decided from the
// principal: the scope every store read filters on, and the account the
// authorizer is asked about.
//
// A handler that forgot the caller helper would answer with tenancy.Global's
// catalog instead, which is a real page rather than an error.
func TestEveryRPCRefusesAnAnonymousCaller(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	for name, call := range everyRPC() {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			err := call(t.Context(), h.server)
			must.Error(t, err)
			test.ErrorIs(t, err, billinggrpc.ErrNoPrincipal)
			test.EqOp(t, codes.Unauthenticated, status.Code(err))
		})
	}
}

// TestABrokenStoreFailsEveryRPC is the failure with no considered answer, and
// the reason every handler here passes codes.Internal as its default.
//
// A store that is simply down reports something no mapper claims, so the default
// is what the caller reads — which is right: there is no field to fix and no
// retry the caller can be told to make. The property worth pinning is that it is
// not swallowed into an empty page or a nil result, which is what a handler that
// dropped the error would produce.
func TestABrokenStoreFailsEveryRPC(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	srv, err := billinggrpc.NewServer(everythingFails(), h.db, extractPrincipal, ownAccountAuthorizer())
	must.NoError(T, err)

	ctx := h.ctx(T, testUser, testAccount)

	for name, call := range everyRPC() {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			callErr := call(ctx, srv)
			must.Error(t, callErr)
			test.ErrorIs(t, callErr, errBrokenStore)
			test.EqOp(t, codes.Internal, status.Code(callErr))
		})
	}
}

// everyRPC is one call per method on the service, so the two suites above assert
// their property about the whole surface rather than about whichever methods
// somebody remembered.
//
// It is checked against the generated service descriptor by
// TestEveryRPCIsExercised, which is what keeps it from silently describing
// seventeen of eighteen.
func everyRPC() map[string]func(context.Context, *billinggrpc.Server) error {
	return map[string]func(context.Context, *billinggrpc.Server) error{
		"CreateProduct": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.CreateProduct(ctx, &billingpb.CreateProductRequest{Input: creationInput()})

			return err
		},
		"GetProduct": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.GetProduct(ctx, &billingpb.GetProductRequest{ProductId: "p"})

			return err
		},
		"ListProducts": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.ListProducts(ctx, &billingpb.ListProductsRequest{})

			return err
		},
		"UpdateProduct": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.UpdateProduct(ctx, &billingpb.UpdateProductRequest{
				ProductId: "p",
				Input:     updateInput(),
			})

			return err
		},
		"ArchiveProduct": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.ArchiveProduct(ctx, &billingpb.ArchiveProductRequest{ProductId: "p"})

			return err
		},
		"GetSubscription": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.GetSubscription(ctx, &billingpb.GetSubscriptionRequest{SubscriptionId: "s"})

			return err
		},
		"ListSubscriptions": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.ListSubscriptions(ctx, &billingpb.ListSubscriptionsRequest{})

			return err
		},
		"ListSubscriptionsForAccount": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.ListSubscriptionsForAccount(ctx,
				&billingpb.ListSubscriptionsForAccountRequest{AccountId: testAccount})

			return err
		},
		"ListCurrentSubscriptions": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.ListCurrentSubscriptions(ctx,
				&billingpb.ListCurrentSubscriptionsRequest{AccountId: testAccount})

			return err
		},
		"ArchiveSubscription": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.ArchiveSubscription(ctx, &billingpb.ArchiveSubscriptionRequest{SubscriptionId: "s"})

			return err
		},
		"GetPurchase": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.GetPurchase(ctx, &billingpb.GetPurchaseRequest{PurchaseId: "p"})

			return err
		},
		"ListPurchases": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.ListPurchases(ctx, &billingpb.ListPurchasesRequest{})

			return err
		},
		"ListPurchasesForAccount": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.ListPurchasesForAccount(ctx,
				&billingpb.ListPurchasesForAccountRequest{AccountId: testAccount})

			return err
		},
		"ArchivePurchase": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.ArchivePurchase(ctx, &billingpb.ArchivePurchaseRequest{PurchaseId: "p"})

			return err
		},
		"GetTransaction": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.GetTransaction(ctx, &billingpb.GetTransactionRequest{TransactionId: "t"})

			return err
		},
		"ListTransactions": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.ListTransactions(ctx, &billingpb.ListTransactionsRequest{})

			return err
		},
		"ListTransactionsForAccount": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.ListTransactionsForAccount(ctx,
				&billingpb.ListTransactionsForAccountRequest{AccountId: testAccount})

			return err
		},
		"ArchiveTransaction": func(ctx context.Context, s *billinggrpc.Server) error {
			_, err := s.ArchiveTransaction(ctx, &billingpb.ArchiveTransactionRequest{TransactionId: "t"})

			return err
		},
	}
}

// errBrokenStore is the failure a store reports that is nobody's fault but the
// database's: not a sentinel, so no mapper claims it.
var errBrokenStore = platformerrors.New("the billing database is unreachable")

// everythingFails is a store whose every method this surface calls reports the
// same broken database.
//
// The eleven store methods that are not on the wire are left unset on purpose. A
// moq mock panics on an unset method, so a handler that reached one of them —
// a status move, a create, a lookup by a provider's identifier — fails here
// loudly rather than being answered.
func everythingFails() billing.Store {
	return &billingmock.StoreMock{
		CreateProductFunc: func(
			context.Context, database.Tx, tenancy.Scope, *billing.Product,
		) (*billing.Product, error) {
			return nil, errBrokenStore
		},
		GetProductFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*billing.Product, error) {
			return nil, errBrokenStore
		},
		ListProductsFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[billing.Product], error) {
			return nil, errBrokenStore
		},
		UpdateProductFunc: func(context.Context, database.Tx, tenancy.Scope, *billing.Product) error {
			return errBrokenStore
		},
		ArchiveProductFunc: func(context.Context, database.Tx, tenancy.Scope, string) error {
			return errBrokenStore
		},

		GetSubscriptionFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*billing.Subscription, error) {
			return nil, errBrokenStore
		},
		ListSubscriptionsFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[billing.Subscription], error) {
			return nil, errBrokenStore
		},
		ListSubscriptionsForAccountFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[billing.Subscription], error) {
			return nil, errBrokenStore
		},
		ListCurrentSubscriptionsFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[billing.Subscription], error) {
			return nil, errBrokenStore
		},
		ArchiveSubscriptionFunc: func(context.Context, database.Tx, tenancy.Scope, string) error {
			return errBrokenStore
		},

		GetPurchaseFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*billing.Purchase, error) {
			return nil, errBrokenStore
		},
		ListPurchasesFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[billing.Purchase], error) {
			return nil, errBrokenStore
		},
		ListPurchasesForAccountFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[billing.Purchase], error) {
			return nil, errBrokenStore
		},
		ArchivePurchaseFunc: func(context.Context, database.Tx, tenancy.Scope, string) error {
			return errBrokenStore
		},

		GetTransactionFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*billing.Transaction, error) {
			return nil, errBrokenStore
		},
		ListTransactionsFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[billing.Transaction], error) {
			return nil, errBrokenStore
		},
		ListTransactionsForAccountFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string, *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[billing.Transaction], error) {
			return nil, errBrokenStore
		},
		ArchiveTransactionFunc: func(context.Context, database.Tx, tenancy.Scope, string) error {
			return errBrokenStore
		},
	}
}

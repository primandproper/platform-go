package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/billing/billingpb"

	"github.com/primandproper/primitives-go/v2/database"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The one-time half: what an account bought outright.
//
// Three reads and one administrative write. billing.PurchaseStore's two other
// writes are absent: CreatePurchase is the checkout handler's, written when the
// attempt starts in the same transaction that records the payment intent, and
// CompletePurchase is the processor callback's — it stamps the provider's own
// settlement time, and it is guarded on completed_at being NULL so that a
// redelivery cannot restamp the moment the money arrived. Neither has a client
// for a caller.

// GetPurchase reads one live purchase. See GetSubscription on why the authorizer
// is asked after the read and why a refusal reads as an absence.
func (s *Server) GetPurchase(
	ctx context.Context,
	request *billingpb.GetPurchaseRequest,
) (*billingpb.GetPurchaseResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_GetPurchase_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetPurchaseId()
	req.op.Set(purchaseKey, id)

	purchase, err := s.store.GetPurchase(ctx, s.client.Reader(), req.scope, id)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading purchase %q", id)

		return nil, err
	}

	req.op.Set(accountKey, purchase.BelongsToAccount)

	if err = s.authorizeRowOwner(ctx, req, purchase.BelongsToAccount,
		"reading purchase %q", id); err != nil {
		return nil, err
	}

	return &billingpb.GetPurchaseResponse{Result: PurchaseToProto(purchase)}, nil
}

// ListPurchases pages every purchase in the caller's scope. It is the operator's
// read; see ListSubscriptions.
func (s *Server) ListPurchases(
	ctx context.Context,
	request *billingpb.ListPurchasesRequest,
) (*billingpb.ListPurchasesResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_ListPurchases_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a purchase page")

		return nil, err
	}

	page, err := s.store.ListPurchases(ctx, s.client.Reader(), req.scope, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing purchases")

		return nil, err
	}

	return &billingpb.ListPurchasesResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    PurchasesToProto(page.Data),
	}, nil
}

// ListPurchasesForAccount pages one account's purchases.
func (s *Server) ListPurchasesForAccount(
	ctx context.Context,
	request *billingpb.ListPurchasesForAccountRequest,
) (*billingpb.ListPurchasesForAccountResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_ListPurchasesForAccount_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	accountID := request.GetAccountId()
	req.op.Set(accountKey, accountID)

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a purchase page")

		return nil, err
	}

	if err = s.requireAccount(req, accountID, "listing an account's purchases"); err != nil {
		return nil, err
	}

	if err = s.authorizeNamedAccount(ctx, req, accountID,
		"authorizing the caller against account %q", accountID); err != nil {
		return nil, err
	}

	page, err := s.store.ListPurchasesForAccount(ctx, s.client.Reader(), req.scope, accountID, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing purchases for account %q", accountID)

		return nil, err
	}

	return &billingpb.ListPurchasesForAccountResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    PurchasesToProto(page.Data),
	}, nil
}

// ArchivePurchase retires a purchase administratively.
//
// It is not a refund. A refund is a transaction of its own, carrying the amount
// returned, and it arrives through the ledger; this hides a row that should not
// have been written.
func (s *Server) ArchivePurchase(
	ctx context.Context,
	request *billingpb.ArchivePurchaseRequest,
) (*billingpb.ArchivePurchaseResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_ArchivePurchase_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetPurchaseId()
	req.op.Set(purchaseKey, id)

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.store.ArchivePurchase(ctx, tx, req.scope, id)
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "archiving purchase %q", id)

		return nil, err
	}

	return &billingpb.ArchivePurchaseResponse{}, nil
}

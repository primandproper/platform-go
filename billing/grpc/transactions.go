package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/billing/billingpb"

	"github.com/primandproper/primitives-go/v2/database"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The ledger: what each attempt to move money left behind.
//
// Three reads and one administrative write, and neither of the table's two real
// writes. RecordTransaction is the write this whole schema is shaped around — a
// payment provider's event, arriving possibly twice, in the same transaction as
// the audit entry naming who was billed and the outbox event somebody fans out —
// and SetTransactionStatus is the guarded move that answers a redelivery with
// billing.ErrStatusUnchanged. An RPC for either would move the write out of the
// transaction that was the entire point of it.

// GetTransaction reads one live ledger row. See GetSubscription on why the
// authorizer is asked after the read and why a refusal reads as an absence —
// which matters most here, because a ledger id is the identifier somebody
// enumerating would most like to be told is real.
func (s *Server) GetTransaction(
	ctx context.Context,
	request *billingpb.GetTransactionRequest,
) (*billingpb.GetTransactionResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_GetTransaction_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetTransactionId()
	req.op.Set(transactionKey, id)

	transaction, err := s.store.GetTransaction(ctx, s.client.Reader(), req.scope, id)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading transaction %q", id)

		return nil, err
	}

	req.op.Set(accountKey, transaction.BelongsToAccount)

	if err = s.authorizeRowOwner(ctx, req, transaction.BelongsToAccount,
		"reading transaction %q", id); err != nil {
		return nil, err
	}

	return &billingpb.GetTransactionResponse{Result: TransactionToProto(transaction)}, nil
}

// ListTransactions pages every ledger row in the caller's scope, which is what a
// reconciliation walks.
//
// It is the sharpest read on this surface — every account's money, in the order
// the attempts were made — and PermissionListAllTransactions is the grant that
// says so.
func (s *Server) ListTransactions(
	ctx context.Context,
	request *billingpb.ListTransactionsRequest,
) (*billingpb.ListTransactionsResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_ListTransactions_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a transaction page")

		return nil, err
	}

	page, err := s.store.ListTransactions(ctx, s.client.Reader(), req.scope, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing transactions")

		return nil, err
	}

	return &billingpb.ListTransactionsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    TransactionsToProto(page.Data),
	}, nil
}

// ListTransactionsForAccount pages one account's ledger, oldest first by
// default, which is the order the attempts were made in.
func (s *Server) ListTransactionsForAccount(
	ctx context.Context,
	request *billingpb.ListTransactionsForAccountRequest,
) (*billingpb.ListTransactionsForAccountResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_ListTransactionsForAccount_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	accountID := request.GetAccountId()
	req.op.Set(accountKey, accountID)

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a transaction page")

		return nil, err
	}

	if err = s.requireAccount(req, accountID, "listing an account's ledger"); err != nil {
		return nil, err
	}

	if err = s.authorizeNamedAccount(ctx, req, accountID,
		"authorizing the caller against account %q", accountID); err != nil {
		return nil, err
	}

	page, err := s.store.ListTransactionsForAccount(ctx, s.client.Reader(), req.scope, accountID, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing transactions for account %q", accountID)

		return nil, err
	}

	return &billingpb.ListTransactionsForAccountResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    TransactionsToProto(page.Data),
	}, nil
}

// ArchiveTransaction retires a ledger row administratively.
//
// It exists for the row written in error — a test charge, a duplicate that
// predates the uniqueness — and not for a refund, which is a transaction of its
// own.
func (s *Server) ArchiveTransaction(
	ctx context.Context,
	request *billingpb.ArchiveTransactionRequest,
) (*billingpb.ArchiveTransactionResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_ArchiveTransaction_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetTransactionId()
	req.op.Set(transactionKey, id)

	// The row the store answers with is discarded, for the reason ArchiveProduct
	// discards a product: this response has no field for a ledger row.
	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		_, archiveErr := s.store.ArchiveTransaction(ctx, tx, req.scope, id)

		return archiveErr
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "archiving transaction %q", id)

		return nil, err
	}

	return &billingpb.ArchiveTransactionResponse{}, nil
}

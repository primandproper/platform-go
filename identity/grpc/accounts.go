package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// UpdateAccount saves an account's name, time zone and billing address.
//
// Neither the billing state nor the owner moves through here — the first is the
// payment processor's to report and the second is TransferAccountOwnership —
// and the input message has no field for either.
//
// The account has to be one the caller may act on. [TargetAuthorizer] is asked
// before the write, and by default that means an account the caller holds a live
// membership in: the fragment's identity.accounts.update says this caller may
// rename an account, and this says which one.
func (s *Server) UpdateAccount(
	ctx context.Context,
	request *identitypb.UpdateAccountRequest,
) (*identitypb.UpdateAccountResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_UpdateAccount_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(accountIDKey, request.GetAccountId())

	update := accountUpdateFromProto(request.GetInput())
	if update == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(identity.ErrNilAccountUpdate, op.Logger(), op.Span(), codes.InvalidArgument, "updating an account")

		return nil, err
	}

	if err = s.authorizeAccount(ctx, op, principal, request.GetAccountId()); err != nil {
		return nil, err
	}

	account, err := s.svc.UpdateAccount(ctx, scopeOf(principal), request.GetAccountId(), update)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "updating account %q", request.GetAccountId())
	}

	return &identitypb.UpdateAccountResponse{Account: AccountToProto(account)}, nil
}

// TransferAccountOwnership moves an account to a new owner.
//
// Transferring to the owner an account already has is a no-op that still runs
// the consumer's hook, naming the same user on both sides — the honest report of
// what was asked for.
//
// Two rows are named and both are checked. The account has to be one the caller
// may act on, and so does the new owner — by default a user the caller shares a
// live account with, so an account is handed to somebody the caller can already
// see rather than to any id in the directory. Handing one to a user who belongs
// to nothing is invitation first and transfer second, which is the order that
// makes them a member before it makes them responsible.
func (s *Server) TransferAccountOwnership(
	ctx context.Context,
	request *identitypb.TransferAccountOwnershipRequest,
) (*identitypb.TransferAccountOwnershipResponse, error) {
	ctx, op, principal, done, err := s.caller(
		ctx, identitypb.IdentityService_TransferAccountOwnership_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(accountIDKey, request.GetAccountId()).Set(userIDKey, request.GetNewOwnerUserId())

	if err = s.authorizeAccount(ctx, op, principal, request.GetAccountId()); err != nil {
		return nil, err
	}

	if err = s.authorizeUser(ctx, op, principal, request.GetNewOwnerUserId()); err != nil {
		return nil, err
	}

	account, err := s.svc.TransferAccountOwnership(
		ctx, scopeOf(principal), request.GetAccountId(), request.GetNewOwnerUserId())
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "transferring ownership of account %q", request.GetAccountId())
	}

	return &identitypb.TransferAccountOwnershipResponse{Account: AccountToProto(account)}, nil
}

// GetAccount reads one account.
//
// The account has to be one the caller may act on, checked before the read.
// identity.accounts.read is a grant on the method, so without this a holder
// could read any account in the directory whose id they knew. An account in
// another directory and an account in this one the caller is not in both answer
// codes.PermissionDenied — one answer rather than two, which is what leaves a
// caller enumerating ids with nothing to tell them apart.
func (s *Server) GetAccount(
	ctx context.Context,
	request *identitypb.GetAccountRequest,
) (*identitypb.GetAccountResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_GetAccount_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(accountIDKey, request.GetAccountId())

	if err = s.authorizeAccount(ctx, op, principal, request.GetAccountId()); err != nil {
		return nil, err
	}

	account, err := s.store.GetAccount(ctx, s.client.Reader(), scopeOf(principal), request.GetAccountId())
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "reading account %q", request.GetAccountId())
	}

	return &identitypb.GetAccountResponse{Account: AccountToProto(account)}, nil
}

// ListAccounts pages every account in the directory. It is an operator's read,
// which is what its entry in Permissions says.
func (s *Server) ListAccounts(
	ctx context.Context,
	request *identitypb.ListAccountsRequest,
) (*identitypb.ListAccountsResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_ListAccounts_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := s.filterFromProto(op, request.GetFilter())
	if err != nil {
		return nil, err
	}

	page, err := s.store.ListAccounts(ctx, s.client.Reader(), scopeOf(principal), filter)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "listing accounts")
	}

	return &identitypb.ListAccountsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    AccountsToProto(page.Data),
	}, nil
}

// ListAccountsForUser pages the accounts one user belongs to.
//
// The user has to be one the caller may act on: by default themselves, or
// anybody they share a live account with. The page is still every account the
// named user belongs to, the ones the caller is not in included — the rule
// decides whether the read happens rather than what it returns, and a consumer
// for whom that disclosure matters replaces [TargetAuthorizer].
func (s *Server) ListAccountsForUser(
	ctx context.Context,
	request *identitypb.ListAccountsForUserRequest,
) (*identitypb.ListAccountsForUserResponse, error) {
	ctx, op, principal, done, err := s.caller(
		ctx, identitypb.IdentityService_ListAccountsForUser_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(userIDKey, request.GetUserId())

	filter, err := s.filterFromProto(op, request.GetFilter())
	if err != nil {
		return nil, err
	}

	if err = s.authorizeUser(ctx, op, principal, request.GetUserId()); err != nil {
		return nil, err
	}

	page, err := s.store.ListAccountsForUser(
		ctx, s.client.Reader(), scopeOf(principal), request.GetUserId(), filter)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "listing accounts for user %q", request.GetUserId())
	}

	return &identitypb.ListAccountsForUserResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    AccountsToProto(page.Data),
	}, nil
}

package grpc

import (
	"context"

	filteringgrpc "github.com/primandproper/platform-go/v14/filtering/grpc"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"google.golang.org/grpc/codes"
)

// UpdateAccount saves an account's name, time zone and billing address.
//
// Neither the billing state nor the owner moves through here — the first is the
// payment processor's to report and the second is TransferAccountOwnership —
// and the input message has no field for either.
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
		err = fail(op, identity.ErrNilAccountUpdate, codes.InvalidArgument, "updating an account")

		return nil, err
	}

	account, err := s.svc.UpdateAccount(ctx, scopeOf(principal), request.GetAccountId(), update)
	if err != nil {
		return nil, fail(op, err, codes.Internal, "updating account %q", request.GetAccountId())
	}

	return &identitypb.UpdateAccountResponse{Account: AccountToProto(account)}, nil
}

// TransferAccountOwnership moves an account to a new owner.
//
// Transferring to the owner an account already has is a no-op that still runs
// the consumer's hook, naming the same user on both sides — the honest report of
// what was asked for.
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

	account, err := s.svc.TransferAccountOwnership(
		ctx, scopeOf(principal), request.GetAccountId(), request.GetNewOwnerUserId())
	if err != nil {
		return nil, fail(op, err, codes.Internal, "transferring ownership of account %q", request.GetAccountId())
	}

	return &identitypb.TransferAccountOwnershipResponse{Account: AccountToProto(account)}, nil
}

// GetAccount reads one account.
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

	account, err := s.store.GetAccount(ctx, s.client.Reader(), scopeOf(principal), request.GetAccountId())
	if err != nil {
		return nil, fail(op, err, codes.Internal, "reading account %q", request.GetAccountId())
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
		return nil, fail(op, err, codes.Internal, "listing accounts")
	}

	return &identitypb.ListAccountsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    AccountsToProto(page.Data),
	}, nil
}

// ListAccountsForUser pages the accounts one user belongs to.
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

	page, err := s.store.ListAccountsForUser(
		ctx, s.client.Reader(), scopeOf(principal), request.GetUserId(), filter)
	if err != nil {
		return nil, fail(op, err, codes.Internal, "listing accounts for user %q", request.GetUserId())
	}

	return &identitypb.ListAccountsForUserResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    AccountsToProto(page.Data),
	}, nil
}

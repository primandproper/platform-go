package grpc

import (
	"context"

	filteringgrpc "github.com/primandproper/platform-go/v14/filtering/grpc"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"google.golang.org/grpc/codes"
)

// SetDefaultAccount marks one of the calling user's accounts as the one they
// land in.
//
// The subject is the caller: where somebody lands is theirs to choose, and an
// RPC that set it for another user would be an operator act wearing a
// self-service name. The store clears the flag from their other memberships in
// the same statement pair, so the invariant is one default per user rather than
// one per call.
func (s *Server) SetDefaultAccount(
	ctx context.Context,
	request *identitypb.SetDefaultAccountRequest,
) (*identitypb.SetDefaultAccountResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_SetDefaultAccount_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(accountIDKey, request.GetAccountId())

	membership, err := s.svc.SetDefaultAccount(
		ctx, scopeOf(principal), principal.UserID(), request.GetAccountId())
	if err != nil {
		return nil, fail(op, err, codes.Internal, "setting default account to %q", request.GetAccountId())
	}

	return &identitypb.SetDefaultAccountResponse{Membership: MembershipToProto(membership)}, nil
}

// SetMembershipRoles replaces the roles a user holds in an account.
//
// It replaces rather than merges, as the store's write does: a caller adding a
// role reads the membership and sends the union, which is visible in their code,
// where a merging setter makes revocation impossible through the same call.
func (s *Server) SetMembershipRoles(
	ctx context.Context,
	request *identitypb.SetMembershipRolesRequest,
) (*identitypb.SetMembershipRolesResponse, error) {
	ctx, op, principal, done, err := s.caller(
		ctx, identitypb.IdentityService_SetMembershipRoles_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(accountIDKey, request.GetAccountId()).Set(userIDKey, request.GetUserId())

	membership, err := s.svc.SetMembershipRoles(
		ctx, scopeOf(principal), request.GetUserId(), request.GetAccountId(), request.GetRoles())
	if err != nil {
		return nil, fail(op, err, codes.Internal,
			"setting roles of user %q in account %q", request.GetUserId(), request.GetAccountId())
	}

	return &identitypb.SetMembershipRolesResponse{Membership: MembershipToProto(membership)}, nil
}

// RemoveMembership ends a user's membership in an account.
//
// Removing the account's owner is refused with ErrLastAccountOwner, which
// reaches a client as codes.FailedPrecondition: an ownerless account fails every
// permission check that resolves through its owner. Transfer it first.
func (s *Server) RemoveMembership(
	ctx context.Context,
	request *identitypb.RemoveMembershipRequest,
) (*identitypb.RemoveMembershipResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_RemoveMembership_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(accountIDKey, request.GetAccountId()).Set(userIDKey, request.GetUserId())

	membership, err := s.svc.RemoveMembership(
		ctx, scopeOf(principal), request.GetUserId(), request.GetAccountId())
	if err != nil {
		return nil, fail(op, err, codes.Internal,
			"removing user %q from account %q", request.GetUserId(), request.GetAccountId())
	}

	return &identitypb.RemoveMembershipResponse{Membership: MembershipToProto(membership)}, nil
}

// GetMembership reads one user's standing in one account.
func (s *Server) GetMembership(
	ctx context.Context,
	request *identitypb.GetMembershipRequest,
) (*identitypb.GetMembershipResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_GetMembership_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(accountIDKey, request.GetAccountId()).Set(userIDKey, request.GetUserId())

	membership, err := s.store.GetMembership(
		ctx, s.client.Reader(), scopeOf(principal), request.GetUserId(), request.GetAccountId())
	if err != nil {
		return nil, fail(op, err, codes.Internal,
			"reading membership of user %q in account %q", request.GetUserId(), request.GetAccountId())
	}

	return &identitypb.GetMembershipResponse{Membership: MembershipToProto(membership)}, nil
}

// ListMembershipsForUser returns every live membership a user holds.
//
// Unpaged, as the store's read is: a user belongs to a handful of accounts, and
// paging a handful means a caller who forgets to loop authorizes against some of
// somebody's memberships as if the rest did not exist.
func (s *Server) ListMembershipsForUser(
	ctx context.Context,
	request *identitypb.ListMembershipsForUserRequest,
) (*identitypb.ListMembershipsForUserResponse, error) {
	ctx, op, principal, done, err := s.caller(
		ctx, identitypb.IdentityService_ListMembershipsForUser_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(userIDKey, request.GetUserId())

	memberships, err := s.store.ListMembershipsForUser(
		ctx, s.client.Reader(), scopeOf(principal), request.GetUserId())
	if err != nil {
		return nil, fail(op, err, codes.Internal, "listing memberships for user %q", request.GetUserId())
	}

	return &identitypb.ListMembershipsForUserResponse{Results: MembershipsToProto(memberships)}, nil
}

// ListAccountMembers pages an account's roster, each membership joined to the
// user who holds it.
func (s *Server) ListAccountMembers(
	ctx context.Context,
	request *identitypb.ListAccountMembersRequest,
) (*identitypb.ListAccountMembersResponse, error) {
	ctx, op, principal, done, err := s.caller(
		ctx, identitypb.IdentityService_ListAccountMembers_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(accountIDKey, request.GetAccountId())

	filter, err := s.filterFromProto(op, request.GetFilter())
	if err != nil {
		return nil, err
	}

	page, err := s.store.ListAccountMembers(
		ctx, s.client.Reader(), scopeOf(principal), request.GetAccountId(), filter)
	if err != nil {
		return nil, fail(op, err, codes.Internal, "listing members of account %q", request.GetAccountId())
	}

	return &identitypb.ListAccountMembersResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    MembershipsWithUserToProto(page.Data),
	}, nil
}

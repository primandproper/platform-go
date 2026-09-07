package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/filtering"
	"github.com/primandproper/platform-go/v14/filtering/filteringpb"
	filteringgrpc "github.com/primandproper/platform-go/v14/filtering/grpc"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	"github.com/primandproper/platform-go/v14/observability"

	"google.golang.org/grpc/codes"
)

// Register creates a user, the first account they own, and the membership
// between them, in one transaction.
//
// It is the one RPC here whose caller is not yet in the directory, and the
// principal it still requires is the registrar's: an unauthenticated public
// sign-up is a flow with policy in it — a captcha, an invitation, a rate limit,
// an email domain rule — and this service holds none of that. A consumer
// building open registration puts that policy in front of this call and gives
// the request a principal of its own, which is the same thing every RPC here
// expects and the reason the seam is an interface rather than a session.
func (s *Server) Register(
	ctx context.Context,
	request *identitypb.RegisterRequest,
) (*identitypb.RegisterResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_Register_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	user := userFromRegistrationInput(request.GetUser())
	if user == nil {
		err = fail(op, identity.ErrNilUser, codes.InvalidArgument, "registering a user")

		return nil, err
	}

	account := accountFromCreationInput(request.GetAccount())
	if account == nil {
		err = fail(op, identity.ErrNilAccount, codes.InvalidArgument, "registering a user")

		return nil, err
	}

	registration, err := s.svc.Register(ctx, scopeOf(principal), user, account, request.GetOwnerRoles())
	if err != nil {
		return nil, fail(op, err, codes.Internal, "registering a user")
	}

	op.Set(userIDKey, registration.User.ID).Set(accountIDKey, registration.Account.ID)

	return &identitypb.RegisterResponse{Registration: RegistrationToProto(registration)}, nil
}

// UpdateProfile saves the fields the calling user may change about themselves.
//
// The subject is the caller and there is no target on the request. Editing
// somebody else's profile is an operator act, and the operator acts this service
// exposes are the three on AdminWriter — a status, a service role, an archival —
// each of which is named as such and permissioned as such. A "update any user"
// RPC would be a fourth, hiding inside the one every signed-in person calls.
func (s *Server) UpdateProfile(
	ctx context.Context,
	request *identitypb.UpdateProfileRequest,
) (*identitypb.UpdateProfileResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_UpdateProfile_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	update := profileUpdateFromProto(request.GetInput())
	if update == nil {
		err = fail(op, identity.ErrNilProfileUpdate, codes.InvalidArgument, "updating a profile")

		return nil, err
	}

	user, err := s.svc.UpdateProfile(ctx, scopeOf(principal), principal.UserID(), update)
	if err != nil {
		return nil, fail(op, err, codes.Internal, "updating a profile")
	}

	return &identitypb.UpdateProfileResponse{User: UserToProto(user)}, nil
}

// RecordAgreement stamps the calling user's acceptance of one or more documents.
func (s *Server) RecordAgreement(
	ctx context.Context,
	request *identitypb.RecordAgreementRequest,
) (*identitypb.RecordAgreementResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_RecordAgreement_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	agreements, err := agreementsFromProto(request.GetAgreements())
	if err != nil {
		return nil, fail(op, err, codes.InvalidArgument, "recording agreements")
	}

	user, err := s.svc.RecordAgreement(ctx, scopeOf(principal), principal.UserID(), agreements...)
	if err != nil {
		return nil, fail(op, err, codes.Internal, "recording agreements")
	}

	return &identitypb.RecordAgreementResponse{User: UserToProto(user)}, nil
}

// ArchiveUser soft-deletes a user and ends every membership they hold.
func (s *Server) ArchiveUser(
	ctx context.Context,
	request *identitypb.ArchiveUserRequest,
) (*identitypb.ArchiveUserResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_ArchiveUser_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(userIDKey, request.GetUserId())

	user, err := s.svc.ArchiveUser(ctx, scopeOf(principal), request.GetUserId())
	if err != nil {
		return nil, fail(op, err, codes.Internal, "archiving user %q", request.GetUserId())
	}

	return &identitypb.ArchiveUserResponse{User: UserToProto(user)}, nil
}

// UpdateUserAccountStatus moves a user between statuses: a ban, a termination, a
// reinstatement.
func (s *Server) UpdateUserAccountStatus(
	ctx context.Context,
	request *identitypb.UpdateUserAccountStatusRequest,
) (*identitypb.UpdateUserAccountStatusResponse, error) {
	ctx, op, principal, done, err := s.caller(
		ctx, identitypb.IdentityService_UpdateUserAccountStatus_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(userIDKey, request.GetUserId())

	status, err := AccountStatusFromProto(request.GetStatus())
	if err != nil {
		return nil, fail(op, err, codes.InvalidArgument, "updating account status")
	}

	user, err := s.svc.UpdateUserAccountStatus(
		ctx, scopeOf(principal), request.GetUserId(), status, request.GetExplanation())
	if err != nil {
		return nil, fail(op, err, codes.Internal, "updating account status of user %q", request.GetUserId())
	}

	return &identitypb.UpdateUserAccountStatusResponse{User: UserToProto(user)}, nil
}

// SetUserServiceRoles replaces the roles a user holds outside any account.
//
// This is the write that grants and withdraws operator access, and it replaces
// rather than merges — a merging setter cannot revoke.
func (s *Server) SetUserServiceRoles(
	ctx context.Context,
	request *identitypb.SetUserServiceRolesRequest,
) (*identitypb.SetUserServiceRolesResponse, error) {
	ctx, op, principal, done, err := s.caller(
		ctx, identitypb.IdentityService_SetUserServiceRoles_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(userIDKey, request.GetUserId())

	user, err := s.svc.SetUserServiceRoles(ctx, scopeOf(principal), request.GetUserId(), request.GetRoles())
	if err != nil {
		return nil, fail(op, err, codes.Internal, "setting service roles of user %q", request.GetUserId())
	}

	return &identitypb.SetUserServiceRolesResponse{User: UserToProto(user)}, nil
}

// GetPrincipal answers "who am I and what may I do" for the calling user.
//
// It is the read a client makes on load, and the one whose shape is the reason
// identity.Principal exists: a user, their memberships and the account this
// request is against, resolved together rather than by three queries a caller
// assembles and gets the active-account check wrong in.
//
// The subject is the caller. Reading somebody else's principal is not something
// this service does — it would be an authorization oracle, answering "what may
// this person do" to anybody who can name them.
func (s *Server) GetPrincipal(
	ctx context.Context,
	request *identitypb.GetPrincipalRequest,
) (*identitypb.GetPrincipalResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_GetPrincipal_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	// The request may name one of the caller's accounts; absent falls back to
	// whatever the authentication layer resolved, and absent again to the user's
	// default. The store refuses an account the user holds no live membership
	// in, which is what keeps this from being a way to look into one.
	activeAccountID := request.GetActiveAccountId()
	if activeAccountID == "" {
		activeAccountID = principal.ActiveAccountID()
	}

	resolved, err := s.store.GetPrincipal(
		ctx, s.client.Reader(), scopeOf(principal), principal.UserID(), activeAccountID)
	if err != nil {
		return nil, fail(op, err, codes.Internal, "reading the calling principal")
	}

	return &identitypb.GetPrincipalResponse{Principal: PrincipalToProto(resolved)}, nil
}

// GetUser reads one user.
func (s *Server) GetUser(
	ctx context.Context,
	request *identitypb.GetUserRequest,
) (*identitypb.GetUserResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_GetUser_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(userIDKey, request.GetUserId())

	user, err := s.store.GetUser(ctx, s.client.Reader(), scopeOf(principal), request.GetUserId())
	if err != nil {
		return nil, fail(op, err, codes.Internal, "reading user %q", request.GetUserId())
	}

	return &identitypb.GetUserResponse{User: UserToProto(user.Redacted())}, nil
}

// ListUsers pages the directory.
func (s *Server) ListUsers(
	ctx context.Context,
	request *identitypb.ListUsersRequest,
) (*identitypb.ListUsersResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_ListUsers_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := s.filterFromProto(op, request.GetFilter())
	if err != nil {
		return nil, err
	}

	page, err := s.store.ListUsers(ctx, s.client.Reader(), scopeOf(principal), filter)
	if err != nil {
		return nil, fail(op, err, codes.Internal, "listing users")
	}

	return &identitypb.ListUsersResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    UsersToProto(page.Data),
	}, nil
}

// SearchUsersByUsername pages the users whose username starts with a prefix.
func (s *Server) SearchUsersByUsername(
	ctx context.Context,
	request *identitypb.SearchUsersByUsernameRequest,
) (*identitypb.SearchUsersByUsernameResponse, error) {
	ctx, op, principal, done, err := s.caller(
		ctx, identitypb.IdentityService_SearchUsersByUsername_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := s.filterFromProto(op, request.GetFilter())
	if err != nil {
		return nil, err
	}

	page, err := s.store.SearchUsersByUsername(
		ctx, s.client.Reader(), scopeOf(principal), request.GetPrefix(), filter)
	if err != nil {
		return nil, fail(op, err, codes.Internal, "searching users")
	}

	return &identitypb.SearchUsersByUsernameResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    UsersToProto(page.Data),
	}, nil
}

// filterFromProto reads a query filter, in the one place every paged read does.
//
// A malformed filter is codes.InvalidArgument rather than the Internal every
// other failure here defaults to: it is the one thing on these requests a client
// can get wrong on its own, and the filtering converters already distinguish it.
// An absent filter is the default page rather than an error.
func (s *Server) filterFromProto(
	op observability.Operation,
	in *filteringpb.QueryFilter,
) (*filtering.QueryFilter, error) {
	filter, err := filteringgrpc.FromProto(in)
	if err != nil {
		return nil, fail(op, err, codes.InvalidArgument, "reading the query filter")
	}

	return filter, nil
}

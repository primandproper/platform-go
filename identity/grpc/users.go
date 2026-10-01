package grpc

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	"github.com/primandproper/platform-go/v14/internal/archivegate"

	"github.com/primandproper/primitives-go/v2/authorization"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"
	"github.com/primandproper/primitives-go/v2/observability"

	"google.golang.org/grpc/codes"
)

// UpdateProfile saves the fields the calling user may change about themselves.
//
// The subject is the caller and there is no target on the request. Editing
// somebody else's profile is an operator act, and the operator acts this service
// exposes are named as such and permissioned as such: the three on AdminWriter —
// a status, a service role, an archival — and SetUserRequiresPasswordChange,
// which is on the credential writer rather than AdminWriter because it is not a
// privilege escalation to hold. A "update any user" RPC would be one more,
// hiding inside the one every signed-in person calls.
//
// The username and the email address are not among those fields unless the
// server was built [WithoutReauthenticatedHandles]: a save naming either is
// refused whole, with identity.ErrHandleChangeRequiresReauthentication, before
// anything is written. Both are credentials in all but name, and a session is
// not proof enough to move one — authentication/signin's UpdateUsername and
// UpdateEmailAddress are where they change.
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
		err = grpcerrors.PrepareAndLogGRPCStatus(identity.ErrNilProfileUpdate, op.Logger(), op.Span(), codes.InvalidArgument, "updating a profile")

		return nil, err
	}

	// Refused whole rather than with the handles dropped: a client whose save
	// silently kept the old address would show the person a change that did
	// not happen.
	if !s.handlesUngated && (update.Username != nil || update.EmailAddress != nil) {
		err = grpcerrors.PrepareAndLogGRPCStatus(identity.ErrHandleChangeRequiresReauthentication, op.Logger(), op.Span(),
			codes.InvalidArgument, "updating a profile")

		return nil, err
	}

	user, err := s.svc.UpdateProfile(ctx, scopeOf(principal), principal.UserID(), update)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "updating a profile")
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
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.InvalidArgument, "recording agreements")
	}

	user, err := s.svc.RecordAgreement(ctx, scopeOf(principal), principal.UserID(), agreements...)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "recording agreements")
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
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "archiving user %q", request.GetUserId())
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
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.InvalidArgument, "updating account status")
	}

	user, err := s.svc.UpdateUserAccountStatus(
		ctx, scopeOf(principal), request.GetUserId(), status, request.GetExplanation())
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "updating account status of user %q", request.GetUserId())
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
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "setting service roles of user %q", request.GetUserId())
	}

	return &identitypb.SetUserServiceRolesResponse{User: UserToProto(user)}, nil
}

// SetUserRequiresPasswordChange forces a password change at a user's next
// sign-in, or releases one.
//
// It is the fourth operator write and the only one whose subject is a
// credential rather than the row itself — and it still carries no credential,
// which is why it is served here and not by the sign-in service. It assigns a
// boolean; the sign-in service reads it, and a password chosen through
// SignInService.UpdatePassword clears it.
//
// An absent requires_password_change is refused rather than read as false, for
// the reason [AccountStatusFromProto] refuses UNSPECIFIED: the value a
// forgotten field carries is the one that undoes somebody's decision, and this
// service will not perform that on the strength of a field nobody set.
func (s *Server) SetUserRequiresPasswordChange(
	ctx context.Context,
	request *identitypb.SetUserRequiresPasswordChangeRequest,
) (*identitypb.SetUserRequiresPasswordChangeResponse, error) {
	ctx, op, principal, done, err := s.caller(
		ctx, identitypb.IdentityService_SetUserRequiresPasswordChange_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(userIDKey, request.GetUserId())

	if request.RequiresPasswordChange == nil {
		err = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue,
			"password change requirement is unset")

		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(),
			codes.InvalidArgument, "setting the password change requirement of user %q", request.GetUserId())
	}

	user, err := s.svc.SetUserRequiresPasswordChange(
		ctx, scopeOf(principal), request.GetUserId(), request.GetRequiresPasswordChange())
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "setting the password change requirement of user %q", request.GetUserId())
	}

	return &identitypb.SetUserRequiresPasswordChangeResponse{User: UserToProto(user)}, nil
}

// GetPrincipal answers "who am I and what may I do" for the calling user.
//
// The second half is answered only by a server built WithPermissionResolver;
// see that option for what is resolved, and GetPrincipalResponse.permissions
// for what a client may and may not conclude from it.
//
// It is the read a client makes on load, and the one whose shape is the reason
// identity.Principal exists: a user, their memberships and the account this
// request is against, resolved together rather than by three queries a caller
// assembles and gets the active-account check wrong in.
//
// The subject is the caller. Reading somebody else's principal is not something
// this service does — it would be an authorization oracle, answering "what may
// this person do" to anybody who can name them.
//
// It is also where account status is enforced, and an interceptor does not repeat
// the check. The store refuses a user whose AccountStatus does not admit sign-in
// with identity.ErrSignInNotAdmitted, which GRPCMapper answers as
// PermissionDenied, so a ban applied while somebody held a valid access token
// takes effect on their next call here rather than at the token's expiry.
// Nothing about the credential is re-examined: a consumer's authentication
// interceptor still decides whether a request is authenticated at all, and this
// method decides whether the directory will answer for whoever it said they were.
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
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "reading the calling principal")
	}

	response := &identitypb.GetPrincipalResponse{Principal: PrincipalToProto(resolved)}

	// The active account rides along because "which account am I in" is the
	// question a client asks next, and GetAccount answers it only for a caller
	// holding a directory grant. The store has just checked the caller is a live
	// member of this account, and that check is the authorization for reading
	// it. A caller with no memberships resolved no account, and gets none.
	if resolved.ActiveAccountID != "" {
		var account *identity.Account

		account, err = s.store.GetAccount(ctx, s.client.Reader(), scopeOf(principal), resolved.ActiveAccountID)
		if err != nil {
			return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "reading the calling principal's active account")
		}

		response.ActiveAccount = AccountToProto(account)
	}

	if s.permissions != nil {
		response.Permissions, err = s.effectivePermissions(ctx, principal, resolved)
		if err != nil {
			return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "resolving the calling principal's permissions")
		}
	}

	return response, nil
}

// effectivePermissions is what caller may do in the account resolved names:
// the service roles the session carries, and the roles the directory holds for
// the caller in that account, resolved together.
//
// The service roles are read off the request's principal and never off
// resolved, whose are the directory's. The two differ exactly when it matters —
// an ordinary-door session of somebody who holds a service role — and the
// directory's answer would tell that session it may do what every call it
// makes is refused.
func (s *Server) effectivePermissions(
	ctx context.Context,
	caller callers.Principal,
	resolved *identity.Principal,
) (*identitypb.EffectivePermissions, error) {
	roles := append(sessionServiceRoles(caller), resolved.AccountRoles()...)

	granted, err := s.permissions.PermissionsForRoles(ctx, roles...)
	if err != nil {
		return nil, err
	}

	permissions := make([]string, 0, granted.Len())
	for _, permission := range granted.Slice() {
		permissions = append(permissions, string(permission))
	}

	return &identitypb.EffectivePermissions{Permissions: permissions}, nil
}

// sessionServiceRoles are the service roles a request's principal carries: the
// ones on the directory's answer it holds, if it holds one. The slice is a copy,
// so appending to it cannot write into the principal.
func sessionServiceRoles(caller callers.Principal) []string {
	carrier, ok := caller.(interface{ Identity() *identity.Principal })
	if !ok {
		return nil
	}

	return slices.Clone(carrier.Identity().ServiceRoles())
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
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "reading user %q", request.GetUserId())
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

	filter, err := s.filterFromProto(ctx, op, request.GetFilter(), PermissionArchiveUsers)
	if err != nil {
		return nil, err
	}

	page, err := s.store.ListUsers(ctx, s.client.Reader(), scopeOf(principal), filter)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "listing users")
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

	filter, err := s.filterFromProto(ctx, op, request.GetFilter(), PermissionArchiveUsers)
	if err != nil {
		return nil, err
	}

	page, err := s.store.SearchUsersByUsername(
		ctx, s.client.Reader(), scopeOf(principal), request.GetPrefix(), filter)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "searching users")
	}

	return &identitypb.SearchUsersByUsernameResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    UsersToProto(page.Data),
	}, nil
}

// filterFromProto reads a query filter, in the one place every paged read does,
// and narrows include_archived to a caller holding archiveGrant.
//
// The field is a request rather than an instruction. Each read names the grant
// that archives what it pages: a user is archived under
// [PermissionArchiveUsers], an account under [PermissionArchiveAccounts], and a
// membership is ended under [PermissionManageMembers] — so whoever may take a
// row out of the directory may see the rows taken out, and a caller holding the
// read grant alone sees the live ones. An invitation is never archived, being
// answered or cancelled by its status instead, so the invitation reads name
// archivegate.NothingArchived and the field is cleared for everybody. The
// narrowing, and why it is not a refusal, is internal/archivegate's.
//
// A malformed filter is codes.InvalidArgument rather than the Internal every
// other failure here defaults to: it is the one thing on these requests a client
// can get wrong on its own. An absent filter is the default page rather than an
// error.
func (s *Server) filterFromProto(
	ctx context.Context,
	op observability.Operation,
	in *filteringpb.QueryFilter,
	archiveGrant authorization.Permission,
) (*filtering.QueryFilter, error) {
	return archivegate.Filter(ctx, op, in, s.grants, archiveGrant, archivedClearedKey, "reading the query filter")
}

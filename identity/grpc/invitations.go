package grpc

import (
	"context"
	"time"

	filteringgrpc "github.com/primandproper/platform-go/v14/filtering/grpc"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"google.golang.org/grpc/codes"
)

// Invite issues an invitation to an account.
//
// The token is minted here rather than sent by the client, and that is the one
// piece of machinery this transport owns that the service layer does not.
// identity.Service.Invite takes an invitation with a token already on it,
// because the package holds no policy about links; a client that could choose
// the token could choose a guessable one, or somebody else's. So the server
// mints it — with the CSPRNG by default, or whatever WithTokenMinter supplied —
// and it goes out with the invitation to the consumer's AfterInvite hook, which
// is where a link gets queued for mailing. It is not returned to the sender:
// the response carries the redacted invitation, and the token reaches only the
// address it was minted for.
//
// The expiry is the request's when it names one and the configured lifetime
// when it does not. A transport cannot decline to pick, because an invitation
// with no expiry is a link that works forever. A named expiry is held to the
// same bound the default is: it has to be later than now, or the hook mails a
// dead link, and no further ahead than the maximum lifetime, or the request is
// the way around the default. Either is codes.InvalidArgument.
func (s *Server) Invite(
	ctx context.Context,
	request *identitypb.InviteRequest,
) (*identitypb.InviteResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_Invite_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(accountIDKey, request.GetAccountId())

	token, err := s.mintToken(ctx)
	if err != nil {
		return nil, fail(op, err, codes.Internal, "minting an invitation token")
	}

	// The client's clock, not one of this package's: it is the seam the module
	// names for a component that stamps a row, and it is what a test that
	// controls time already controls. UTC, like every other timestamp this
	// module writes.
	now := s.client.CurrentTime().UTC()

	expiresAt := timeFromProto(request.GetExpiresAt())

	switch {
	case expiresAt.IsZero():
		expiresAt = now.Add(s.invitationTTL)
	case !expiresAt.After(now):
		err = fail(op, ErrInvitationExpiryInPast, codes.InvalidArgument,
			"invitation expiry %s is not after %s", expiresAt.Format(time.RFC3339), now.Format(time.RFC3339))

		return nil, err
	case expiresAt.After(now.Add(s.maxInvitationTTL)):
		err = fail(op, ErrInvitationExpiryTooFar, codes.InvalidArgument,
			"invitation expiry %s is more than %s ahead", expiresAt.Format(time.RFC3339), s.maxInvitationTTL)

		return nil, err
	}

	invitation := &identity.Invitation{
		BelongsToAccount: request.GetAccountId(),
		FromUser:         principal.UserID(),
		ToEmail:          request.GetToEmail(),
		ToName:           request.GetToName(),
		Note:             request.GetNote(),
		Roles:            request.GetRoles(),
		Token:            token,
		ExpiresAt:        expiresAt,
	}

	if err = s.svc.Invite(ctx, scopeOf(principal), invitation); err != nil {
		return nil, fail(op, err, codes.Internal, "issuing an invitation")
	}

	op.Set(invitationIDKey, invitation.ID)

	return &identitypb.InviteResponse{Invitation: InvitationToProto(invitation.Redacted())}, nil
}

// AcceptInvitation answers an invitation and mints the membership it promised.
//
// The acceptor is the caller. The roles come from the invitation and never from
// this request — what somebody was invited to is what they get, and a parameter
// here is where an escalation goes in.
func (s *Server) AcceptInvitation(
	ctx context.Context,
	request *identitypb.AcceptInvitationRequest,
) (*identitypb.AcceptInvitationResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_AcceptInvitation_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(invitationIDKey, request.GetInvitationId())

	acceptance, err := s.svc.AcceptInvitation(
		ctx,
		scopeOf(principal),
		request.GetInvitationId(),
		request.GetToken(),
		principal.UserID(),
		request.GetStatusNote(),
	)
	if err != nil {
		return nil, fail(op, err, codes.Internal, "accepting invitation %q", request.GetInvitationId())
	}

	return &identitypb.AcceptInvitationResponse{Acceptance: AcceptanceToProto(acceptance)}, nil
}

// RejectInvitation declines an invitation on the recipient's behalf.
//
// The token is checked before the status is written, which is what keeps a
// rejection from being something anybody holding an invitation's id can do.
func (s *Server) RejectInvitation(
	ctx context.Context,
	request *identitypb.RejectInvitationRequest,
) (*identitypb.RejectInvitationResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_RejectInvitation_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(invitationIDKey, request.GetInvitationId())

	invitation, err := s.svc.RejectInvitation(
		ctx, scopeOf(principal), request.GetInvitationId(), request.GetToken(), request.GetStatusNote())
	if err != nil {
		return nil, fail(op, err, codes.Internal, "rejecting invitation %q", request.GetInvitationId())
	}

	return &identitypb.RejectInvitationResponse{Invitation: InvitationToProto(invitation)}, nil
}

// CancelInvitation withdraws an invitation on the sender's behalf.
//
// No token, because the sender never had one: they are looking at what they
// sent, addressed by id. Nothing here checks that this caller is that sender.
// The permission the fragment asks for, PermissionInviteMembers, is a grant on
// the method and not on the row, so a holder of it may cancel any pending
// invitation in the directory, and Invitation.FromUser is the field a consumer
// who wants the narrower rule compares — though the enforcer this module ships
// hands its interceptor no row to compare it against, so today that check is
// the consumer's own interceptor's to make, ahead of this handler.
//
// An invitation that has already been answered reads as absent: the status write
// matches only a pending row, which is what makes a cancellation that raced an
// acceptance leave the acceptance standing.
func (s *Server) CancelInvitation(
	ctx context.Context,
	request *identitypb.CancelInvitationRequest,
) (*identitypb.CancelInvitationResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_CancelInvitation_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(invitationIDKey, request.GetInvitationId())

	invitation, err := s.svc.CancelInvitation(
		ctx, scopeOf(principal), request.GetInvitationId(), request.GetStatusNote())
	if err != nil {
		return nil, fail(op, err, codes.Internal, "cancelling invitation %q", request.GetInvitationId())
	}

	return &identitypb.CancelInvitationResponse{Invitation: InvitationToProto(invitation)}, nil
}

// GetInvitation reads one invitation, redacted.
func (s *Server) GetInvitation(
	ctx context.Context,
	request *identitypb.GetInvitationRequest,
) (*identitypb.GetInvitationResponse, error) {
	ctx, op, principal, done, err := s.caller(ctx, identitypb.IdentityService_GetInvitation_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	op.Set(invitationIDKey, request.GetInvitationId())

	invitation, err := s.store.GetInvitation(
		ctx, s.client.Reader(), scopeOf(principal), request.GetInvitationId())
	if err != nil {
		return nil, fail(op, err, codes.Internal, "reading invitation %q", request.GetInvitationId())
	}

	return &identitypb.GetInvitationResponse{Invitation: InvitationToProto(invitation.Redacted())}, nil
}

// ListInvitationsFromUser pages what the calling user has sent.
func (s *Server) ListInvitationsFromUser(
	ctx context.Context,
	request *identitypb.ListInvitationsFromUserRequest,
) (*identitypb.ListInvitationsFromUserResponse, error) {
	ctx, op, principal, done, err := s.caller(
		ctx, identitypb.IdentityService_ListInvitationsFromUser_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := s.filterFromProto(op, request.GetFilter())
	if err != nil {
		return nil, err
	}

	page, err := s.store.ListInvitationsFromUser(
		ctx, s.client.Reader(), scopeOf(principal), principal.UserID(), filter)
	if err != nil {
		return nil, fail(op, err, codes.Internal, "listing sent invitations")
	}

	return &identitypb.ListInvitationsFromUserResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    InvitationsToProto(page.Data),
	}, nil
}

// ListInvitationsForEmailAddress pages what the calling user has been sent.
//
// The address is read off the caller's own user row rather than taken from the
// request. An address a client could name would answer "has this person been
// invited anywhere" to anybody who can guess an email address, which is a
// membership-graph oracle wearing an inbox's clothes.
//
// The row's address has to be verified, and that is the same oracle closed from
// the other side. UpdateProfile is self-service and takes an email address, and
// the service clears the address's verification when it moves, leaving proving
// it to the sign-in flow — so an unverified address on the caller's row is one
// the caller typed a moment ago, and reading invitations by it would answer the
// same question for the price of two RPCs instead of one. An unverified caller
// is refused with ErrEmailAddressUnverified rather than shown an empty page,
// because an empty page reads as "nobody has invited you", which is not what
// the server knows.
func (s *Server) ListInvitationsForEmailAddress(
	ctx context.Context,
	request *identitypb.ListInvitationsForEmailAddressRequest,
) (*identitypb.ListInvitationsForEmailAddressResponse, error) {
	ctx, op, principal, done, err := s.caller(
		ctx, identitypb.IdentityService_ListInvitationsForEmailAddress_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := s.filterFromProto(op, request.GetFilter())
	if err != nil {
		return nil, err
	}

	scope := scopeOf(principal)

	caller, err := s.store.GetUser(ctx, s.client.Reader(), scope, principal.UserID())
	if err != nil {
		return nil, fail(op, err, codes.Internal, "reading the calling user")
	}

	if !caller.EmailAddressVerified() {
		err = fail(op, ErrEmailAddressUnverified, codes.FailedPrecondition,
			"listing invitations for an address the caller has not verified")

		return nil, err
	}

	page, err := s.store.ListInvitationsForEmailAddress(
		ctx, s.client.Reader(), scope, caller.EmailAddress, filter)
	if err != nil {
		return nil, fail(op, err, codes.Internal, "listing received invitations")
	}

	return &identitypb.ListInvitationsForEmailAddressResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    InvitationsToProto(page.Data),
	}, nil
}

package identity

import (
	"slices"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func invitations(t *testing.T, s *conformance.Session) {
	t.Helper()

	role, otherRole := membershipRoles(s)

	t.Run("an invitation is kept, and its token comes back to the sender only if the deployment says so", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t, conformance.Making(invite, getInvitation))
		needsAccount(t, sender)

		response, err := sender.Surfaces.Identity.Invite(sender.Context(t.Context()), &identitypb.InviteRequest{
			AccountId: sender.AccountID,
			ToEmail:   freshEmail(),
			Roles:     []string{role},
		})
		must.NoError(t, err)

		invitation := response.GetInvitation()
		token := tokenFor(t, s, sender, invitation.GetId())

		// Either way, never on the invitation itself.
		test.StrNotContains(t, invitation.String(), token,
			test.Sprint("the invitation's token came back on the invitation"))

		if s.Seams().InvitationTokenReturned {
			test.EqOp(t, token, response.GetToken(),
				test.Sprint("a deployment that returns the token returned one other than it delivered"))
		} else {
			test.StrNotContains(t, response.String(), token,
				test.Sprint("the invitation's token came back to the sender (Seams.InvitationTokenReturned is false)"))
		}

		read, err := sender.Surfaces.Identity.GetInvitation(sender.Context(t.Context()),
			&identitypb.GetInvitationRequest{InvitationId: invitation.GetId()})
		must.NoError(t, err)
		test.EqOp(t, sender.UserID, read.GetInvitation().GetFromUser())
		test.EqOp(t, identitypb.InvitationStatus_INVITATION_STATUS_PENDING, read.GetInvitation().GetStatus())
		test.StrNotContains(t, read.GetInvitation().String(), token)
	})

	t.Run("an invitation expires when the request says", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t, conformance.Making(invite))
		needsAccount(t, sender)

		// Whole seconds, because a timestamp is compared exactly here and the
		// weakest dialect keeps no more than that.
		asked := time.Now().UTC().Add(3 * time.Hour).Truncate(time.Second)

		response, err := sender.Surfaces.Identity.Invite(sender.Context(t.Context()), &identitypb.InviteRequest{
			AccountId: sender.AccountID,
			ToEmail:   freshEmail(),
			Roles:     []string{role},
			ExpiresAt: timestamppb.New(asked),
		})
		must.NoError(t, err)
		test.EqOp(t, asked, response.GetInvitation().GetExpiresAt().AsTime())
	})

	t.Run("an expiry in the past is refused", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t, conformance.Making(invite))
		needsAccount(t, sender)

		_, err := sender.Surfaces.Identity.Invite(sender.Context(t.Context()), &identitypb.InviteRequest{
			AccountId: sender.AccountID,
			ToEmail:   freshEmail(),
			Roles:     []string{role},
			ExpiresAt: timestamppb.New(time.Now().UTC().Add(-time.Minute)),
		})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("accepting an invitation mints the membership it promised, once", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t, conformance.Making(invite))
		needsAccount(t, sender)
		invitee := colleague(t, s, sender, conformance.Making(getPrincipal, acceptInvitation))

		invitation := sendInvitation(t, sender, self(t, invitee).GetEmailAddress(), role, otherRole)
		token := tokenFor(t, s, sender, invitation.GetId())
		ctx := invitee.Context(t.Context())

		response, err := invitee.Surfaces.Identity.AcceptInvitation(ctx, &identitypb.AcceptInvitationRequest{
			InvitationId: invitation.GetId(),
			Token:        token,
			StatusNote:   "glad to be here",
		})
		must.NoError(t, err)

		acceptance := response.GetAcceptance()
		must.NotNil(t, acceptance.GetInvitation())
		must.NotNil(t, acceptance.GetMembership())
		test.EqOp(t, identitypb.InvitationStatus_INVITATION_STATUS_ACCEPTED, acceptance.GetInvitation().GetStatus())
		test.EqOp(t, invitee.UserID, acceptance.GetInvitation().GetToUser())
		test.EqOp(t, invitee.UserID, acceptance.GetMembership().GetBelongsToUser())
		test.EqOp(t, sender.AccountID, acceptance.GetMembership().GetBelongsToAccount())
		test.Eq(t, slices.Sorted(slices.Values([]string{role, otherRole})),
			slices.Sorted(slices.Values(acceptance.GetMembership().GetRoles())))
		test.StrNotContains(t, acceptance.GetInvitation().String(), token)

		// Accepted once. The token has been spent, and a second acceptance
		// reads as the absence it now is.
		_, err = invitee.Surfaces.Identity.AcceptInvitation(ctx, &identitypb.AcceptInvitationRequest{
			InvitationId: invitation.GetId(),
			Token:        token,
		})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("accepting with the wrong token is refused as absent", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t, conformance.Making(invite))
		invitee := colleague(t, s, sender, conformance.Making(getPrincipal, acceptInvitation))
		invitation := sendInvitation(t, sender, self(t, invitee).GetEmailAddress(), role)

		_, err := invitee.Surfaces.Identity.AcceptInvitation(invitee.Context(t.Context()),
			&identitypb.AcceptInvitationRequest{InvitationId: invitation.GetId(), Token: "not the token"})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("the right token under somebody else's address is refused as absent", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t, conformance.Making(invite, listAccountMembers))
		needsAccount(t, sender)
		addressee := colleague(t, s, sender, conformance.Making(getPrincipal, acceptInvitation))
		holder := colleague(t, s, sender, conformance.Making(acceptInvitation))

		invitation := sendInvitation(t, sender, self(t, addressee).GetEmailAddress(), role)
		token := tokenFor(t, s, sender, invitation.GetId())

		// The token leaks wherever the mail and the events about it go. The
		// address is what stops a leaked one admitting whoever holds it, and
		// the refusal is a wrong token's, so the holder learns nothing about
		// which half they got right.
		_, err := holder.Surfaces.Identity.AcceptInvitation(holder.Context(t.Context()),
			&identitypb.AcceptInvitationRequest{InvitationId: invitation.GetId(), Token: token})
		must.Error(t, err, must.Sprint("an invitation admitted somebody it was not addressed to"))
		test.EqOp(t, codes.NotFound, status.Code(err))
		test.SliceNotContains(t, memberIDs(t, sender, sender.AccountID), holder.UserID)

		// The positive control: the addressee answers the same link.
		_, err = addressee.Surfaces.Identity.AcceptInvitation(addressee.Context(t.Context()),
			&identitypb.AcceptInvitationRequest{InvitationId: invitation.GetId(), Token: token})
		must.NoError(t, err, must.Sprint("the addressee cannot accept; the refusal above proves nothing"))
		test.SliceContains(t, memberIDs(t, sender, sender.AccountID), addressee.UserID)
	})

	t.Run("a rejection checks the token before it writes", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t, conformance.Making(invite, getInvitation))
		invitee := colleague(t, s, sender, conformance.Making(getPrincipal, rejectInvitation))
		invitation := sendInvitation(t, sender, self(t, invitee).GetEmailAddress(), role)
		ctx := invitee.Context(t.Context())

		_, err := invitee.Surfaces.Identity.RejectInvitation(ctx,
			&identitypb.RejectInvitationRequest{InvitationId: invitation.GetId(), Token: "not the token"})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))

		read, err := sender.Surfaces.Identity.GetInvitation(sender.Context(t.Context()),
			&identitypb.GetInvitationRequest{InvitationId: invitation.GetId()})
		must.NoError(t, err)
		test.EqOp(t, identitypb.InvitationStatus_INVITATION_STATUS_PENDING, read.GetInvitation().GetStatus(),
			test.Sprint("a rejection with the wrong token changed the invitation anyway"))

		response, err := invitee.Surfaces.Identity.RejectInvitation(ctx, &identitypb.RejectInvitationRequest{
			InvitationId: invitation.GetId(),
			Token:        tokenFor(t, s, sender, invitation.GetId()),
			StatusNote:   "no thank you",
		})
		must.NoError(t, err)
		test.EqOp(t, identitypb.InvitationStatus_INVITATION_STATUS_REJECTED, response.GetInvitation().GetStatus())
		test.EqOp(t, "no thank you", response.GetInvitation().GetStatusNote())
		test.EqOp(t, "come and join us", response.GetInvitation().GetNote())
	})

	t.Run("a cancellation takes no token, and happens once", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t, conformance.Making(invite, cancelInvitation))
		invitation := sendInvitation(t, sender, freshEmail(), role)
		ctx := sender.Context(t.Context())

		response, err := sender.Surfaces.Identity.CancelInvitation(ctx,
			&identitypb.CancelInvitationRequest{InvitationId: invitation.GetId(), StatusNote: "hired somebody else"})
		must.NoError(t, err)
		test.EqOp(t, identitypb.InvitationStatus_INVITATION_STATUS_CANCELLED, response.GetInvitation().GetStatus())

		_, err = sender.Surfaces.Identity.CancelInvitation(ctx,
			&identitypb.CancelInvitationRequest{InvitationId: invitation.GetId()})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("an invitation read is confined to the sender's directory", func(t *testing.T) {
		t.Parallel()

		sender, stranger := twoDirectories(t, s, conformance.Making(invite, getInvitation))
		address := freshEmail()
		invitation := sendInvitation(t, sender, address, role)

		read, err := sender.Surfaces.Identity.GetInvitation(sender.Context(t.Context()),
			&identitypb.GetInvitationRequest{InvitationId: invitation.GetId()})
		must.NoError(t, err)
		test.EqOp(t, address, read.GetInvitation().GetToEmail())

		_, err = stranger.Surfaces.Identity.GetInvitation(stranger.Context(t.Context()),
			&identitypb.GetInvitationRequest{InvitationId: invitation.GetId()})
		notYours(t, err, "a neighboring directory's invitation")
	})

	t.Run("an invitation read is refused to a colleague outside the account it is into", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t, conformance.Making(invite, getInvitation))
		invitation := sendInvitation(t, sender, freshEmail(), role)

		// The positive control: the sender is a member of the account the
		// invitation is into, and reads it through the same call.
		read, err := sender.Surfaces.Identity.GetInvitation(sender.Context(t.Context()),
			&identitypb.GetInvitationRequest{InvitationId: invitation.GetId()})
		must.NoError(t, err, must.Sprint("the account's own member cannot read its invitation; the refusal below proves nothing"))
		test.EqOp(t, invitation.GetId(), read.GetInvitation().GetId())

		// Same directory, same permission, another account, and not the
		// address the invitation names. The permission is the grant, and it
		// does not reach somebody else's account.
		stranger := colleague(t, s, sender, conformance.Making(getInvitation), conformance.AsMember())

		_, err = stranger.Surfaces.Identity.GetInvitation(stranger.Context(t.Context()),
			&identitypb.GetInvitationRequest{InvitationId: invitation.GetId()})
		must.Error(t, err, must.Sprint("a colleague outside the account read its invitation"))
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("a sender's listing is what they sent", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t, conformance.Making(invite, listInvitationsFromUser))
		other := colleague(t, s, sender, conformance.Making(invite))

		first, second, theirs := freshEmail(), freshEmail(), freshEmail()
		sendInvitation(t, sender, first, role)
		sendInvitation(t, sender, second, role)
		sendInvitation(t, other, theirs, role)

		page, err := sender.Surfaces.Identity.ListInvitationsFromUser(sender.Context(t.Context()),
			&identitypb.ListInvitationsFromUserRequest{})
		must.NoError(t, err)
		test.NotNil(t, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))

		addresses := invitationAddresses(page.GetResults())
		test.SliceContains(t, addresses, first)
		test.SliceContains(t, addresses, second)
		test.SliceNotContains(t, addresses, theirs)
	})

	t.Run("a verified caller reads the invitations addressed to them, and only those", func(t *testing.T) {
		t.Parallel()

		verified := s.Seams().Actions.EmailVerified
		s.NeedsAction(t, verified != nil, "email verified")

		sender := s.Subject(t, conformance.Making(invite))
		invitee := colleague(t, s, sender, conformance.Making(getPrincipal, listInvitationsForEmailAddress))
		bystander := colleague(t, s, sender, conformance.Making(listInvitationsForEmailAddress))

		must.NoError(t, verified(t.Context(), invitee.ScopeFor(surface), invitee.UserID))
		must.NoError(t, verified(t.Context(), bystander.ScopeFor(surface), bystander.UserID))

		address := self(t, invitee).GetEmailAddress()
		sendInvitation(t, sender, address, role)

		received, err := invitee.Surfaces.Identity.ListInvitationsForEmailAddress(invitee.Context(t.Context()),
			&identitypb.ListInvitationsForEmailAddressRequest{})
		must.NoError(t, err)
		test.SliceContains(t, invitationAddresses(received.GetResults()), address)
		test.NotNil(t, received.GetPagination(), test.Sprint("a paged read answered with no pagination"))

		empty, err := bystander.Surfaces.Identity.ListInvitationsForEmailAddress(bystander.Context(t.Context()),
			&identitypb.ListInvitationsForEmailAddressRequest{})
		must.NoError(t, err)
		test.SliceNotContains(t, invitationAddresses(empty.GetResults()), address)
	})

	t.Run("a caller who has not verified their address is refused its invitations", func(t *testing.T) {
		t.Parallel()

		verified := s.Seams().Actions.EmailVerified
		s.NeedsAction(t, verified != nil, "email verified")

		sender := s.Subject(t, conformance.Making(invite))
		victim := colleague(t, s, sender, conformance.Making(getPrincipal, listInvitationsForEmailAddress))
		must.NoError(t, verified(t.Context(), victim.ScopeFor(surface), victim.UserID))

		sendInvitation(t, sender, self(t, victim).GetEmailAddress(), role)

		// An attacker who sets their own address to anything is unverified, and
		// an unverified address is exactly the one a read keyed on it must not
		// answer for: otherwise claiming somebody's address reads their mail.
		attacker := colleague(t, s, sender, conformance.Making(updateProfile, listInvitationsForEmailAddress))
		attackerCtx := attacker.Context(t.Context())

		_, err := attacker.Surfaces.Identity.UpdateProfile(attackerCtx, &identitypb.UpdateProfileRequest{
			Input: &identitypb.ProfileUpdateInput{EmailAddress: new(freshEmail())},
		})
		must.NoError(t, err)

		_, err = attacker.Surfaces.Identity.ListInvitationsForEmailAddress(attackerCtx,
			&identitypb.ListInvitationsForEmailAddressRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))

		// The positive control: the verified victim reads theirs.
		received, err := victim.Surfaces.Identity.ListInvitationsForEmailAddress(victim.Context(t.Context()),
			&identitypb.ListInvitationsForEmailAddressRequest{})
		must.NoError(t, err)
		test.SliceNotEmpty(t, received.GetResults())
	})
}

func invitationAddresses(invitations []*identitypb.Invitation) []string {
	out := make([]string, 0, len(invitations))
	for _, i := range invitations {
		out = append(out, i.GetToEmail())
	}

	return out
}

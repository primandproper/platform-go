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

	t.Run("an invitation is kept, and its token is not returned to the sender", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t)
		invitation := invite(t, sender, freshEmail(), roleSupport)
		token := tokenFor(t, s, sender, invitation.GetId())

		test.StrNotContains(t, invitation.String(), token,
			test.Sprint("the invitation's token came back to the sender"))

		read, err := sender.Surfaces.Identity.GetInvitation(sender.Context(t.Context()),
			&identitypb.GetInvitationRequest{InvitationId: invitation.GetId()})
		must.NoError(t, err)
		test.EqOp(t, sender.UserID, read.GetInvitation().GetFromUser())
		test.EqOp(t, identitypb.InvitationStatus_INVITATION_STATUS_PENDING, read.GetInvitation().GetStatus())
		test.StrNotContains(t, read.GetInvitation().String(), token)
	})

	t.Run("an invitation expires when the request says", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t)
		needsAccount(t, sender)

		// Whole seconds, because a timestamp is compared exactly here and the
		// weakest dialect keeps no more than that.
		asked := time.Now().UTC().Add(3 * time.Hour).Truncate(time.Second)

		response, err := sender.Surfaces.Identity.Invite(sender.Context(t.Context()), &identitypb.InviteRequest{
			AccountId: sender.AccountID,
			ToEmail:   freshEmail(),
			Roles:     []string{roleSupport},
			ExpiresAt: timestamppb.New(asked),
		})
		must.NoError(t, err)
		test.EqOp(t, asked, response.GetInvitation().GetExpiresAt().AsTime())
	})

	t.Run("an expiry in the past is refused", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t)
		needsAccount(t, sender)

		_, err := sender.Surfaces.Identity.Invite(sender.Context(t.Context()), &identitypb.InviteRequest{
			AccountId: sender.AccountID,
			ToEmail:   freshEmail(),
			Roles:     []string{roleSupport},
			ExpiresAt: timestamppb.New(time.Now().UTC().Add(-time.Minute)),
		})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("accepting an invitation mints the membership it promised, once", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t)
		needsAccount(t, sender)
		invitee := colleague(t, s, sender)

		invitation := invite(t, sender, self(t, invitee).GetEmailAddress(), roleSupport, "billing")
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
		test.Eq(t, []string{"billing", roleSupport}, slices.Sorted(slices.Values(acceptance.GetMembership().GetRoles())))
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

		sender := s.Subject(t)
		invitee := colleague(t, s, sender)
		invitation := invite(t, sender, self(t, invitee).GetEmailAddress(), roleSupport)

		_, err := invitee.Surfaces.Identity.AcceptInvitation(invitee.Context(t.Context()),
			&identitypb.AcceptInvitationRequest{InvitationId: invitation.GetId(), Token: "not the token"})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("a rejection checks the token before it writes", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t)
		invitee := colleague(t, s, sender)
		invitation := invite(t, sender, self(t, invitee).GetEmailAddress(), roleSupport)
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

		sender := s.Subject(t)
		invitation := invite(t, sender, freshEmail(), roleSupport)
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

		sender, stranger := twoDirectories(t, s)
		address := freshEmail()
		invitation := invite(t, sender, address, roleSupport)

		read, err := sender.Surfaces.Identity.GetInvitation(sender.Context(t.Context()),
			&identitypb.GetInvitationRequest{InvitationId: invitation.GetId()})
		must.NoError(t, err)
		test.EqOp(t, address, read.GetInvitation().GetToEmail())

		_, err = stranger.Surfaces.Identity.GetInvitation(stranger.Context(t.Context()),
			&identitypb.GetInvitationRequest{InvitationId: invitation.GetId()})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("a sender's listing is what they sent", func(t *testing.T) {
		t.Parallel()

		sender := s.Subject(t)
		other := colleague(t, s, sender)

		first, second, theirs := freshEmail(), freshEmail(), freshEmail()
		invite(t, sender, first, roleSupport)
		invite(t, sender, second, roleSupport)
		invite(t, other, theirs, roleSupport)

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

		sender := s.Subject(t)
		invitee := colleague(t, s, sender)
		bystander := colleague(t, s, sender)

		must.NoError(t, verified(t.Context(), invitee.Scope, invitee.UserID))
		must.NoError(t, verified(t.Context(), bystander.Scope, bystander.UserID))

		address := self(t, invitee).GetEmailAddress()
		invite(t, sender, address, roleSupport)

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

		sender := s.Subject(t)
		victim := colleague(t, s, sender)
		must.NoError(t, verified(t.Context(), victim.Scope, victim.UserID))

		invite(t, sender, self(t, victim).GetEmailAddress(), roleSupport)

		// An attacker who sets their own address to anything is unverified, and
		// an unverified address is exactly the one a read keyed on it must not
		// answer for: otherwise claiming somebody's address reads their mail.
		attacker := colleague(t, s, sender)
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

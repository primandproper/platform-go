package grpc_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fixedMinter is a consumer's TokenMinter: whatever mints their links. Here it
// mints a known one, which is the only way a test can follow an invitation the
// way a recipient does — the token is never returned to the sender, so nothing
// on the wire could hand it back.
func fixedMinter(token string) identitygrpc.TokenMinter {
	return func(context.Context) (string, error) { return token, nil }
}

const testInvitationToken = "test-invitation-token"

// invite is the harness's sender: an account, its owner, and one invitation out
// to an address, with the token the suite knows.
func invite(
	t *testing.T,
	h *harness,
	sender *identity.Registration,
	toEmail string,
	roles ...string,
) *identitypb.Invitation {
	t.Helper()

	ctx := h.as(&testPrincipal{userID: sender.User.ID, scope: testScope})

	response, err := h.client.Invite(ctx, &identitypb.InviteRequest{
		AccountId: sender.Account.ID,
		ToEmail:   toEmail,
		ToName:    "Some Body",
		Note:      "come and join us",
		Roles:     roles,
	})
	must.NoError(t, err)
	must.NotNil(t, response.GetInvitation())

	return response.GetInvitation()
}

// TestInviteMintsTheTokenAndKeepsIt is the one piece of machinery this transport
// owns that the service does not, and the reason it owns it: a client that could
// choose the token could choose a guessable one, or somebody else's.
func TestInviteMintsTheTokenAndKeepsIt(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)))

	sender := h.seedAccount(T, testScope, "sender")
	invitation := invite(T, h, sender, "invitee@example.com", "support")

	// It is not returned to the sender: the response carries the redacted
	// invitation, and the token reaches only the address it was minted for.
	test.StrNotContains(T, invitation.String(), testInvitationToken,
		test.Sprint("the invitation's token came back to the sender"))

	// And it was stored, which is what makes the recipient's link work. Reading
	// it by token is the store's own check, so this asserts the value that
	// traveled rather than that some value did.
	stored, err := h.store.GetInvitationByToken(
		T.Context(), h.db.Reader(), testScope, invitation.GetId(), testInvitationToken)
	must.NoError(T, err)
	test.EqOp(T, invitation.GetId(), stored.ID)
	test.EqOp(T, sender.User.ID, stored.FromUser)
	test.EqOp(T, identity.InvitationPending, stored.Status)
}

// TestInviteDefaultsTheExpiry pins the one thing a transport cannot decline to
// pick: an invitation with no expiry is a link that works forever.
func TestInviteDefaultsTheExpiry(T *testing.T) {
	T.Parallel()

	const ttl = 90 * time.Minute

	h := newHarness(T,
		identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)),
		identitygrpc.WithInvitationTTL(ttl),
	)

	sender := h.seedAccount(T, testScope, "sender")

	before := time.Now().UTC()
	invitation := invite(T, h, sender, "invitee@example.com", "support")
	after := time.Now().UTC()

	expiry := invitation.GetExpiresAt().AsTime()
	test.True(T, expiry.After(before.Add(ttl).Add(-time.Minute)), test.Sprintf(
		"an invitation with no expiry on the request expired at %s, before the configured TTL", expiry))
	test.True(T, expiry.Before(after.Add(ttl).Add(time.Minute)), test.Sprintf(
		"an invitation with no expiry on the request expired at %s, after the configured TTL", expiry))
}

// TestInviteHonorsTheExpiryTheRequestNames is the other half: the TTL is a
// default rather than a policy this package holds.
func TestInviteHonorsTheExpiryTheRequestNames(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)))

	sender := h.seedAccount(T, testScope, "sender")
	ctx := h.as(&testPrincipal{userID: sender.User.ID, scope: testScope})

	// Truncated to the second because SQLite stores times to the second, so a
	// finer instant would come back as a different one for reasons that have
	// nothing to do with this RPC.
	asked := time.Now().UTC().Add(3 * time.Hour).Truncate(time.Second)

	response, err := h.client.Invite(ctx, &identitypb.InviteRequest{
		AccountId: sender.Account.ID,
		ToEmail:   "invitee@example.com",
		Roles:     []string{"support"},
		ExpiresAt: timestamppb.New(asked),
	})
	must.NoError(T, err)

	test.EqOp(T, asked, response.GetInvitation().GetExpiresAt().AsTime())
}

// TestInviteSurfacesAMintingFailure: a consumer whose minter is a service of
// their own has a minter that can fail, and an invitation with no token would be
// a link nobody can follow.
func TestInviteSurfacesAMintingFailure(T *testing.T) {
	T.Parallel()

	minterErr := errors.New("the minter is down")

	h := newHarness(T, identitygrpc.WithTokenMinter(
		func(context.Context) (string, error) { return "", minterErr }))

	sender := h.seedAccount(T, testScope, "sender")
	ctx := h.as(&testPrincipal{userID: sender.User.ID, scope: testScope})

	_, err := h.client.Invite(ctx, &identitypb.InviteRequest{
		AccountId: sender.Account.ID,
		ToEmail:   "invitee@example.com",
		Roles:     []string{"support"},
	})
	must.Error(T, err)
	test.EqOp(T, codes.Internal, status.Code(err))

	// Nothing was written: the token is minted before the invitation is, so a
	// minter that failed leaves no row for a recipient who can never be sent a
	// link.
	sent, err := h.client.ListInvitationsFromUser(ctx, &identitypb.ListInvitationsFromUserRequest{})
	must.NoError(T, err)
	test.SliceEmpty(T, sent.GetResults())
}

// TestAcceptInvitationMintsTheMembershipItPromised is the pair the store writes
// in one transaction: an accepted invitation without a membership is somebody
// who was told they joined and did not.
func TestAcceptInvitationMintsTheMembershipItPromised(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)))

	sender := h.seedAccount(T, testScope, "sender")
	invitation := invite(T, h, sender, "invitee@example.com", "support", "billing")

	invitee := h.seedUser(T, testScope, "invitee")
	inviteeCtx := h.as(&testPrincipal{userID: invitee.ID, scope: testScope})

	response, err := h.client.AcceptInvitation(inviteeCtx, &identitypb.AcceptInvitationRequest{
		InvitationId: invitation.GetId(),
		Token:        testInvitationToken,
		StatusNote:   "glad to be here",
	})
	must.NoError(T, err)

	acceptance := response.GetAcceptance()
	must.NotNil(T, acceptance.GetInvitation())
	must.NotNil(T, acceptance.GetMembership())

	test.EqOp(T, identitypb.InvitationStatus_INVITATION_STATUS_ACCEPTED,
		acceptance.GetInvitation().GetStatus())
	test.EqOp(T, invitee.ID, acceptance.GetInvitation().GetToUser())
	test.EqOp(T, invitee.ID, acceptance.GetMembership().GetBelongsToUser())
	test.EqOp(T, sender.Account.ID, acceptance.GetMembership().GetBelongsToAccount())

	// The roles come from the invitation and never from the request, which is
	// where an escalation would otherwise go in. Sorted, because what is being
	// asserted is the set the invitation promised and not the order the store
	// happens to read it back in.
	test.Eq(T, []string{"billing", "support"}, slices.Sorted(slices.Values(
		acceptance.GetMembership().GetRoles())))

	// And the answered invitation still carries no token.
	test.StrNotContains(T, acceptance.GetInvitation().String(), testInvitationToken)

	// Two clicks on one link produce one membership: the second finds nothing
	// pending.
	_, err = h.client.AcceptInvitation(inviteeCtx, &identitypb.AcceptInvitationRequest{
		InvitationId: invitation.GetId(),
		Token:        testInvitationToken,
	})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))
}

func TestAcceptInvitationRefusesTheWrongToken(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)))

	sender := h.seedAccount(T, testScope, "sender")
	invitation := invite(T, h, sender, "invitee@example.com", "support")

	invitee := h.seedUser(T, testScope, "invitee")
	inviteeCtx := h.as(&testPrincipal{userID: invitee.ID, scope: testScope})

	_, err := h.client.AcceptInvitation(inviteeCtx, &identitypb.AcceptInvitationRequest{
		InvitationId: invitation.GetId(),
		Token:        "not the token",
	})
	must.Error(T, err)

	// Absent rather than forbidden: naming an invitation without holding its
	// token is indistinguishable from naming one that does not exist, and the
	// other answer would confirm the id.
	test.EqOp(T, codes.NotFound, status.Code(err))
	test.True(T, errors.Is(err, identity.ErrInvitationNotFound))
}

// TestRejectInvitationChecksTheTokenBeforeWriting is the difference between this
// and a status write addressed by id alone: a rejection arrives from whoever
// followed the link, and the link is the token.
func TestRejectInvitationChecksTheTokenBeforeWriting(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)))

	sender := h.seedAccount(T, testScope, "sender")
	invitation := invite(T, h, sender, "invitee@example.com", "support")

	invitee := h.seedUser(T, testScope, "invitee")
	inviteeCtx := h.as(&testPrincipal{userID: invitee.ID, scope: testScope})

	_, err := h.client.RejectInvitation(inviteeCtx, &identitypb.RejectInvitationRequest{
		InvitationId: invitation.GetId(),
		Token:        "not the token",
	})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))

	// And the invitation is untouched, which is the half a status write
	// addressed by id alone would have got wrong.
	read, err := h.client.GetInvitation(h.ctx(),
		&identitypb.GetInvitationRequest{InvitationId: invitation.GetId()})
	must.NoError(T, err)
	test.EqOp(T, identitypb.InvitationStatus_INVITATION_STATUS_PENDING, read.GetInvitation().GetStatus())

	response, err := h.client.RejectInvitation(inviteeCtx, &identitypb.RejectInvitationRequest{
		InvitationId: invitation.GetId(),
		Token:        testInvitationToken,
		StatusNote:   "no thank you",
	})
	must.NoError(T, err)

	test.EqOp(T, identitypb.InvitationStatus_INVITATION_STATUS_REJECTED,
		response.GetInvitation().GetStatus())
	test.EqOp(T, "no thank you", response.GetInvitation().GetStatusNote())

	// The sender's note survives the answer, because an answer that overwrote it
	// would destroy the message at the moment a roster wants to show both.
	test.EqOp(T, "come and join us", response.GetInvitation().GetNote())
}

// TestCancelInvitationTakesNoToken: the sender never had one — they are looking
// at what they sent, addressed by id.
func TestCancelInvitationTakesNoToken(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)))

	sender := h.seedAccount(T, testScope, "sender")
	invitation := invite(T, h, sender, "invitee@example.com", "support")

	ctx := h.as(&testPrincipal{userID: sender.User.ID, scope: testScope})

	response, err := h.client.CancelInvitation(ctx, &identitypb.CancelInvitationRequest{
		InvitationId: invitation.GetId(),
		StatusNote:   "hired somebody else",
	})
	must.NoError(T, err)
	test.EqOp(T, identitypb.InvitationStatus_INVITATION_STATUS_CANCELLED,
		response.GetInvitation().GetStatus())

	// An invitation that has already been answered reads as absent, which is
	// what makes a cancellation that raced an acceptance leave the acceptance
	// standing.
	_, err = h.client.CancelInvitation(ctx, &identitypb.CancelInvitationRequest{
		InvitationId: invitation.GetId(),
	})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))
	test.True(T, errors.Is(err, identity.ErrInvitationNotFound))
}

func TestGetInvitationIsScopedAndRedacted(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)))

	sender := h.seedAccount(T, testScope, "sender")
	invitation := invite(T, h, sender, "invitee@example.com", "support")

	read, err := h.client.GetInvitation(h.ctx(),
		&identitypb.GetInvitationRequest{InvitationId: invitation.GetId()})
	must.NoError(T, err)
	test.EqOp(T, "invitee@example.com", read.GetInvitation().GetToEmail())
	test.StrNotContains(T, read.GetInvitation().String(), testInvitationToken)

	// A neighbor cannot read it, and is told it is absent.
	_, err = h.client.GetInvitation(h.as(&testPrincipal{userID: "caller", scope: otherScope}),
		&identitypb.GetInvitationRequest{InvitationId: invitation.GetId()})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))
}

func TestListInvitationsFromUserPagesWhatTheCallerSent(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)))

	sender := h.seedAccount(T, testScope, "sender")
	other := h.seedAccount(T, testScope, "othersender")

	invite(T, h, sender, "first@example.com", "support")
	invite(T, h, sender, "second@example.com", "support")
	invite(T, h, other, "third@example.com", "support")

	ctx := h.as(&testPrincipal{userID: sender.User.ID, scope: testScope})

	page, err := h.client.ListInvitationsFromUser(ctx, &identitypb.ListInvitationsFromUserRequest{})
	must.NoError(T, err)
	test.NotNil(T, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))

	addresses := invitationAddresses(page.GetResults())
	test.SliceContains(T, addresses, "first@example.com")
	test.SliceContains(T, addresses, "second@example.com")

	// The sender is the caller and there is no field to ask with, so somebody
	// else's outbox is not reachable from here.
	test.SliceNotContains(T, addresses, "third@example.com")

	for _, sent := range page.GetResults() {
		test.StrNotContains(T, sent.String(), testInvitationToken)
	}
}

// TestListInvitationsForEmailAddressReadsTheCallersOwnRow is the property the
// method's doc names: an address a client could send would answer "has this
// person been invited anywhere" to anybody who can guess an email address.
func TestListInvitationsForEmailAddressReadsTheCallersOwnRow(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)))

	sender := h.seedAccount(T, testScope, "sender")

	// seedUser addresses each user as <username>@example.com, which is the
	// address these two invitations are sent to.
	invitee := h.seedUser(T, testScope, "invitee")
	bystander := h.seedUser(T, testScope, "bystander")

	h.verifyEmail(T, testScope, invitee.ID)
	h.verifyEmail(T, testScope, bystander.ID)

	invite(T, h, sender, invitee.EmailAddress, "support")

	received, err := h.client.ListInvitationsForEmailAddress(
		h.as(&testPrincipal{userID: invitee.ID, scope: testScope}),
		&identitypb.ListInvitationsForEmailAddressRequest{})
	must.NoError(T, err)
	test.SliceContains(T, invitationAddresses(received.GetResults()), invitee.EmailAddress)
	test.NotNil(T, received.GetPagination(), test.Sprint("a paged read answered with no pagination"))

	// The request carries no address, so the bystander's inbox is their own and
	// there is no way to ask for somebody else's.
	empty, err := h.client.ListInvitationsForEmailAddress(
		h.as(&testPrincipal{userID: bystander.ID, scope: testScope}),
		&identitypb.ListInvitationsForEmailAddressRequest{})
	must.NoError(T, err)
	test.SliceEmpty(T, empty.GetResults())
}

// TestListInvitationsForEmailAddressNeedsTheCallersRow is the failure mode the
// read off the caller's own user row introduces: a principal the consumer's
// authentication resolved to a user this directory does not have cannot be told
// what was sent to them.
func TestListInvitationsForEmailAddressNeedsTheCallersRow(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	_, err := h.client.ListInvitationsForEmailAddress(h.ctx(),
		&identitypb.ListInvitationsForEmailAddressRequest{})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))
	test.True(T, errors.Is(err, identity.ErrUserNotFound))
}

func invitationAddresses(invitations []*identitypb.Invitation) []string {
	out := make([]string, 0, len(invitations))
	for _, i := range invitations {
		out = append(out, i.GetToEmail())
	}

	return out
}

// TestListInvitationsForEmailAddressRefusesAnUnverifiedCaller is the other half
// of the oracle the method's doc closes. UpdateProfile is self-service and takes
// an email address, so a caller who could list invitations by an unverified
// address could list anybody's by typing it in first. The refusal is a status
// of its own rather than an empty page, because an empty page says "nobody has
// invited you" and that is not what the server knows.
func TestListInvitationsForEmailAddressRefusesAnUnverifiedCaller(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)))

	sender := h.seedAccount(T, testScope, "sender")
	victim := h.seedUser(T, testScope, "victim")
	h.verifyEmail(T, testScope, victim.ID)

	invite(T, h, sender, victim.EmailAddress, "support")

	// The attacker registers, then claims the victim's address through the
	// self-service update. The service accepts the claim and clears its
	// verification, which is exactly what the read below has to notice.
	attacker := h.seedAccount(T, testScope, "attacker")
	attackerCtx := h.as(&testPrincipal{userID: attacker.User.ID, scope: testScope})

	_, err := h.client.UpdateProfile(attackerCtx, &identitypb.UpdateProfileRequest{
		Input: &identitypb.ProfileUpdateInput{EmailAddress: new("victim2@example.com")},
	})
	must.NoError(T, err)

	_, err = h.client.ListInvitationsForEmailAddress(attackerCtx,
		&identitypb.ListInvitationsForEmailAddressRequest{})
	must.Error(T, err)
	test.EqOp(T, codes.FailedPrecondition, status.Code(err))
	test.True(T, errors.Is(err, identitygrpc.ErrEmailAddressUnverified))

	// The victim, verified, still reads their own inbox.
	received, err := h.client.ListInvitationsForEmailAddress(
		h.as(&testPrincipal{userID: victim.ID, scope: testScope}),
		&identitypb.ListInvitationsForEmailAddressRequest{})
	must.NoError(T, err)
	test.SliceContains(T, invitationAddresses(received.GetResults()), victim.EmailAddress)
}

// TestInviteRefusesAnExpiryInThePast: a request may name its own expiry, and
// one no later than now is a link the consumer's hook would mail out already
// dead.
func TestInviteRefusesAnExpiryInThePast(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)))

	sender := h.seedAccount(T, testScope, "sender")
	ctx := h.as(&testPrincipal{userID: sender.User.ID, scope: testScope})

	_, err := h.client.Invite(ctx, &identitypb.InviteRequest{
		AccountId: sender.Account.ID,
		ToEmail:   "invitee@example.com",
		Roles:     []string{"support"},
		ExpiresAt: timestamppb.New(time.Now().UTC().Add(-time.Minute)),
	})
	must.Error(T, err)
	test.EqOp(T, codes.InvalidArgument, status.Code(err))
	test.True(T, errors.Is(err, identitygrpc.ErrInvitationExpiryInPast))
}

// TestInviteRefusesAnExpiryBeyondTheMaximum is what makes the default a bound
// rather than a suggestion: a request naming the year 9999 would otherwise be
// the forever-link the default exists to prevent.
func TestInviteRefusesAnExpiryBeyondTheMaximum(T *testing.T) {
	T.Parallel()

	const maxTTL = 2 * time.Hour

	h := newHarness(T,
		identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)),
		identitygrpc.WithInvitationTTL(time.Hour),
		identitygrpc.WithMaxInvitationTTL(maxTTL),
	)

	sender := h.seedAccount(T, testScope, "sender")
	ctx := h.as(&testPrincipal{userID: sender.User.ID, scope: testScope})

	_, err := h.client.Invite(ctx, &identitypb.InviteRequest{
		AccountId: sender.Account.ID,
		ToEmail:   "invitee@example.com",
		Roles:     []string{"support"},
		ExpiresAt: timestamppb.New(time.Now().UTC().Add(maxTTL + time.Hour)),
	})
	must.Error(T, err)
	test.EqOp(T, codes.InvalidArgument, status.Code(err))
	test.True(T, errors.Is(err, identitygrpc.ErrInvitationExpiryTooFar))

	// Inside the ceiling is still the client's call.
	asked := time.Now().UTC().Add(maxTTL - time.Minute).Truncate(time.Second)

	response, err := h.client.Invite(ctx, &identitypb.InviteRequest{
		AccountId: sender.Account.ID,
		ToEmail:   "invitee@example.com",
		Roles:     []string{"support"},
		ExpiresAt: timestamppb.New(asked),
	})
	must.NoError(T, err)
	test.EqOp(T, asked, response.GetInvitation().GetExpiresAt().AsTime())
}

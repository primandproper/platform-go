package signin

import (
	"slices"
	"testing"

	domain "github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func registration(t *testing.T, s *conformance.Session) {
	t.Helper()

	// A deployment that closed its sign-up door is held to the refusal that
	// tells a closed door from a broken one: Unimplemented alone is also what
	// a server answers for a method it never mounted, so the reason is the
	// promise. Every assertion below registers somebody, and skips here.
	t.Run("a closed sign-up door is refused by name", func(t *testing.T) {
		t.Parallel()

		if !s.Seams().RegistrationClosed {
			conformance.Skip(t, "conformance: this subject's sign-up door is open (Seams.RegistrationClosed is false), so there is no closed door to refuse; skipping")
		}

		anon := anonymous(t, s, registerUser)

		_, err := anon.Register(t.Context(), withPassword(registrationRequest()))
		refused(t, s, err, codes.Unimplemented, reasonRegistrationClosed)
	})

	// The property this surface exists for: somebody registered over the wire,
	// with nobody signed in, can sign in over the wire. They cannot before
	// their address is proven, and the refusal says why in a form a client can
	// send them to verification on; the link they were mailed, answered with
	// nobody signed in, is what lets them in.
	//
	// A deployment whose policy admits a registrant unverified says so in
	// Seams.RegistrantsAdmittedUnverified, and is held to that instead: the
	// door admits them at once, and the link still proves the address, which
	// they are told when they ask.
	t.Run("a registrant with nobody on the request signs in once the mailed link proves their address", func(t *testing.T) {
		t.Parallel()

		if s.Seams().RegistrationClosed {
			conformance.Skip(t, "conformance: this subject closes its sign-up door (Seams.RegistrationClosed), so nobody registers with nobody on the request; skipping")
		}

		anon := anonymous(t, s, registerUser, verifyEmailAddress, loginForToken)
		request := withPassword(registrationRequest())

		response, err := anon.Register(t.Context(), request)
		must.NoError(t, err, must.Sprint("registering somebody with nobody on the request"))

		registered := response.GetRegistration()
		who := &registrant{
			userID:   registered.GetUser().GetId(),
			username: request.GetUser().GetUsername(),
			email:    request.GetUser().GetEmailAddress(),
		}

		test.EqOp(t, who.username, registered.GetUser().GetUsername())
		test.NotNil(t, registered.GetAccount(), test.Sprint("a registration naming an account answered with none"))
		test.NotNil(t, registered.GetMembership(), test.Sprint("a registration answered with no membership"))

		admitted := unproven(t, s, anon, who)
		if admitted != nil {
			if proven, ok := addressProven(t, s, admitted); ok {
				test.False(t, proven, test.Sprint("a registrant nobody verified was told their address is proven"))
			}
		}

		verify(t, s, anon, who)

		issued := loggedIn(t, anon, who.username, password)
		test.NotEqOp(t, "", issued.GetToken())

		if admitted != nil {
			if proven, ok := addressProven(t, s, issued); ok {
				test.True(t, proven, test.Sprint("the mailed link admitted nobody new, and proved nothing either"))
			}
		}
	})

	// A registration on the wire names no roles — the request has no field for
	// them — so the roles a registrant owns their account with are the
	// deployment's: its default owner roles, or what its registration policy
	// replaced them with. Seams.Roles.Owner is the deployment saying which.
	t.Run("a registrant owns their account with the deployment's owner role", func(t *testing.T) {
		t.Parallel()

		_, registered := register(t, s, withPassword(registrationRequest()))

		test.Eq(t, []string{s.Roles().Owner}, registered.GetMembership().GetRoles(),
			test.Sprint("a registrant holds other roles in their own account than the deployment's owner role"))
	})

	passwordlessRegistration(t, s)

	// Expired, already answered, never issued and simply wrong share a remedy,
	// and telling them apart tells whoever is guessing which guesses are getting
	// warm. So a dead link is the answer a wrong password gets, word for word.
	t.Run("a verification link nobody was mailed is refused as a wrong password is", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress)

		_, err := anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: identifiers.New()})
		refused(t, s, err, codes.Unauthenticated, reasonInvalidCredentials)
		test.EqOp(t, domain.ErrInvalidCredentials.Error(), status.Convert(err).Message())

		// A spent link lands in the same place.
		who, _ := register(t, s, withPassword(registrationRequest()))
		link := mailedVerification(t, s, who.email)

		_, spendErr := anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: link})
		must.NoError(t, spendErr, must.Sprint("the control: the link the deployment mailed did not verify"))

		_, spent := anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: link})
		indistinguishable(t, s, err, spent, "a spent verification link against one never mailed")
	})

	// The "resend" button: somebody whose link never arrived asks for another
	// while signed in, and the one they held stops working. A registrant cannot
	// sign in until their address is proven, so the person here is one an
	// operator admitted without it — signed in, with an address still unproven,
	// which is also where an address change leaves somebody.
	t.Run("a resend retires the mailed link, and the link it mails verifies", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		operator := directoryCaller(t, s, updateUserAccountStatus)

		if s.Seams().SignedIn == nil {
			conformance.Skip(t, "conformance: this subject supplies no SignedIn seam, so nobody the suite signs in can be called as; skipping")
		}

		who, _ := register(t, s, withPassword(registrationRequest()))
		first := mailedVerification(t, s, who.email)

		_, err := operator.Surfaces.Identity.UpdateUserAccountStatus(operator.Context(t.Context()),
			&identitypb.UpdateUserAccountStatusRequest{
				UserId: who.userID,
				Status: identitypb.AccountStatus_ACCOUNT_STATUS_GOOD,
			})
		must.NoError(t, err, must.Sprint("admitting a registrant whose address is unproven"))

		sub := caller(t, s, loggedIn(t, anon, who.username, password), requestVerificationEmail)

		_, err = sub.Surfaces.SignIn.RequestVerificationEmail(sub.Context(t.Context()),
			&signinpb.RequestVerificationEmailRequest{})
		must.NoError(t, err, must.Sprint("asking for another verification link"))

		fresh := mailedVerification(t, s, who.email)
		must.NotEqOp(t, first, fresh,
			must.Sprint("the verification token action answered with the link from before the resend; it must report the newest"))

		_, err = anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: first})
		refused(t, s, err, codes.Unauthenticated, reasonInvalidCredentials)

		_, err = anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: fresh})
		must.NoError(t, err, must.Sprint("the link the resend mailed did not verify"))
	})

	// The registrant's resend: they cannot sign in until they answer a link, so
	// they ask by address, with nobody on the request. The link it mails is the
	// one that works afterwards, and an address nobody holds is answered the
	// same way.
	t.Run("a resend by address retires the mailed link, and the link it mails verifies", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, requestVerificationEmailByAddress)

		who, _ := register(t, s, withPassword(registrationRequest()))
		first := mailedVerification(t, s, who.email)

		_, err := anon.RequestVerificationEmailByAddress(t.Context(),
			&signinpb.RequestVerificationEmailByAddressRequest{EmailAddress: who.email})
		must.NoError(t, err, must.Sprint("asking for another verification link by address"))

		fresh := mailedVerification(t, s, who.email)
		must.NotEqOp(t, first, fresh,
			must.Sprint("the verification token action answered with the link from before the resend; it must report the newest"))

		_, err = anon.RequestVerificationEmailByAddress(t.Context(),
			&signinpb.RequestVerificationEmailByAddressRequest{EmailAddress: freshEmail()})
		must.NoError(t, err, must.Sprint("a resend for an address nobody holds was answered differently"))

		_, err = anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: first})
		refused(t, s, err, codes.Unauthenticated, reasonInvalidCredentials)

		_, err = anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: fresh})
		must.NoError(t, err, must.Sprint("the link the resend by address mailed did not verify"))
	})

	// A resend can never un-prove anybody: asking for a link for an address that
	// is already proven is refused, and the proof is exactly what it was.
	t.Run("a resend for a proven address is refused and leaves the proof", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		sub, _ := signedIn(t, s, anon, requestVerificationEmail, getSelf)

		before, err := sub.Surfaces.SignIn.GetSelf(sub.Context(t.Context()), &signinpb.GetSelfRequest{})
		must.NoError(t, err)
		must.NotNil(t, before.GetUser().GetEmailAddressVerifiedAt(),
			must.Sprint("the control: a verified registrant's address reads as unproven"))

		_, err = sub.Surfaces.SignIn.RequestVerificationEmail(sub.Context(t.Context()),
			&signinpb.RequestVerificationEmailRequest{})
		refused(t, s, err, codes.FailedPrecondition, reasonEmailAlreadyVerified)

		after, err := sub.Surfaces.SignIn.GetSelf(sub.Context(t.Context()), &signinpb.GetSelfRequest{})
		must.NoError(t, err)
		must.NotNil(t, after.GetUser().GetEmailAddressVerifiedAt(),
			must.Sprint("a refused resend withdrew the proof"))
		test.True(t, before.GetUser().GetEmailAddressVerifiedAt().AsTime().Equal(
			after.GetUser().GetEmailAddressVerifiedAt().AsTime()),
			test.Sprint("a refused resend moved the proof"))
	})

	// A registration that did not say how the registrant will prove who they are
	// is refused rather than defaulted to passwordless, and refused before
	// anything is written: the same username registers afterwards, which it
	// could not if the refusal had left half a registrant behind.
	t.Run("a registration naming no credential is refused and leaves nobody behind", func(t *testing.T) {
		t.Parallel()

		request := registrationRequest()

		_, err := registrationDoor(t, s)(t.Context(), request)
		refused(t, s, err, codes.InvalidArgument, reasonNoCredentialNamed)

		register(t, s, withPassword(request))
	})

	// A client that sent the message and filled none of it in has a bug to
	// fix, and is told so rather than answered as though something failed.
	t.Run("an empty registration is a bad request", func(t *testing.T) {
		t.Parallel()

		_, err := registrationDoor(t, s)(t.Context(), &signinpb.RegisterRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		register(t, s, withPassword(registrationRequest()))
	})

	// The secret that claims an account goes to the person the account is
	// about, and never back to whoever called Register. The whole rendered
	// answer is searched rather than the fields somebody thought to name.
	t.Run("a registration's answer never carries the link that claims it", func(t *testing.T) {
		t.Parallel()

		who, registered := register(t, s, withPassword(registrationRequest()))
		link := mailedVerification(t, s, who.email)

		test.StrNotContains(t, registered.String(), link,
			test.Sprint("the registration's answer carried the verification link"))
	})

	// A username or address somebody holds is AlreadyExists, in words the
	// person at the form is meant to read — the refusal a sign-up page sees
	// most. The holder signing in afterwards is the control that the collision
	// overwrote nothing.
	t.Run("a registration colliding with a username or an address is AlreadyExists", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		holder, _ := register(t, s, withPassword(registrationRequest()))
		door := registrationDoor(t, s)

		sameUsername := withPassword(registrationRequest())
		sameUsername.User.Username = holder.username

		_, err := door(t.Context(), sameUsername)
		must.Error(t, err, must.Sprint("a second registrant was given a username somebody holds"))
		test.EqOp(t, codes.AlreadyExists, status.Code(err))
		test.StrContains(t, status.Convert(err).Message(), "username")

		sameAddress := withPassword(registrationRequest())
		sameAddress.User.EmailAddress = holder.email

		_, err = door(t.Context(), sameAddress)
		must.Error(t, err, must.Sprint("a second registrant was given an address somebody holds"))
		test.EqOp(t, codes.AlreadyExists, status.Code(err))
		test.StrContains(t, status.Convert(err).Message(), "email")

		verify(t, s, anon, holder)
		loggedIn(t, anon, holder.username, password)
	})

	// The invitation arm of registration, by the token the deployment mailed:
	// the registrant joins the inviting account with the roles they were
	// offered, and it is theirs to land in. A wrong token is refused before
	// anything is written — the same username and address register with the
	// right one afterwards, which they could not if the refusal had left
	// somebody behind.
	t.Run("a registration answering an invitation joins the inviter's account, and a wrong token registers nobody", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		inviter := directoryCaller(t, s, invite)
		if inviter.AccountID == "" {
			conformance.Skip(t, "conformance: this subject does not surface the inviter's account, so there is no account to be invited into; skipping")
		}

		delivered := s.Seams().Actions.InvitationToken
		s.NeedsAction(t, delivered != nil, "invitation token")

		membership := s.Roles().Membership
		roles := membership[:]
		addressed := freshEmail()

		invited, err := inviter.Surfaces.Identity.Invite(inviter.Context(t.Context()), &identitypb.InviteRequest{
			AccountId: inviter.AccountID,
			ToEmail:   addressed,
			ToName:    inviteeName,
			Roles:     roles,
		})
		must.NoError(t, err, must.Sprint("inviting somebody who has not registered"))

		token, err := delivered(t.Context(), inviter.ScopeFor(identitySurface), invited.GetInvitation().GetId())
		must.NoError(t, err, must.Sprint("reading the token the deployment delivered"))

		request := withPassword(registrationRequest())
		request.User.EmailAddress = addressed
		request.Account = nil
		request.Invitation = &signinpb.RegistrationInvitation{
			InvitationId: invited.GetInvitation().GetId(),
			Token:        "not the token",
		}

		_, err = registrationDoor(t, s)(t.Context(), request)
		must.Error(t, err, must.Sprint("a registration naming the wrong token was honored"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		request.Invitation.Token = token
		who, registered := register(t, s, request)

		test.Nil(t, registered.GetAccount(), test.Sprint("a registration by invitation minted an account of its own"))
		test.EqOp(t, identitypb.InvitationStatus_INVITATION_STATUS_ACCEPTED, registered.GetInvitation().GetStatus())
		test.EqOp(t, inviter.AccountID, registered.GetMembership().GetBelongsToAccount(),
			test.Sprint("an invited registrant joined somewhere other than the inviting account"))

		held := slices.Clone(registered.GetMembership().GetRoles())
		slices.Sort(held)
		offered := slices.Clone(roles)
		slices.Sort(offered)
		test.Eq(t, offered, held, test.Sprint("an invited registrant holds other roles than they were offered"))

		verify(t, s, anon, who)

		issued := loggedIn(t, anon, who.username, password)
		test.EqOp(t, inviter.AccountID, issued.GetActiveAccountId(),
			test.Sprint("an invited registrant's only account is not where they land"))
	})

	// Moving an address un-proves it, and the link mailed to the old address
	// dies with the move: a link that survived would let whoever reads the old
	// mailbox prove the new one. The dead link is refused as one never mailed
	// is, and the control is the door itself, answering the link mailed to
	// the address the person moved to.
	t.Run("moving an address kills the link mailed to the old one", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		sub, _ := signedIn(t, s, anon, updateEmailAddress, requestVerificationEmail)

		move := func(to string) {
			t.Helper()

			_, err := sub.Surfaces.SignIn.UpdateEmailAddress(sub.Context(t.Context()), &signinpb.UpdateEmailAddressRequest{
				CurrentPassword: password,
				NewEmailAddress: to,
			})
			must.NoError(t, err, must.Sprint("moving the signed-in person's address"))
		}

		resend := func(to string) string {
			t.Helper()

			_, err := sub.Surfaces.SignIn.RequestVerificationEmail(sub.Context(t.Context()),
				&signinpb.RequestVerificationEmailRequest{})
			must.NoError(t, err, must.Sprint("asking for a verification link for a moved address"))

			return mailedVerification(t, s, to)
		}

		// Read before the move: a deployment may find a mailed link by the
		// address it went to.
		old := freshEmail()
		move(old)
		stale := resend(old)

		moved := freshEmail()
		move(moved)

		_, never := anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: identifiers.New()})
		_, err := anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: stale})
		refused(t, s, err, codes.Unauthenticated, reasonInvalidCredentials)
		indistinguishable(t, s, never, err, "a link mailed before an address moved against one never mailed")

		_, err = anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: resend(moved)})
		must.NoError(t, err, must.Sprint("the link mailed to the address moved to did not verify"))
	})

	// A sender who copied an invitation's link holds the link the mail
	// carries, and it does what the mailed one does: registers the person it
	// was addressed to into the inviting account. Somebody else registering
	// with it is refused as a wrong token is, and the refusal leaves the link
	// standing for the person it was meant for.
	t.Run("a copied invitation link registers the addressed person into the inviting account", func(t *testing.T) {
		t.Parallel()

		if !s.Seams().InvitationTokenReturned {
			conformance.Skip(t, "conformance: this subject does not return an invitation's token to its sender (Seams.InvitationTokenReturned), so there is no copied link; skipping")
		}

		inviter := directoryCaller(t, s, invite)
		if inviter.AccountID == "" {
			conformance.Skip(t, "conformance: this subject does not surface the inviter's account, so there is no account to be invited into; skipping")
		}

		addressed := freshEmail()

		invited, err := inviter.Surfaces.Identity.Invite(inviter.Context(t.Context()), &identitypb.InviteRequest{
			AccountId: inviter.AccountID,
			ToEmail:   addressed,
			ToName:    inviteeName,
			Roles:     []string{s.Roles().Membership[0]},
		})
		must.NoError(t, err, must.Sprint("inviting somebody who has not registered"))

		link := &signinpb.RegistrationInvitation{
			InvitationId: invited.GetInvitation().GetId(),
			Token:        invited.GetToken(),
		}
		must.NotEqOp(t, "", link.GetToken(), must.Sprint("a subject that returns the token returned none"))

		stranger := withPassword(registrationRequest())
		stranger.Account = nil
		stranger.Invitation = link

		_, err = registrationDoor(t, s)(t.Context(), stranger)
		must.Error(t, err, must.Sprint("a copied link registered somebody it was not addressed to"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		request := withPassword(registrationRequest())
		request.User.EmailAddress = addressed
		request.Account = nil
		request.Invitation = link

		_, registered := register(t, s, request)

		test.Nil(t, registered.GetAccount(), test.Sprint("a registration by invitation minted an account of its own"))
		test.EqOp(t, inviter.AccountID, registered.GetMembership().GetBelongsToAccount(),
			test.Sprint("a copied link registered its addressee somewhere other than the inviting account"))
		test.EqOp(t, identitypb.InvitationStatus_INVITATION_STATUS_ACCEPTED, registered.GetInvitation().GetStatus())
	})
}

// passwordlessRegistration is the registrant who names no password: the arrival
// a deployment's registration policy may refuse, and declares that it does in
// Seams.PasswordlessRegistrationRefused. Where it is admitted, the registrant
// attaches a password through the mail that verifies them; where it is refused,
// the refusal is the policy's, and the assertions about attaching one skip.
func passwordlessRegistration(t *testing.T, s *conformance.Session) {
	t.Helper()

	// A policy refusing the arm is answered as any policy's refusal is, with the
	// code and reason the contract lists, and refuses before anything is
	// written: the same username and address register with a password
	// afterwards, which they could not if the refusal had left half a
	// registrant behind.
	t.Run("a deployment refusing passwordless registration refuses it by its policy and leaves nobody behind", func(t *testing.T) {
		t.Parallel()

		if !s.Seams().PasswordlessRegistrationRefused {
			conformance.Skip(t, "conformance: this subject admits a registrant who names no password (Seams.PasswordlessRegistrationRefused is false), so there is no refusal to assert; skipping")
		}

		request := withNoPassword(registrationRequest())

		_, err := registrationDoor(t, s)(t.Context(), request)
		refused(t, s, err, codes.InvalidArgument, reasonRegistrationRefused)

		register(t, s, withPassword(request))
	})

	// The other arrival: somebody who named no password claims their account
	// from the same mail, with nobody signed in. Attaching does not spend the
	// link, so the one click goes on to verify.
	t.Run("a registrant with no password attaches one through the mailed link", func(t *testing.T) {
		t.Parallel()

		admitsPasswordless(t, s)

		anon := anonymous(t, s, attachPassword, verifyEmailAddress, loginForToken)
		who, _ := register(t, s, withNoPassword(registrationRequest()))
		link := mailedVerification(t, s, who.email)

		_, err := anon.AttachPassword(t.Context(), &signinpb.AttachPasswordRequest{Token: link, NewPassword: password})
		must.NoError(t, err, must.Sprint("attaching a first password through the mailed link"))

		_, err = anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: link})
		must.NoError(t, err, must.Sprint("attaching a password spent the link it was answered with"))

		loggedIn(t, anon, who.username, password)
	})

	// What keeps a verification link from being a password reset: against an
	// account that holds a password it can do nothing. The control is the same
	// door furnishing an account that holds none, and the password afterwards
	// is still the one the registrant chose.
	t.Run("a mailed link cannot replace a password somebody already holds", func(t *testing.T) {
		t.Parallel()

		admitsPasswordless(t, s)

		anon := anonymous(t, s, attachPassword, verifyEmailAddress, loginForToken)

		without, _ := register(t, s, withNoPassword(registrationRequest()))
		_, err := anon.AttachPassword(t.Context(), &signinpb.AttachPasswordRequest{
			Token:       mailedVerification(t, s, without.email),
			NewPassword: password,
		})
		must.NoError(t, err, must.Sprint("the control: an account with no password could not be given one"))

		holder, _ := register(t, s, withPassword(registrationRequest()))
		link := mailedVerification(t, s, holder.email)

		const chosenBySomebodyElse = "a password somebody else chose, long enough"

		_, err = anon.AttachPassword(t.Context(), &signinpb.AttachPasswordRequest{
			Token:       link,
			NewPassword: chosenBySomebodyElse,
		})
		refused(t, s, err, codes.FailedPrecondition, reasonPasswordAlreadySet)

		_, err = anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: link})
		must.NoError(t, err)

		loggedIn(t, anon, holder.username, password)

		_, err = login(t.Context(), anon, holder.username, chosenBySomebodyElse, "")
		test.Error(t, err, test.Sprint("a refused attach changed the password anyway"))
	})
}

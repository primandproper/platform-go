package signin

import (
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

	// The property this surface exists for: somebody registered over the wire
	// can sign in over the wire. They cannot before their address is proven,
	// and the refusal says why in a form a client can send them to
	// verification on; the link they were mailed, answered with nobody signed
	// in, is what lets them in.
	t.Run("a registrant signs in once the mailed link proves their address", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		who, registered := register(t, s, withPassword(registrationRequest(s)))

		test.EqOp(t, who.username, registered.GetUser().GetUsername())
		test.NotNil(t, registered.GetAccount(), test.Sprint("a registration naming an account answered with none"))
		test.NotNil(t, registered.GetMembership(), test.Sprint("a registration answered with no membership"))

		_, err := login(t.Context(), anon, who.username, password, "")
		refused(t, s, err, codes.FailedPrecondition, reasonUserUnverified)

		verify(t, s, anon, who)

		issued := loggedIn(t, anon, who.username, password)
		test.NotEqOp(t, "", issued.GetToken())
	})

	// The other arrival: somebody who named no password claims their account
	// from the same mail, with nobody signed in. Attaching does not spend the
	// link, so the one click goes on to verify.
	t.Run("a registrant with no password attaches one through the mailed link", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, attachPassword, verifyEmailAddress, loginForToken)
		who, _ := register(t, s, withNoPassword(registrationRequest(s)))
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

		anon := anonymous(t, s, attachPassword, verifyEmailAddress, loginForToken)

		without, _ := register(t, s, withNoPassword(registrationRequest(s)))
		_, err := anon.AttachPassword(t.Context(), &signinpb.AttachPasswordRequest{
			Token:       mailedVerification(t, s, without.email),
			NewPassword: password,
		})
		must.NoError(t, err, must.Sprint("the control: an account with no password could not be given one"))

		holder, _ := register(t, s, withPassword(registrationRequest(s)))
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
		who, _ := register(t, s, withPassword(registrationRequest(s)))
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

		who, _ := register(t, s, withPassword(registrationRequest(s)))
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

		who, _ := register(t, s, withPassword(registrationRequest(s)))
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

		by := registrar(t, s)
		request := registrationRequest(s)

		_, err := by.Surfaces.SignIn.Register(by.Context(t.Context()), request)
		refused(t, s, err, codes.InvalidArgument, reasonNoCredentialNamed)

		register(t, s, withPassword(request))
	})

	// A client that sent the message and filled none of it in has a bug to
	// fix, and is told so rather than answered as though something failed.
	t.Run("an empty registration is a bad request", func(t *testing.T) {
		t.Parallel()

		by := registrar(t, s)

		_, err := by.Surfaces.SignIn.Register(by.Context(t.Context()), &signinpb.RegisterRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		register(t, s, withPassword(registrationRequest(s)))
	})

	// The secret that claims an account goes to the person the account is
	// about, and never back to whoever called Register. The whole rendered
	// answer is searched rather than the fields somebody thought to name.
	t.Run("a registration's answer never carries the link that claims it", func(t *testing.T) {
		t.Parallel()

		who, registered := register(t, s, withNoPassword(registrationRequest(s)))
		link := mailedVerification(t, s, who.email)

		test.StrNotContains(t, registered.String(), link,
			test.Sprint("the registration's answer carried the verification link"))
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
			ToName:    "Some Body",
			Roles:     []string{s.Roles().Membership[0]},
		})
		must.NoError(t, err, must.Sprint("inviting somebody who has not registered"))

		link := &signinpb.RegistrationInvitation{
			InvitationId: invited.GetInvitation().GetId(),
			Token:        invited.GetToken(),
		}
		must.NotEqOp(t, "", link.GetToken(), must.Sprint("a subject that returns the token returned none"))

		by := registrar(t, s)

		stranger := withPassword(registrationRequest(s))
		stranger.Account, stranger.OwnerRoles = nil, nil
		stranger.Invitation = link

		_, err = by.Surfaces.SignIn.Register(by.Context(t.Context()), stranger)
		must.Error(t, err, must.Sprint("a copied link registered somebody it was not addressed to"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		request := withPassword(registrationRequest(s))
		request.User.EmailAddress = addressed
		request.Account, request.OwnerRoles = nil, nil
		request.Invitation = link

		_, registered := register(t, s, request)

		test.Nil(t, registered.GetAccount(), test.Sprint("a registration by invitation minted an account of its own"))
		test.EqOp(t, inviter.AccountID, registered.GetMembership().GetBelongsToAccount(),
			test.Sprint("a copied link registered its addressee somewhere other than the inviting account"))
		test.EqOp(t, identitypb.InvitationStatus_INVITATION_STATUS_ACCEPTED, registered.GetInvitation().GetStatus())
	})
}

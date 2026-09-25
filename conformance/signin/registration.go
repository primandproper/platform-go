package signin

import (
	"testing"

	domain "github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"

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

		anon := anonymous(t, s)
		who, registered := register(t, s, withPassword(registrationRequest()))

		test.EqOp(t, who.username, registered.GetUser().GetUsername())
		test.NotNil(t, registered.GetAccount(), test.Sprint("a registration naming an account answered with none"))
		test.NotNil(t, registered.GetMembership(), test.Sprint("a registration answered with no membership"))

		_, err := login(t.Context(), anon, who.username, password, "")
		refused(t, err, codes.FailedPrecondition, reasonUserUnverified)

		verify(t, s, anon, who)

		issued := loggedIn(t, anon, who.username, password)
		test.NotEqOp(t, "", issued.GetToken())
	})

	// The other arrival: somebody who named no password claims their account
	// from the same mail, with nobody signed in. Attaching does not spend the
	// link, so the one click goes on to verify.
	t.Run("a registrant with no password attaches one through the mailed link", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s)
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

		anon := anonymous(t, s)

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
		refused(t, err, codes.FailedPrecondition, reasonPasswordAlreadySet)

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

		anon := anonymous(t, s)

		_, err := anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: identifiers.New()})
		refused(t, err, codes.Unauthenticated, reasonInvalidCredentials)
		test.EqOp(t, domain.ErrInvalidCredentials.Error(), status.Convert(err).Message())

		// A spent link lands in the same place.
		who, _ := register(t, s, withPassword(registrationRequest()))
		link := mailedVerification(t, s, who.email)

		_, spendErr := anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: link})
		must.NoError(t, spendErr, must.Sprint("the control: the link the deployment mailed did not verify"))

		_, spent := anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{Token: link})
		indistinguishable(t, err, spent, "a spent verification link against one never mailed")
	})

	// A registration that did not say how the registrant will prove who they are
	// is refused rather than defaulted to passwordless, and refused before
	// anything is written: the same username registers afterwards, which it
	// could not if the refusal had left half a registrant behind.
	t.Run("a registration naming no credential is refused and leaves nobody behind", func(t *testing.T) {
		t.Parallel()

		by := registrar(t, s)
		request := registrationRequest()

		_, err := by.Surfaces.SignIn.Register(by.Context(t.Context()), request)
		refused(t, err, codes.InvalidArgument, reasonNoCredentialNamed)

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

		register(t, s, withPassword(registrationRequest()))
	})

	// The secret that claims an account goes to the person the account is
	// about, and never back to whoever called Register. The whole rendered
	// answer is searched rather than the fields somebody thought to name.
	t.Run("a registration's answer never carries the link that claims it", func(t *testing.T) {
		t.Parallel()

		who, registered := register(t, s, withNoPassword(registrationRequest()))
		link := mailedVerification(t, s, who.email)

		test.StrNotContains(t, registered.String(), link,
			test.Sprint("the registration's answer carried the verification link"))
	})
}

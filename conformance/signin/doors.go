package signin

import (
	"slices"
	"testing"
	"time"

	domain "github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func doors(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("the right password signs in, for the registrant's own account", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s)
		who := signInAs(t, s, anon)

		issued := loggedIn(t, anon, who.username, password)

		test.NotEqOp(t, "", issued.GetToken(), test.Sprint("a sign-in answered with no access token"))
		test.NotEqOp(t, "", issued.GetTokenId(), test.Sprint("a sign-in answered with no token identifier"))
		test.EqOp(t, who.accountID, issued.GetActiveAccountId(),
			test.Sprint("a sign-in naming no account landed somewhere other than the registrant's only one"))
		test.False(t, issued.GetAdministrative(), test.Sprint("the ordinary door minted an administrative token"))

		// The contract populates the family whether or not a refresh token was
		// stored, because it names a sign-in rather than a row.
		test.NotEqOp(t, "", issued.GetFamilyId(), test.Sprint("a sign-in answered with no family"))

		// Whole seconds and generously: what is asserted is that the token is
		// not born expired, not how long it lives, which is the deployment's.
		must.NotNil(t, issued.GetExpiresAt(), must.Sprint("a sign-in answered with no expiry"))
		test.True(t, issued.GetExpiresAt().AsTime().After(time.Now().Add(-time.Second)),
			test.Sprint("a sign-in answered with a token already expired"))
	})

	// The refusal the whole package is organized around. Telling an unknown
	// handle from a wrong password is telling whoever is guessing which half of
	// the guess was right, so the two must be the same answer on every channel a
	// client reads — and the right password must still get in, or a door that
	// refused everybody would pass.
	t.Run("a wrong password and an unknown username are one answer", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s)
		who := signInAs(t, s, anon)

		_, wrong := login(t.Context(), anon, who.username, wrongPassword, "")
		refused(t, wrong, codes.Unauthenticated, reasonInvalidCredentials)

		// The sentinel's own words rather than the code's name, which is what a
		// client with no access to the details reads.
		test.EqOp(t, domain.ErrInvalidCredentials.Error(), status.Convert(wrong).Message())

		_, unknown := login(t.Context(), anon, "conf_"+identifiers.New(), password, "")
		indistinguishable(t, wrong, unknown, "an unknown username against a wrong password")

		loggedIn(t, anon, who.username, password)
	})

	// The calling code being wrong rather than a guess at a credential, so it is
	// told to correct its request rather than to try another password.
	t.Run("a sign-in naming no credentials is a bad request", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s)
		who := signInAs(t, s, anon)

		_, err := anon.LoginForToken(t.Context(), &signinpb.LoginForTokenRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		loggedIn(t, anon, who.username, password)
	})

	// The one disclosure the door makes on purpose, made in a form a client can
	// branch on: the password was right and a code is needed. A wrong code is
	// then the ordinary refusal, and the right one gets in.
	t.Run("a user with a second factor is told to send a code, by reason", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s)
		sub, user := passworded(t, s)
		secret := enroll(t, sub)

		_, withoutCode := login(t.Context(), anon, user.GetUsername(), password, "")
		refused(t, withoutCode, codes.Unauthenticated, reasonSecondFactorRequired)
		test.EqOp(t, domain.ErrSecondFactorRequired.Error(), status.Convert(withoutCode).Message())

		_, badPassword := login(t.Context(), anon, user.GetUsername(), wrongPassword, "")
		_, badCode := login(t.Context(), anon, user.GetUsername(), password, wrongCode(t, secret))
		indistinguishable(t, badPassword, badCode, "a wrong code against a wrong password")
		test.EqOp(t, reasonInvalidCredentials, reason(badCode))

		issued, err := login(t.Context(), anon, user.GetUsername(), password, code(t, secret))
		must.NoError(t, err, must.Sprint("the right password and the right code did not sign in"))
		test.NotEqOp(t, "", issued.GetToken())
	})

	// Whether a deployment has an administrative door at all is its own
	// decision, and the two ways of being refused there — no door, or not
	// admitted through it — share a code and mean the same to a person. What
	// every deployment owes is that somebody it never made an administrator is
	// told to stop asking rather than to retype a password that was right.
	t.Run("the administrative door refuses somebody never made an administrator", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s)
		who := signInAs(t, s, anon)

		_, err := anon.AdminLoginForToken(t.Context(), &signinpb.AdminLoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: who.username, Password: password},
		})
		must.Error(t, err, must.Sprint("somebody nobody made an administrator signed in as one"))
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
		test.True(t, slices.Contains([]string{reasonNotAnAdministrator, reasonAdminSignInUnavailable}, reason(err)),
			test.Sprintf("the administrative refusal carried reason %q", reason(err)))

		// The same credentials through the ordinary door, so the refusal above
		// was about the door rather than the password.
		issued := loggedIn(t, anon, who.username, password)
		test.False(t, issued.GetAdministrative())
	})
}

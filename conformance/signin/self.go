package signin

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

func self(t *testing.T, s *conformance.Session) {
	t.Helper()

	// "Am I signed in" is a question whose answer can be no, and a client asks
	// it on load precisely because it does not know. Refusing it would make
	// every client treat its own first question as an error.
	t.Run("a client with nobody signed in is told so rather than refused", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s)

		nobody, err := anon.GetAuthStatus(t.Context(), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err, must.Sprint("asking whether anybody is signed in was refused"))
		test.False(t, nobody.GetAuthenticated())
		test.Nil(t, nobody.GetStatus(), test.Sprint("nobody signed in was answered with somebody's status"))

		// The control: the same question from a caller is answered yes, so the
		// no above was about the request rather than about the surface.
		sub := registrar(t, s)

		somebody, err := sub.Surfaces.SignIn.GetAuthStatus(sub.Context(t.Context()), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.True(t, somebody.GetAuthenticated())
	})

	t.Run("a caller's status names them, the account they are in, and their credentials", func(t *testing.T) {
		t.Parallel()

		sub := registrar(t, s)

		before, err := sub.Surfaces.SignIn.GetAuthStatus(sub.Context(t.Context()), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		must.True(t, before.GetAuthenticated())

		standing := before.GetStatus()
		must.NotNil(t, standing)

		if sub.UserID != "" {
			test.EqOp(t, sub.UserID, standing.GetUser().GetId())
		}

		if sub.AccountID != "" {
			test.EqOp(t, sub.AccountID, standing.GetActiveAccountId())
			test.SliceContains(t, standing.GetAccountIds(), sub.AccountID,
				test.Sprint("the caller's active account is not among the accounts it is a member of"))
		}

		test.False(t, standing.GetTwoFactorEnrolled())
	})

	// has_password is a fact a client renders a door from — "set a password"
	// against "change your password" — so it has to follow the credential
	// rather than a guess about how the account arrived.
	t.Run("a caller's status says whether they hold a password", func(t *testing.T) {
		t.Parallel()

		sub, _ := passworded(t, s)

		response, err := sub.Surfaces.SignIn.GetAuthStatus(sub.Context(t.Context()), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.True(t, response.GetStatus().GetHasPassword(), test.Sprint("a caller who set a password is reported as holding none"))
	})

	t.Run("a caller reads themselves", func(t *testing.T) {
		t.Parallel()

		sub, user := passworded(t, s)

		response, err := sub.Surfaces.SignIn.GetSelf(sub.Context(t.Context()), &signinpb.GetSelfRequest{})
		must.NoError(t, err)

		test.EqOp(t, user.GetId(), response.GetUser().GetId())
		test.EqOp(t, user.GetUsername(), response.GetUser().GetUsername())
	})

	// Being signed in is not by itself proof enough to change the credential the
	// sign-in was obtained with, so the current password is checked — and a
	// refused change changes nothing.
	t.Run("a password change needs the current password and then takes effect", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s)
		sub, user := passworded(t, s)

		_, err := sub.Surfaces.SignIn.UpdatePassword(sub.Context(t.Context()), &signinpb.UpdatePasswordRequest{
			CurrentPassword: wrongPassword,
			NewPassword:     newPassword,
		})
		refused(t, s, err, codes.Unauthenticated, reasonInvalidCredentials)
		loggedIn(t, anon, user.GetUsername(), password)

		_, err = sub.Surfaces.SignIn.UpdatePassword(sub.Context(t.Context()), &signinpb.UpdatePasswordRequest{
			CurrentPassword: password,
			NewPassword:     newPassword,
		})
		must.NoError(t, err, must.Sprint("the right current password could not change it"))

		loggedIn(t, anon, user.GetUsername(), newPassword)

		_, err = login(t.Context(), anon, user.GetUsername(), password, "")
		test.Error(t, err, test.Sprint("the password that was replaced still signs in"))
	})

	// Enrollment is two calls, and between them the caller holds no second
	// factor at all: an issued secret becomes one only once a code from it has
	// been proven.
	t.Run("a second factor is a secret and then a proof of it", func(t *testing.T) {
		t.Parallel()

		sub, _ := passworded(t, s)

		refreshed, err := sub.Surfaces.SignIn.RefreshTOTPSecret(sub.Context(t.Context()),
			&signinpb.RefreshTOTPSecretRequest{CurrentPassword: password})
		must.NoError(t, err)
		must.NotEqOp(t, "", refreshed.GetSecret())
		test.StrContains(t, refreshed.GetProvisioningUri(), "otpauth://totp/")

		unproven, err := sub.Surfaces.SignIn.GetAuthStatus(sub.Context(t.Context()), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.False(t, unproven.GetStatus().GetTwoFactorEnrolled(),
			test.Sprint("an issued secret nobody proved counts as a second factor"))

		_, err = sub.Surfaces.SignIn.VerifyTOTPSecret(sub.Context(t.Context()),
			&signinpb.VerifyTOTPSecretRequest{TotpCode: code(t, refreshed.GetSecret())})
		must.NoError(t, err)

		proven, err := sub.Surfaces.SignIn.GetAuthStatus(sub.Context(t.Context()), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.True(t, proven.GetStatus().GetTwoFactorEnrolled())
	})

	// A code for a secret nobody was issued is a state to fix, not a guess to
	// retry, and the reason says which state.
	t.Run("proving a second factor nobody issued is refused as a precondition", func(t *testing.T) {
		t.Parallel()

		sub, _ := passworded(t, s)

		_, err := sub.Surfaces.SignIn.VerifyTOTPSecret(sub.Context(t.Context()),
			&signinpb.VerifyTOTPSecretRequest{TotpCode: "000000"})
		refused(t, s, err, codes.FailedPrecondition, reasonSecondFactorNotEnrolled)

		// The control: once a secret is issued, the same call with its code is
		// the proof it was refused for lacking.
		enroll(t, sub)
	})
}

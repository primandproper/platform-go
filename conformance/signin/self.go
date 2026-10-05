package signin

import (
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/identifiers"

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

		anon := anonymous(t, s, getAuthStatus)

		nobody, err := anon.GetAuthStatus(t.Context(), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err, must.Sprint("asking whether anybody is signed in was refused"))
		test.False(t, nobody.GetAuthenticated())
		test.Nil(t, nobody.GetStatus(), test.Sprint("nobody signed in was answered with somebody's status"))

		// The control: the same question from a caller is answered yes, so the
		// no above was about the request rather than about the surface.
		sub := member(t, s, getAuthStatus)

		somebody, err := sub.Surfaces.SignIn.GetAuthStatus(sub.Context(t.Context()), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.True(t, somebody.GetAuthenticated())
	})

	t.Run("a caller's status names them, the account they are in, and their credentials", func(t *testing.T) {
		t.Parallel()

		sub := member(t, s, getAuthStatus, getSelf)

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

		// A registration policy may enroll a second factor, so a fresh caller's
		// enrollment is whatever the deployment's own record of them says: the
		// status agrees with the proof the self read carries.
		me, err := sub.Surfaces.SignIn.GetSelf(sub.Context(t.Context()), &signinpb.GetSelfRequest{})
		must.NoError(t, err)
		test.EqOp(t, me.GetUser().GetTwoFactorSecretVerifiedAt() != nil, standing.GetTwoFactorEnrolled(),
			test.Sprint("the status's second factor disagrees with the caller's own record of one"))
	})

	// has_password is a fact a client renders a door from — "set a password"
	// against "change your password" — so it has to follow the credential
	// rather than a guess about how the account arrived.
	t.Run("a caller's status says whether they hold a password", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		sub, _ := signedIn(t, s, anon, getAuthStatus)

		response, err := sub.Surfaces.SignIn.GetAuthStatus(sub.Context(t.Context()), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.True(t, response.GetStatus().GetHasPassword(), test.Sprint("a caller who set a password is reported as holding none"))
	})

	t.Run("a caller reads themselves", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		sub, who := signedIn(t, s, anon, getSelf)

		response, err := sub.Surfaces.SignIn.GetSelf(sub.Context(t.Context()), &signinpb.GetSelfRequest{})
		must.NoError(t, err)

		test.EqOp(t, who.userID, response.GetUser().GetId())
		test.EqOp(t, who.username, response.GetUser().GetUsername())
	})

	// Being signed in is not by itself proof enough to change the credential the
	// sign-in was obtained with, so the current password is checked — and a
	// refused change changes nothing.
	t.Run("a password change needs the current password and then takes effect", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		sub, who := signedIn(t, s, anon, updatePassword)

		_, err := sub.Surfaces.SignIn.UpdatePassword(sub.Context(t.Context()), &signinpb.UpdatePasswordRequest{
			CurrentPassword: wrongPassword,
			NewPassword:     newPassword,
		})
		refused(t, s, err, codes.PermissionDenied, reasonInvalidCredentials)
		loggedIn(t, anon, who.username, password)

		_, err = sub.Surfaces.SignIn.UpdatePassword(sub.Context(t.Context()), &signinpb.UpdatePasswordRequest{
			CurrentPassword: password,
			NewPassword:     newPassword,
		})
		must.NoError(t, err, must.Sprint("the right current password could not change it"))

		loggedIn(t, anon, who.username, newPassword)

		_, err = login(t.Context(), anon, who.username, password, "")
		test.Error(t, err, test.Sprint("the password that was replaced still signs in"))
	})

	// An address is where a password reset goes, so moving it is guarded as a
	// password change is: a wrong password is refused and moves nothing, and
	// the right one moves it.
	t.Run("an address change needs the current password and then takes effect", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		sub, who := signedIn(t, s, anon, updateEmailAddress, getSelf)
		ctx := sub.Context(t.Context())

		before, err := sub.Surfaces.SignIn.GetSelf(ctx, &signinpb.GetSelfRequest{})
		must.NoError(t, err)

		moved := freshEmail()

		_, err = sub.Surfaces.SignIn.UpdateEmailAddress(ctx, &signinpb.UpdateEmailAddressRequest{
			CurrentPassword: wrongPassword,
			NewEmailAddress: moved,
		})
		refused(t, s, err, codes.PermissionDenied, reasonInvalidCredentials)

		unmoved, err := sub.Surfaces.SignIn.GetSelf(ctx, &signinpb.GetSelfRequest{})
		must.NoError(t, err)
		test.EqOp(t, before.GetUser().GetEmailAddress(), unmoved.GetUser().GetEmailAddress(),
			test.Sprint("a refused address change moved the address anyway"))

		changed, err := sub.Surfaces.SignIn.UpdateEmailAddress(ctx, &signinpb.UpdateEmailAddressRequest{
			CurrentPassword: password,
			NewEmailAddress: moved,
		})
		must.NoError(t, err, must.Sprint("the right current password could not move the address"))
		test.EqOp(t, moved, changed.GetUser().GetEmailAddress())
		test.Nil(t, changed.GetUser().GetEmailAddressVerifiedAt(), test.Sprint("a moved address kept the old one's proof"))

		after, err := sub.Surfaces.SignIn.GetSelf(ctx, &signinpb.GetSelfRequest{})
		must.NoError(t, err)
		test.EqOp(t, moved, after.GetUser().GetEmailAddress())
		test.EqOp(t, who.userID, after.GetUser().GetId())
	})

	// A username is what somebody signs in with, so it is guarded the same
	// way, and what lands is observable at the door: the new name signs in.
	t.Run("a username change needs the current password and then takes effect", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		sub, who := signedIn(t, s, anon, updateUsername)
		ctx := sub.Context(t.Context())

		renamed := "conf_" + identifiers.New()

		_, err := sub.Surfaces.SignIn.UpdateUsername(ctx, &signinpb.UpdateUsernameRequest{
			CurrentPassword: wrongPassword,
			NewUsername:     renamed,
		})
		refused(t, s, err, codes.PermissionDenied, reasonInvalidCredentials)

		// Before the success below, so nothing but the refusal could have
		// moved it.
		loggedIn(t, anon, who.username, password)

		changed, err := sub.Surfaces.SignIn.UpdateUsername(ctx, &signinpb.UpdateUsernameRequest{
			CurrentPassword: password,
			NewUsername:     renamed,
		})
		must.NoError(t, err, must.Sprint("the right current password could not rename"))
		test.EqOp(t, renamed, changed.GetUser().GetUsername())

		loggedIn(t, anon, renamed, password)

		_, err = login(t.Context(), anon, who.username, password, "")
		test.Error(t, err, test.Sprint("the username that was replaced still signs in"))
	})

	// Enrollment is two calls, and between them the caller holds no second
	// factor at all: an issued secret becomes one only once a code from it has
	// been proven.
	t.Run("a second factor is a secret and then a proof of it", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		sub, _ := signedIn(t, s, anon, refreshTOTPSecret, getAuthStatus, verifyTOTPSecret)

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

		if s.Seams().RegistrationIssuesSecondFactor {
			conformance.Skip(t, "conformance: this subject says its registration issues every registrant a second-factor secret (Seams.RegistrationIssuesSecondFactor), so nobody here holds none; skipping")
		}

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		sub, _ := signedIn(t, s, anon, verifyTOTPSecret, refreshTOTPSecret)

		_, err := sub.Surfaces.SignIn.VerifyTOTPSecret(sub.Context(t.Context()),
			&signinpb.VerifyTOTPSecretRequest{TotpCode: "000000"})
		refused(t, s, err, codes.FailedPrecondition, reasonSecondFactorNotEnrolled)

		// The control: once a secret is issued, the same call with its code is
		// the proof it was refused for lacking.
		enroll(t, sub)
	})

	// A registration issues a second-factor secret exactly where the subject
	// says its policy has it do so, and what it issues is a secret rather than
	// a factor: the registrant holds none until they prove it, a wrong code is
	// refused as a wrong code, and the right one is the proof.
	t.Run("a registration issues a second-factor secret where the deployment says, unproven", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		who, registered := register(t, s, withPassword(registrationRequest()))
		issued := registered.GetTotpEnrollment()

		if !s.Seams().RegistrationIssuesSecondFactor {
			test.Nil(t, issued, test.Sprint("a registration issued a second-factor secret, and this subject does not say its policy has it do so (Seams.RegistrationIssuesSecondFactor)"))

			return
		}

		must.NotNil(t, issued, must.Sprint("this subject says its registration issues a second-factor secret (Seams.RegistrationIssuesSecondFactor), and it answered with none"))
		must.NotEqOp(t, "", issued.GetSecret(), must.Sprint("a registration issued a second factor with no secret"))

		if s.Seams().SignedIn == nil {
			conformance.Skip(t, "conformance: this subject supplies no SignedIn seam, so nobody the suite signs in can be called as; skipping")
		}

		verify(t, s, anon, who)

		sub := caller(t, s, loggedIn(t, anon, who.username, password), getAuthStatus, verifyTOTPSecret)
		ctx := sub.Context(t.Context())

		unproven, err := sub.Surfaces.SignIn.GetAuthStatus(ctx, &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.False(t, unproven.GetStatus().GetTwoFactorEnrolled(),
			test.Sprint("a secret a registration issued counts as a second factor before anybody proved it"))

		_, err = sub.Surfaces.SignIn.VerifyTOTPSecret(ctx, &signinpb.VerifyTOTPSecretRequest{TotpCode: wrongCode(t, issued.GetSecret())})
		refused(t, s, err, codes.Unauthenticated, reasonInvalidCredentials)

		_, err = sub.Surfaces.SignIn.VerifyTOTPSecret(ctx, &signinpb.VerifyTOTPSecretRequest{TotpCode: code(t, issued.GetSecret())})
		must.NoError(t, err, must.Sprint("the right code for the secret a registration issued did not prove it"))

		proven, err := sub.Surfaces.SignIn.GetAuthStatus(ctx, &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.True(t, proven.GetStatus().GetTwoFactorEnrolled())
	})

	// Replacing a second factor is replacing a credential, so it asks for
	// every credential the caller holds: the password always, and the current
	// code once there is one. A refusal replaces nothing, which is only
	// observable as the old secret still signing in.
	t.Run("a new second-factor secret needs the current password, and the current code once enrolled", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		sub, who := signedIn(t, s, anon, refreshTOTPSecret, verifyTOTPSecret)
		ctx := sub.Context(t.Context())

		_, err := sub.Surfaces.SignIn.RefreshTOTPSecret(ctx, &signinpb.RefreshTOTPSecretRequest{CurrentPassword: wrongPassword})
		refused(t, s, err, codes.PermissionDenied, reasonInvalidCredentials)

		// The control, and the enrollment the rest is about.
		secret := enroll(t, sub)

		_, err = sub.Surfaces.SignIn.RefreshTOTPSecret(ctx, &signinpb.RefreshTOTPSecretRequest{CurrentPassword: password})
		refused(t, s, err, codes.PermissionDenied, reasonSecondFactorRequired)

		_, err = sub.Surfaces.SignIn.RefreshTOTPSecret(ctx, &signinpb.RefreshTOTPSecretRequest{
			CurrentPassword: password,
			TotpCode:        wrongCode(t, secret),
		})
		refused(t, s, err, codes.PermissionDenied, reasonInvalidCredentials)

		// Before the success below rather than after it, so that nothing but
		// the refusals above could have replaced the secret this code is from.
		_, err = login(t.Context(), anon, who.username, password, code(t, secret))
		must.NoError(t, err, must.Sprint("a refused refresh replaced the second factor anyway"))

		refreshed, err := sub.Surfaces.SignIn.RefreshTOTPSecret(ctx, &signinpb.RefreshTOTPSecretRequest{
			CurrentPassword: password,
			TotpCode:        code(t, secret),
		})
		must.NoError(t, err, must.Sprint("the right password and the right code did not issue a new secret"))
		test.NotEqOp(t, "", refreshed.GetSecret())
		test.NotEqOp(t, secret, refreshed.GetSecret(), test.Sprint("a new second-factor secret is the old one"))
	})

	// The flag is how a client is told to send somebody to the form, which is
	// why the door still admits them: a user a forced change locked out could
	// never reach the form. It ends with the change it asked for.
	t.Run("a forced password change still signs in, is reported, and ends with the change", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		sub, who := signedIn(t, s, anon, getAuthStatus)
		imposer := directoryCaller(t, s, setUserRequiresPasswordChange)

		before, err := sub.Surfaces.SignIn.GetAuthStatus(sub.Context(t.Context()), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.False(t, before.GetStatus().GetRequiresPasswordChange(),
			test.Sprint("a fresh registrant already owed a password change"))

		_, err = imposer.Surfaces.Identity.SetUserRequiresPasswordChange(imposer.Context(t.Context()),
			&identitypb.SetUserRequiresPasswordChangeRequest{UserId: who.userID, RequiresPasswordChange: new(true)})
		must.NoError(t, err, must.Sprint("imposing a forced password change"))

		forced := caller(t, s, loggedIn(t, anon, who.username, password), getAuthStatus, updatePassword)
		ctx := forced.Context(t.Context())

		owed, err := forced.Surfaces.SignIn.GetAuthStatus(ctx, &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err, must.Sprint("a caller owing a password change could not ask where they stand"))
		test.True(t, owed.GetStatus().GetRequiresPasswordChange(),
			test.Sprint("a forced password change was not reported to the client that has to act on it"))

		_, err = forced.Surfaces.SignIn.UpdatePassword(ctx, &signinpb.UpdatePasswordRequest{
			CurrentPassword: password,
			NewPassword:     newPassword,
		})
		must.NoError(t, err, must.Sprint("a caller owing a password change could not make it"))

		after, err := forced.Surfaces.SignIn.GetAuthStatus(ctx, &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.False(t, after.GetStatus().GetRequiresPasswordChange(),
			test.Sprint("a forced password change outlived the change it asked for"))
	})

	// Reporting the flag is half of it, and the half that works only when every
	// client cooperates. The other half is that nothing else answers until the
	// change is made: an ordinary call is refused with the reason a client
	// branches on, the change itself goes through, and the same call then
	// succeeds with nothing else to clear.
	t.Run("a forced password change refuses every other call until it is made", func(t *testing.T) {
		t.Parallel()

		if s.Seams().PasswordChangeGateDisabled {
			conformance.Skip(t, "conformance: this subject says it installs no password change gate (Seams.PasswordChangeGateDisabled), so a forced change is reported and not enforced; skipping")
		}

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		sub, who := signedIn(t, s, anon, refreshTOTPSecret, updatePassword, getAuthStatus)
		ctx := sub.Context(t.Context())
		imposer := directoryCaller(t, s, setUserRequiresPasswordChange)

		_, err := imposer.Surfaces.Identity.SetUserRequiresPasswordChange(imposer.Context(t.Context()),
			&identitypb.SetUserRequiresPasswordChangeRequest{UserId: who.userID, RequiresPasswordChange: new(true)})
		must.NoError(t, err, must.Sprint("imposing a forced password change"))

		// On the token the caller already held: the flag is read per request,
		// so an operator's write reaches a login that began before it.
		_, err = sub.Surfaces.SignIn.RefreshTOTPSecret(ctx, &signinpb.RefreshTOTPSecretRequest{CurrentPassword: password})
		refused(t, s, err, codes.FailedPrecondition, reasonPasswordChangeRequired)

		_, err = sub.Surfaces.SignIn.UpdatePassword(ctx, &signinpb.UpdatePasswordRequest{
			CurrentPassword: password,
			NewPassword:     newPassword,
		})
		must.NoError(t, err, must.Sprint("the gate refused the change it was holding the caller for"))

		after, err := sub.Surfaces.SignIn.GetAuthStatus(ctx, &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.False(t, after.GetStatus().GetRequiresPasswordChange(),
			test.Sprint("a forced password change outlived the change it asked for"))

		refreshed, err := sub.Surfaces.SignIn.RefreshTOTPSecret(ctx, &signinpb.RefreshTOTPSecretRequest{CurrentPassword: newPassword})
		must.NoError(t, err, must.Sprint("the call the gate refused was still refused once the change was made"))
		test.NotEqOp(t, "", refreshed.GetSecret())
	})
}

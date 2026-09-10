package signin_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/authentication/argon2"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestService_UpdatePassword(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		must.NoError(t, e.svc.UpdatePassword(t.Context(), testScope, e.user.ID, &signin.PasswordUpdate{
			CurrentPassword: e.password,
			NewPassword:     "a whole new password",
		}))

		test.EqOp(t, 1, e.hooks.passwords)

		// The old password no longer signs in and the new one does, which is
		// the only assertion that proves the write reached the column the
		// comparison reads.
		_, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope,
			&signin.Credentials{Username: "jane", Password: "a whole new password"})
		must.NoError(t, err)
		test.NotEq(t, "", signedIn.Token)
	})

	T.Run("the current password is checked", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		err := e.svc.UpdatePassword(t.Context(), testScope, e.user.ID, &signin.PasswordUpdate{
			CurrentPassword: "not it",
			NewPassword:     "a whole new password",
		})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.EqOp(t, 0, e.hooks.passwords)
	})

	T.Run("a proven second factor is required", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		secret := e.enrollTOTP(t)

		// Being signed in is not proof enough to change the credential the
		// sign-in was obtained with.
		err := e.svc.UpdatePassword(t.Context(), testScope, e.user.ID, &signin.PasswordUpdate{
			CurrentPassword: e.password,
			NewPassword:     "a whole new password",
		})
		test.ErrorIs(t, err, signin.ErrSecondFactorRequired)

		must.NoError(t, e.svc.UpdatePassword(t.Context(), testScope, e.user.ID, &signin.PasswordUpdate{
			CurrentPassword: e.password,
			NewPassword:     "a whole new password",
			TOTPCode:        code(t, secret),
		}))
	})

	T.Run("a passwordless user is told so", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		passwordless := e.registerPasswordless(t, "passkeyonly")

		// The caller is the subject and is signed in, so the specific answer
		// discloses nothing — and it is the only one that sends them anywhere
		// useful. The anonymous path gives them ErrInvalidCredentials instead.
		err := e.svc.UpdatePassword(t.Context(), testScope, passwordless.ID, &signin.PasswordUpdate{
			CurrentPassword: "anything",
			NewPassword:     "a whole new password",
		})
		test.ErrorIs(t, err, signin.ErrNoPasswordCredential)
	})

	T.Run("the empty inputs", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		test.ErrorIs(t, e.svc.UpdatePassword(t.Context(), testScope, "", nil), signin.ErrEmptyUserID)
		test.ErrorIs(t, e.svc.UpdatePassword(t.Context(), testScope, e.user.ID, nil), signin.ErrNilPasswordUpdate)
		test.ErrorIs(t, e.svc.UpdatePassword(t.Context(), testScope, e.user.ID,
			&signin.PasswordUpdate{CurrentPassword: e.password}), signin.ErrEmptyPassword)
	})

	T.Run("an unknown user", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		err := e.svc.UpdatePassword(t.Context(), testScope, "user_nobody", &signin.PasswordUpdate{
			CurrentPassword: e.password,
			NewPassword:     "a whole new password",
		})
		test.ErrorIs(t, err, identity.ErrUserNotFound)
	})

	T.Run("a forced password change terminates", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		must.NoError(t, e.client.WithTransaction(t.Context(), func(tx dbTx) error {
			return e.store.SetUserRequiresPasswordChange(t.Context(), tx, testScope, e.user.ID, true)
		}))

		status, err := e.svc.GetAuthStatus(t.Context(), testScope, e.user.ID, "")
		must.NoError(t, err)
		test.True(t, status.RequiresPasswordChange)

		must.NoError(t, e.svc.UpdatePassword(t.Context(), testScope, e.user.ID, &signin.PasswordUpdate{
			CurrentPassword: e.password,
			NewPassword:     "a whole new password",
		}))

		// The store clears the flag as part of the write, which is what stops a
		// forced change prompting forever.
		status, err = e.svc.GetAuthStatus(t.Context(), testScope, e.user.ID, "")
		must.NoError(t, err)
		test.False(t, status.RequiresPasswordChange)
	})
}

func TestService_RefreshTOTPSecret(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		enrollment, err := e.svc.RefreshTOTPSecret(t.Context(), testScope, e.user.ID,
			&signin.SecretRefresh{CurrentPassword: e.password})
		must.NoError(t, err)
		must.NotNil(t, enrollment)

		test.NotEq(t, "", enrollment.Secret)
		test.StrContains(t, enrollment.URI, "issuer=Example")
		test.EqOp(t, 1, e.hooks.refreshes)

		// Issued and unproven: the user still signs in without a code, and the
		// status says they hold no second factor.
		status, err := e.svc.GetAuthStatus(t.Context(), testScope, e.user.ID, "")
		must.NoError(t, err)
		test.False(t, status.TwoFactorEnrolled)
	})

	T.Run("without an issuer label", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		// newEnv sets one, so this builds a second service without it.
		svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer)
		must.NoError(t, err)

		enrollment, err := svc.RefreshTOTPSecret(t.Context(), testScope, e.user.ID,
			&signin.SecretRefresh{CurrentPassword: e.password})
		test.Nil(t, enrollment)
		test.ErrorIs(t, err, signin.ErrTOTPIssuerNotConfigured)
	})

	T.Run("the current password is checked", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.RefreshTOTPSecret(t.Context(), testScope, e.user.ID,
			&signin.SecretRefresh{CurrentPassword: "not it"})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
	})

	T.Run("replacing a proven secret needs a code from it", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		secret := e.enrollTOTP(t)

		// Losing a phone is not a way to replace the factor that phone held.
		_, err := e.svc.RefreshTOTPSecret(t.Context(), testScope, e.user.ID,
			&signin.SecretRefresh{CurrentPassword: e.password})
		test.ErrorIs(t, err, signin.ErrSecondFactorRequired)

		enrollment, err := e.svc.RefreshTOTPSecret(t.Context(), testScope, e.user.ID,
			&signin.SecretRefresh{CurrentPassword: e.password, TOTPCode: code(t, secret)})
		must.NoError(t, err)
		test.NotEq(t, secret, enrollment.Secret)
	})

	T.Run("the empty inputs", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.RefreshTOTPSecret(t.Context(), testScope, "", nil)
		test.ErrorIs(t, err, signin.ErrEmptyUserID)

		_, err = e.svc.RefreshTOTPSecret(t.Context(), testScope, e.user.ID, nil)
		test.ErrorIs(t, err, signin.ErrNilSecretRefresh)
	})
}

func TestService_VerifyTOTPSecret(T *testing.T) {
	T.Parallel()

	T.Run("enrollment is the two calls together", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		enrollment, err := e.svc.RefreshTOTPSecret(t.Context(), testScope, e.user.ID,
			&signin.SecretRefresh{CurrentPassword: e.password})
		must.NoError(t, err)

		must.NoError(t, e.svc.VerifyTOTPSecret(t.Context(), testScope, e.user.ID, code(t, enrollment.Secret)))
		test.EqOp(t, 1, e.hooks.verifications)

		// The hook is handed the row the directory's write answered with, so
		// the stamp a consumer records is the one this call made rather than
		// nothing at all — the copy read a statement earlier carried no proof,
		// which is what the call was for.
		must.NotNil(t, e.hooks.verified)
		must.NotNil(t, e.hooks.verified.TwoFactorSecretVerifiedAt)
		test.EqOp(t, e.user.ID, e.hooks.verified.ID)

		// Redacted, like every user this service hands a hook.
		test.EqOp(t, "", e.hooks.verified.HashedPassword)
		test.EqOp(t, "", e.hooks.verified.TwoFactorSecret)

		status, err := e.svc.GetAuthStatus(t.Context(), testScope, e.user.ID, "")
		must.NoError(t, err)
		test.True(t, status.TwoFactorEnrolled)

		// And from here the sign-in path asks for a code.
		_, err = e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		test.ErrorIs(t, err, signin.ErrSecondFactorRequired)
	})

	T.Run("a wrong code", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.RefreshTOTPSecret(t.Context(), testScope, e.user.ID,
			&signin.SecretRefresh{CurrentPassword: e.password})
		must.NoError(t, err)

		test.ErrorIs(t,
			e.svc.VerifyTOTPSecret(t.Context(), testScope, e.user.ID, "000000"),
			signin.ErrInvalidCredentials)
		test.EqOp(t, 0, e.hooks.verifications)
	})

	T.Run("no secret to prove", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		test.ErrorIs(t,
			e.svc.VerifyTOTPSecret(t.Context(), testScope, e.user.ID, "000000"),
			signin.ErrSecondFactorNotEnrolled)
	})

	T.Run("no code", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.RefreshTOTPSecret(t.Context(), testScope, e.user.ID,
			&signin.SecretRefresh{CurrentPassword: e.password})
		must.NoError(t, err)

		test.ErrorIs(t,
			e.svc.VerifyTOTPSecret(t.Context(), testScope, e.user.ID, ""),
			signin.ErrSecondFactorRequired)
	})

	T.Run("no user", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		test.ErrorIs(t, e.svc.VerifyTOTPSecret(t.Context(), testScope, "", "000000"), signin.ErrEmptyUserID)
	})
}

func TestService_GetAuthStatus(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		status, err := e.svc.GetAuthStatus(t.Context(), testScope, e.user.ID, "")
		must.NoError(t, err)
		must.NotNil(t, status)

		test.EqOp(t, e.user.ID, status.User.ID)
		test.EqOp(t, e.accountID, status.ActiveAccountID)
		test.Eq(t, []string{e.accountID}, status.AccountIDs)
		test.True(t, status.HasPassword)
		test.False(t, status.TwoFactorEnrolled)
		test.False(t, status.RequiresPasswordChange)
		test.False(t, status.EmailAddressVerified)

		// The user is redacted, which is why the three booleans are fields
		// rather than something a caller derives.
		test.EqOp(t, "", status.User.HashedPassword)
		test.False(t, status.User.HasPassword())
	})

	T.Run("a passwordless user", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		passwordless := e.registerPasswordless(t, "passkeyonly")

		status, err := e.svc.GetAuthStatus(t.Context(), testScope, passwordless.ID, "")
		must.NoError(t, err)
		test.False(t, status.HasPassword)
	})

	T.Run("an account the caller does not belong to", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.GetAuthStatus(t.Context(), testScope, e.user.ID, "acct_somebody_else")
		test.ErrorIs(t, err, identity.ErrMembershipNotFound)
	})

	T.Run("no user", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.GetAuthStatus(t.Context(), testScope, "", "")
		test.ErrorIs(t, err, signin.ErrEmptyUserID)
	})
}

func TestService_GetSelf(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		user, err := e.svc.GetSelf(t.Context(), testScope, e.user.ID)
		must.NoError(t, err)
		must.NotNil(t, user)

		test.EqOp(t, e.user.ID, user.ID)
		test.EqOp(t, "jane", user.Username)

		// Redacted: this is a response body, and a response body is not a place
		// for a password hash.
		test.EqOp(t, "", user.HashedPassword)
		test.EqOp(t, "", user.TwoFactorSecret)
	})

	T.Run("no user", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.GetSelf(t.Context(), testScope, "")
		test.ErrorIs(t, err, signin.ErrEmptyUserID)
	})

	T.Run("an unknown user", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.GetSelf(t.Context(), testScope, "user_nobody")
		test.ErrorIs(t, err, identity.ErrUserNotFound)
	})
}

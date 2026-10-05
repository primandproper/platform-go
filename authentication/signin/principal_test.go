package signin_test

import (
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/identity"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestService_IssueForPrincipal(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		signedIn, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "")
		must.NoError(t, err)

		test.EqOp(t, "token-for-"+e.user.ID, signedIn.Token)
		test.False(t, signedIn.Administrative)
		test.EqOp(t, e.accountID, signedIn.Principal.ActiveAccountID)
		test.NotEqOp(t, "", signedIn.FamilyID)

		// The same claims and lifetime a password sign-in gets, which is the
		// whole reason a consumer calls this rather than minting their own.
		test.EqOp(t, signin.DefaultTokenTTL, e.issuer.expiry)
		test.Eq(t, map[string]any{
			signin.ClaimAccountID:      e.accountID,
			signin.ClaimScope:          testScope.String(),
			signin.ClaimFamilyID:       signedIn.FamilyID,
			signin.ClaimAdministrative: false,
		}, e.issuer.claims)

		test.Eq(t, []string{"authenticate", "issue"}, e.hooks.calls)
		must.SliceLen(t, 1, e.hooks.authentications)
		test.EqOp(t, e.user.ID, e.hooks.authentications[0].Principal.User.ID)
		test.EqOp(t, signin.CredentialKindPrincipal, e.hooks.authentications[0].CredentialKind)
		must.SliceLen(t, 1, e.hooks.signIns)
		test.EqOp(t, signedIn.FamilyID, e.hooks.signIns[0].FamilyID)
		test.SliceEmpty(t, e.hooks.failures)
	})

	T.Run("a user who holds no password", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		user := e.registerPasswordless(t, "ada")

		// The case the door is for: somebody whose only credential is one this
		// package does not verify.
		signedIn, err := e.svc.IssueForPrincipal(t.Context(), testScope, user.ID, "")
		must.NoError(t, err)
		test.EqOp(t, user.ID, signedIn.Principal.User.ID)
	})

	T.Run("a credential that was two factors is asked for no third", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithSecondFactorPolicy(signin.SecondFactorRequired))
		e.enrollTOTP(t)

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "", signin.MultiFactor())
		must.NoError(t, err)
	})

	// A passkey the authenticator did not verify the person for: a key tap is
	// possession alone, and a user with a second factor is asked for it.
	T.Run("a single-factor credential is asked for the second factor", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		e.enrollTOTP(t)

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "", signin.WithCredentialKind("passkey"))
		test.ErrorIs(t, err, signin.ErrSecondFactorRequired)

		test.SliceEmpty(t, e.hooks.signIns)
		must.SliceLen(t, 1, e.hooks.failures)
		test.EqOp(t, e.user.ID, e.hooks.failures[0].UserID)
		test.ErrorIs(t, e.hooks.failures[0].Reason, signin.ErrSecondFactorRequired)
	})

	T.Run("a single-factor credential signs in beside its second factor", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		secret := e.enrollTOTP(t)

		signedIn, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "",
			signin.WithCredentialKind("passkey"), signin.WithTOTPCode(code(t, secret)))
		must.NoError(t, err)
		test.EqOp(t, e.user.ID, signedIn.Principal.User.ID)

		must.SliceLen(t, 1, e.hooks.authentications)
		test.EqOp(t, signin.CredentialKind("passkey"), e.hooks.authentications[0].CredentialKind)
		test.SliceEmpty(t, e.hooks.failures)
	})

	T.Run("a wrong second-factor code is invalid credentials", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		e.enrollTOTP(t)

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "", signin.WithTOTPCode("000000"))
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		test.SliceEmpty(t, e.hooks.signIns)
		must.SliceLen(t, 1, e.hooks.failures)
	})

	T.Run("a single-factor credential answers to the service's policy", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithSecondFactorPolicy(signin.SecondFactorRequired))

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrSecondFactorNotEnrolled)
		test.SliceEmpty(t, e.hooks.signIns)
	})

	T.Run("mints a refresh token an exchange accepts", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "")
		must.NoError(t, err)
		must.NotEqOp(t, "", signedIn.RefreshToken)

		exchanged, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
		must.NoError(t, err)
		test.EqOp(t, signedIn.FamilyID, exchanged.FamilyID)
		test.False(t, exchanged.Administrative)
	})

	T.Run("a hook failure leaves no refresh token behind", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)
		e.hooks.issueErr = errRecordingFailed

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "")
		test.ErrorIs(t, err, errRecordingFailed)

		var rows int
		must.NoError(t, e.client.Writer().QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM "+refreshTable(t, e)).Scan(&rows))

		test.EqOp(t, 0, rows)
	})

	T.Run("an empty user ID is refused before anything is read", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, "", "")
		test.ErrorIs(t, err, signin.ErrEmptyUserID)
		test.SliceEmpty(t, e.hooks.calls)
		test.SliceEmpty(t, e.hooks.failures)
	})

	T.Run("an invalid scope is refused", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.IssueForPrincipal(t.Context(), tenancy.Scope{}, e.user.ID, "")
		test.Error(t, err)
		test.SliceEmpty(t, e.hooks.signIns)
	})

	T.Run("a user the directory does not hold is its answer, not an attempt", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, "nobody", "")
		test.ErrorIs(t, err, identity.ErrUserNotFound)
		test.SliceEmpty(t, e.hooks.failures)
		test.SliceEmpty(t, e.hooks.signIns)
	})

	T.Run("a proven credential does not sign in a suspended user", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		e.setStatus(t, identity.StatusBanned, "you were rude")

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrUserBanned)
		test.StrContains(t, err.Error(), "you were rude")

		test.SliceEmpty(t, e.hooks.signIns)
		must.SliceLen(t, 1, e.hooks.failures)
		test.EqOp(t, e.user.ID, e.hooks.failures[0].UserID)
		test.EqOp(t, "", e.hooks.failures[0].Handle)
		test.False(t, e.hooks.failures[0].Administrative)
		test.ErrorIs(t, e.hooks.failures[0].Reason, signin.ErrUserBanned)
	})

	T.Run("the status is checked even where the directory would not", func(t *testing.T) {
		t.Parallel()

		e := newPermissiveRefreshEnv(t)
		e.setStatus(t, identity.StatusTerminated, "")

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrUserTerminated)
		test.SliceEmpty(t, e.hooks.signIns)
	})
}

func TestService_IssueForPrincipal_Administrative(T *testing.T) {
	T.Parallel()

	T.Run("no administrative roles named means no administrative door", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "", signin.Administrative())
		test.ErrorIs(t, err, signin.ErrAdminLoginDisabled)
		test.SliceEmpty(t, e.hooks.signIns)
	})

	T.Run("a user without the role is refused, and the refusal recorded", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithAdminServiceRoles("service_admin"))

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "", signin.Administrative())
		test.ErrorIs(t, err, signin.ErrNotAnAdministrator)

		test.SliceEmpty(t, e.hooks.signIns)
		must.SliceLen(t, 1, e.hooks.failures)
		test.True(t, e.hooks.failures[0].Administrative)
		test.EqOp(t, e.user.ID, e.hooks.failures[0].UserID)
	})

	// A passkey the authenticator did not verify the person for, at the door
	// where possession alone is not an answer. No code rescues it: the door
	// takes none.
	T.Run("a single-factor credential is refused", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithAdminServiceRoles("service_admin"))
		e.setServiceRoles(t, "service_admin")
		secret := e.enrollTOTP(t)

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "", signin.Administrative())
		test.ErrorIs(t, err, signin.ErrMultiFactorRequired)

		_, err = e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "",
			signin.Administrative(), signin.WithTOTPCode(code(t, secret)))
		test.ErrorIs(t, err, signin.ErrMultiFactorRequired)

		test.SliceEmpty(t, e.hooks.signIns)
		must.SliceLen(t, 2, e.hooks.failures)
		test.True(t, e.hooks.failures[0].Administrative)
		test.EqOp(t, e.user.ID, e.hooks.failures[0].UserID)
		test.ErrorIs(t, e.hooks.failures[0].Reason, signin.ErrMultiFactorRequired)
	})

	// The role is checked first, so the refusal for a weak credential is not
	// how a non-administrator learns the door exists.
	T.Run("the role is checked before the credential's strength", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithAdminServiceRoles("service_admin"))

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "", signin.Administrative())
		test.ErrorIs(t, err, signin.ErrNotAnAdministrator)
	})

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t, signin.WithAdminServiceRoles("service_admin"))
		e.setServiceRoles(t, "service_admin")

		// No TOTP secret is enrolled, and none is asked for: the credential was
		// two factors on its own.
		signedIn, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "", signin.Administrative(), signin.MultiFactor())
		must.NoError(t, err)

		test.True(t, signedIn.Administrative)
		test.EqOp(t, signin.DefaultAdminTokenTTL, e.issuer.expiry)
		test.EqOp[any](t, true, e.issuer.claims[signin.ClaimAdministrative])

		must.SliceLen(t, 1, e.hooks.authentications)
		test.True(t, e.hooks.authentications[0].Administrative)
		test.EqOp(t, signin.CredentialKindPrincipal, e.hooks.authentications[0].CredentialKind)
		must.SliceLen(t, 1, e.hooks.signIns)
		test.True(t, e.hooks.signIns[0].Administrative)

		// The door is kept on the refresh token, so the session stays
		// administrative across an exchange.
		exchanged, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
		must.NoError(t, err)
		test.True(t, exchanged.Administrative)
		test.EqOp(t, signin.DefaultAdminTokenTTL, e.issuer.expiry)
	})
}

func TestService_IssueForPrincipal_WithCredentialKind(T *testing.T) {
	T.Parallel()

	T.Run("stamps the kind its caller names", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		passkey := signin.CredentialKind("passkey")

		signedIn, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "", signin.WithCredentialKind(passkey))
		must.NoError(t, err)
		test.EqOp(t, e.user.ID, signedIn.Principal.User.ID)
		test.False(t, signedIn.Administrative)

		test.Eq(t, []string{"authenticate", "issue"}, e.hooks.calls)
		must.SliceLen(t, 1, e.hooks.authentications)
		test.EqOp(t, passkey, e.hooks.authentications[0].CredentialKind)
		test.False(t, e.hooks.authentications[0].Administrative)
	})

	T.Run("the administrative door stamps it too", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithAdminServiceRoles("service_admin"))
		e.setServiceRoles(t, "service_admin")
		passkey := signin.CredentialKind("passkey")

		signedIn, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "",
			signin.WithCredentialKind(passkey), signin.Administrative(), signin.MultiFactor())
		must.NoError(t, err)
		test.True(t, signedIn.Administrative)

		must.SliceLen(t, 1, e.hooks.authentications)
		test.EqOp(t, passkey, e.hooks.authentications[0].CredentialKind)
		test.True(t, e.hooks.authentications[0].Administrative)
	})

	// An empty kind is a name that got lost on its way in, not a request for
	// CredentialKindPrincipal — that caller leaves the option off.
	T.Run("refuses a caller who named no kind", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "", signin.WithCredentialKind(""))
		test.ErrorIs(t, err, signin.ErrEmptyCredentialKind)

		_, err = e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "", signin.WithCredentialKind(""), signin.Administrative())
		test.ErrorIs(t, err, signin.ErrEmptyCredentialKind)

		test.SliceEmpty(t, e.hooks.authentications)
		test.SliceEmpty(t, e.hooks.failures)
	})
}

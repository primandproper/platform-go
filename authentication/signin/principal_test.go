package signin_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"

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

	T.Run("the second-factor policy is the credential's, not this door's", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithSecondFactorPolicy(signin.SecondFactorRequired))
		e.enrollTOTP(t)

		_, err := e.svc.IssueForPrincipal(t.Context(), testScope, e.user.ID, "")
		must.NoError(t, err)
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

func TestService_AdminIssueForPrincipal(T *testing.T) {
	T.Parallel()

	T.Run("no administrative roles named means no administrative door", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.AdminIssueForPrincipal(t.Context(), testScope, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrAdminLoginDisabled)
		test.SliceEmpty(t, e.hooks.signIns)
	})

	T.Run("a user without the role is refused, and the refusal recorded", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithAdminServiceRoles("service_admin"))

		_, err := e.svc.AdminIssueForPrincipal(t.Context(), testScope, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrNotAnAdministrator)

		test.SliceEmpty(t, e.hooks.signIns)
		must.SliceLen(t, 1, e.hooks.failures)
		test.True(t, e.hooks.failures[0].Administrative)
		test.EqOp(t, e.user.ID, e.hooks.failures[0].UserID)
	})

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t, signin.WithAdminServiceRoles("service_admin"))
		e.setServiceRoles(t, "service_admin")

		// No second factor is enrolled, and none is asked for: whether the
		// credential in front of this door was strong enough is the consumer's.
		signedIn, err := e.svc.AdminIssueForPrincipal(t.Context(), testScope, e.user.ID, "")
		must.NoError(t, err)

		test.True(t, signedIn.Administrative)
		test.EqOp(t, signin.DefaultAdminTokenTTL, e.issuer.expiry)
		test.EqOp[any](t, true, e.issuer.claims[signin.ClaimAdministrative])

		must.SliceLen(t, 1, e.hooks.authentications)
		test.True(t, e.hooks.authentications[0].Administrative)
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

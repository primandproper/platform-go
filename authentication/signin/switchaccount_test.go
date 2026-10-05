package signin_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/signin"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestService_SwitchAccount(T *testing.T) {
	T.Parallel()

	// The switch itself: the same login, a new account, and every exchange
	// after it carrying the new one.
	T.Run("moves the login to the named account in the same family", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)
		second := e.addAccount(t, "Second")

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)
		must.EqOp(t, e.accountID, first.Principal.ActiveAccountID)

		switched, err := e.svc.SwitchAccount(t.Context(), testScope, first.RefreshToken, second)
		must.NoError(t, err)
		must.NotNil(t, switched)

		test.EqOp(t, first.FamilyID, switched.FamilyID)
		test.NotEqOp(t, first.RefreshToken, switched.RefreshToken)
		test.EqOp(t, second, switched.Principal.ActiveAccountID)
		test.EqOp[any](t, second, e.issuer.claims[signin.ClaimAccountID])
		test.EqOp[any](t, first.FamilyID, e.issuer.claims[signin.ClaimFamilyID])

		// The account is the new row's from here on, which is what makes the
		// switch stick rather than last one access token.
		rotated, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, switched.RefreshToken)
		must.NoError(t, err)
		test.EqOp(t, second, rotated.Principal.ActiveAccountID)
		test.EqOp(t, first.FamilyID, rotated.FamilyID)
	})

	// The dedicated hook, with both accounts on it, and the issuance hook
	// after it — the switch is the event, the token is what came of it.
	T.Run("reports the switch, then the token it minted", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)
		second := e.addAccount(t, "Second")

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		e.hooks.calls = nil

		switched, err := e.svc.SwitchAccount(t.Context(), testScope, first.RefreshToken, second)
		must.NoError(t, err)

		test.Eq(t, []string{"switch", "issue"}, e.hooks.calls)
		must.SliceLen(t, 1, e.hooks.switches)

		change := e.hooks.switches[0]
		test.EqOp(t, e.user.ID, change.SubjectID)
		test.EqOp(t, first.FamilyID, change.FamilyID)
		test.EqOp(t, e.accountID, change.FromAccountID)
		test.EqOp(t, second, change.ToAccountID)
		test.False(t, change.Administrative)

		test.EqOp(t, switched.TokenID, e.hooks.signIns[len(e.hooks.signIns)-1].TokenID)
	})

	// The door is the row's, and a switch carries it as an exchange does. An
	// administrative session that came out of a switch as an ordinary one would
	// be the hardening undone by moving tenants, in the direction that
	// lengthens it.
	T.Run("an administrative session stays administrative and stays short", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t, signin.WithAdminServiceRoles("service_admin"))
		e.setServiceRoles(t, "service_admin")
		secret := e.enrollTOTP(t)
		second := e.addAccount(t, "Second")

		credentials := e.credentials()
		credentials.TOTPCode = code(t, secret)

		first, err := e.svc.AdminLoginForToken(t.Context(), testScope, credentials)
		must.NoError(t, err)
		must.True(t, first.Administrative)

		e.hooks.switches = nil

		switched, err := e.svc.SwitchAccount(t.Context(), testScope, first.RefreshToken, second)
		must.NoError(t, err)

		test.True(t, switched.Administrative)
		test.EqOp(t, second, switched.Principal.ActiveAccountID)
		test.EqOp(t, signin.DefaultAdminTokenTTL, e.issuer.expiry)
		test.EqOp[any](t, true, e.issuer.claims[signin.ClaimAdministrative])

		gap := switched.RefreshTokenExpiresAt.Sub(switched.ExpiresAt)
		want := signin.DefaultAdminRefreshTokenTTL - signin.DefaultAdminTokenTTL

		test.True(t, gap > want-time.Second && gap < want+time.Second,
			test.Sprintf("refresh expiry is %s past the token's, wanted %s", gap, want))

		must.SliceLen(t, 1, e.hooks.switches)
		test.True(t, e.hooks.switches[0].Administrative)
	})

	// A switch to somewhere the subject does not belong is a bad exchange like
	// any other, and changes nothing: the token is unspent and the login is
	// where it was.
	T.Run("refuses an account the subject is not a member of and leaves the login intact", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)
		foreign := e.addForeignAccount(t, "Somebody Else's")

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		for what, accountID := range map[string]string{
			"an account they are not in": foreign,
			"an account that never was":  "acct-never-created",
		} {
			switched, switchErr := e.svc.SwitchAccount(t.Context(), testScope, first.RefreshToken, accountID)
			test.Nil(t, switched, test.Sprintf("%s: a refused switch answered with a token", what))
			test.ErrorIs(t, switchErr, signin.ErrInvalidCredentials, test.Sprintf("%s: %v", what, switchErr))
			test.False(t, platformerrors.Is(switchErr, signin.ErrRefreshTokenReused), test.Sprint(what))
		}

		// One answer with a token nobody minted, so the refusal says nothing
		// about the account named.
		_, neverMinted := e.svc.SwitchAccount(t.Context(), testScope, "never-minted", e.accountID)
		_, foreignErr := e.svc.SwitchAccount(t.Context(), testScope, first.RefreshToken, foreign)
		must.ErrorIs(t, neverMinted, signin.ErrInvalidCredentials)
		must.Error(t, foreignErr)
		test.EqOp(t, neverMinted.Error(), foreignErr.Error())

		test.SliceEmpty(t, e.hooks.switches)

		// The token was not spent, and it still mints for the account the
		// login began in.
		rotated, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		must.NoError(t, err, must.Sprint("a refused switch spent the token"))
		test.EqOp(t, e.accountID, rotated.Principal.ActiveAccountID)
		test.EqOp(t, first.FamilyID, rotated.FamilyID)
	})

	// One login before, one login after.
	T.Run("the switched login is still one entry in the list", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)
		second := e.addAccount(t, "Second")

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.SwitchAccount(t.Context(), testScope, first.RefreshToken, second)
		must.NoError(t, err)

		signIns, err := e.svc.ListSignIns(t.Context(), testScope, e.user.ID, 0)
		must.NoError(t, err)
		must.SliceLen(t, 1, signIns)
		test.EqOp(t, first.FamilyID, signIns[0].FamilyID)
		test.EqOp(t, second, signIns[0].ActiveAccountID)
	})

	// Reuse detection runs across a switch as across a refresh: the token the
	// switch spent, presented again, ends the login it moved.
	T.Run("presenting the token a switch spent ends the login", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)
		second := e.addAccount(t, "Second")

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		switched, err := e.svc.SwitchAccount(t.Context(), testScope, first.RefreshToken, second)
		must.NoError(t, err)

		_, err = e.svc.SwitchAccount(t.Context(), testScope, first.RefreshToken, second)
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, switched.RefreshToken)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		must.SliceLen(t, 1, e.hooks.revocations)
		test.EqOp(t, signin.RevocationReuse, e.hooks.revocations[0].Reason)
		test.Eq(t, []string{first.FamilyID}, e.hooks.revocations[0].FamilyIDs)
	})

	// A hook that refuses rolls the switch back, as every hook does.
	T.Run("a refusing hook leaves the token unspent", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)
		second := e.addAccount(t, "Second")

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		e.hooks.switchErr = platformerrors.New("audit store unavailable")

		_, err = e.svc.SwitchAccount(t.Context(), testScope, first.RefreshToken, second)
		test.ErrorIs(t, err, e.hooks.switchErr)

		e.hooks.switchErr = nil

		rotated, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		must.NoError(t, err, must.Sprint("a rolled-back switch spent the token"))
		test.EqOp(t, e.accountID, rotated.Principal.ActiveAccountID)
	})

	// The requests that name nothing, refused before anything is read.
	T.Run("refuses an empty request", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.SwitchAccount(t.Context(), testScope, first.RefreshToken, "")
		test.ErrorIs(t, err, signin.ErrEmptyAccountID)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)

		_, err = e.svc.SwitchAccount(t.Context(), testScope, "", e.accountID)
		test.ErrorIs(t, err, signin.ErrEmptyRefreshToken)

		// None of those touched the token.
		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		test.NoError(t, err)
	})

	T.Run("is not configured without a refresh token store", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.SwitchAccount(t.Context(), testScope, "anything", e.accountID)
		test.ErrorIs(t, err, signin.ErrRefreshTokensNotConfigured)
	})
}

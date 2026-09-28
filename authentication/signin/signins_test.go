package signin_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// familiesOf is the order a listing named its logins in.
func familiesOf(signIns []*signin.ActiveSignIn) []string {
	ids := make([]string, 0, len(signIns))
	for _, s := range signIns {
		ids = append(ids, s.FamilyID)
	}

	return ids
}

func TestService_ListSignIns(T *testing.T) {
	T.Parallel()

	T.Run("lists each login once, however often it has refreshed", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		phone, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		laptop, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		refreshed, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, phone.RefreshToken)
		must.NoError(t, err)

		signIns, err := e.svc.ListSignIns(t.Context(), testScope, e.user.ID, 0)
		must.NoError(t, err)
		must.SliceLen(t, 2, signIns)

		// Which comes first is the store's ordering, pinned against a clock the
		// store's own tests control; SQLite stores whole seconds, so two logins
		// this close together can tie here.
		test.SliceContainsAll(t, []string{phone.FamilyID, laptop.FamilyID}, familiesOf(signIns))

		listed := signIns[0]
		if listed.FamilyID != phone.FamilyID {
			listed = signIns[1]
		}

		// The exchange carried the login's start forward, so the phone still
		// began when it signed in, and it lapses when its newest token does —
		// compared to the second, which is what SQLite stores.
		test.True(t, !listed.SignedInAt.After(listed.LastRefreshedAt))
		test.True(t, refreshed.RefreshTokenExpiresAt.Truncate(time.Second).Equal(listed.ExpiresAt.Truncate(time.Second)),
			test.Sprintf("exchange said %v, listing said %v", refreshed.RefreshTokenExpiresAt, listed.ExpiresAt))
		test.EqOp(t, e.accountID, listed.ActiveAccountID)
	})

	// The start is what survives a refresh; it is not the last token's mint.
	T.Run("a refresh does not move when the login began", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		before, err := e.svc.ListSignIns(t.Context(), testScope, e.user.ID, 0)
		must.NoError(t, err)
		must.SliceLen(t, 1, before)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
		must.NoError(t, err)

		after, err := e.svc.ListSignIns(t.Context(), testScope, e.user.ID, 0)
		must.NoError(t, err)
		must.SliceLen(t, 1, after)

		test.True(t, before[0].SignedInAt.Equal(after[0].SignedInAt),
			test.Sprintf("began %v, then %v", before[0].SignedInAt, after[0].SignedInAt))
	})

	T.Run("leaves out a login that signed out", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		gone, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		kept, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		must.NoError(t, e.svc.SignOut(t.Context(), testScope, gone.RefreshToken))

		signIns, err := e.svc.ListSignIns(t.Context(), testScope, e.user.ID, 0)
		must.NoError(t, err)
		test.Eq(t, []string{kept.FamilyID}, familiesOf(signIns))
	})

	T.Run("honors a limit", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		for range 3 {
			_, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
			must.NoError(t, err)
		}

		signIns, err := e.svc.ListSignIns(t.Context(), testScope, e.user.ID, 2)
		must.NoError(t, err)
		test.SliceLen(t, 2, signIns)

		// A limit past the ceiling is the ceiling rather than a refusal.
		signIns, err = e.svc.ListSignIns(t.Context(), testScope, e.user.ID, signin.MaxSignInListLimit+1)
		must.NoError(t, err)
		test.SliceLen(t, 3, signIns)
	})

	T.Run("answers only in the scope it was asked about", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		_, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		signIns, err := e.svc.ListSignIns(t.Context(), tenancy.Of("dir_2"), e.user.ID, 0)
		must.NoError(t, err)
		test.SliceEmpty(t, signIns)
	})

	T.Run("refuses a listing about nobody", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		_, err := e.svc.ListSignIns(t.Context(), testScope, "", 0)
		test.ErrorIs(t, err, signin.ErrEmptyUserID)
	})

	T.Run("refuses on a service that stores no refresh tokens", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.ListSignIns(t.Context(), testScope, e.user.ID, 0)
		test.ErrorIs(t, err, signin.ErrRefreshTokensNotConfigured)
	})
}

func TestService_EndSignIn(T *testing.T) {
	T.Parallel()

	T.Run("ends the named login and leaves the rest", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		ended, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		kept, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		revoked, err := e.svc.EndSignIn(t.Context(), testScope, e.user.ID, ended.FamilyID)
		must.NoError(t, err)
		test.EqOp(t, 1, revoked)

		// The ended login's refresh token no longer exchanges, and the other
		// one's still does.
		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, ended.RefreshToken)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, kept.RefreshToken)
		test.NoError(t, err)

		signIns, err := e.svc.ListSignIns(t.Context(), testScope, e.user.ID, 0)
		must.NoError(t, err)
		test.Eq(t, []string{kept.FamilyID}, familiesOf(signIns))
	})

	// A family identifier is not a secret, so the subject is what stands
	// between a caller and somebody else's login — and the answer is the one a
	// family that never existed gets.
	T.Run("cannot end somebody else's login", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		theirs, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		revoked, err := e.svc.EndSignIn(t.Context(), testScope, "somebody_else", theirs.FamilyID)
		must.NoError(t, err)
		test.EqOp(t, 0, revoked)

		unknown, err := e.svc.EndSignIn(t.Context(), testScope, e.user.ID, "no_such_family")
		must.NoError(t, err)
		test.EqOp(t, 0, unknown)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, theirs.RefreshToken)
		test.NoError(t, err)
	})

	T.Run("refuses a request that names nothing", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		_, err := e.svc.EndSignIn(t.Context(), testScope, "", "family")
		test.ErrorIs(t, err, signin.ErrEmptyUserID)

		_, err = e.svc.EndSignIn(t.Context(), testScope, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrEmptyFamilyID)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})

	T.Run("refuses on a service that stores no refresh tokens", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.EndSignIn(t.Context(), testScope, e.user.ID, "family")
		test.ErrorIs(t, err, signin.ErrRefreshTokensNotConfigured)
	})
}

func TestService_EndOtherSignIns(T *testing.T) {
	T.Parallel()

	T.Run("ends every other login and reports which", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		kept, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		phone, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		laptop, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		ended, err := e.svc.EndOtherSignIns(t.Context(), testScope, e.user.ID, kept.FamilyID)
		must.NoError(t, err)
		test.SliceContainsAll(t, []string{phone.FamilyID, laptop.FamilyID}, ended)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, phone.RefreshToken)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, laptop.RefreshToken)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, kept.RefreshToken)
		test.NoError(t, err)

		signIns, err := e.svc.ListSignIns(t.Context(), testScope, e.user.ID, 0)
		must.NoError(t, err)
		test.Eq(t, []string{kept.FamilyID}, familiesOf(signIns))
	})

	T.Run("reports nothing when the asking login is the only one", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		kept, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		ended, err := e.svc.EndOtherSignIns(t.Context(), testScope, e.user.ID, kept.FamilyID)
		must.NoError(t, err)
		test.SliceEmpty(t, ended)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, kept.RefreshToken)
		test.NoError(t, err)
	})

	// The refusal the whole door turns on: a caller that cannot say which
	// login it is gets nothing ended, rather than everything.
	T.Run("refuses to keep a login it was not told", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.EndOtherSignIns(t.Context(), testScope, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrSignInNotIdentified)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
		test.NoError(t, err)
	})

	T.Run("refuses a request that names nobody", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		_, err := e.svc.EndOtherSignIns(t.Context(), testScope, "", "family")
		test.ErrorIs(t, err, signin.ErrEmptyUserID)
	})

	T.Run("refuses on a service that stores no refresh tokens", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.EndOtherSignIns(t.Context(), testScope, e.user.ID, "family")
		test.ErrorIs(t, err, signin.ErrRefreshTokensNotConfigured)
	})
}

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

func TestService_CheckSignIn(T *testing.T) {
	T.Parallel()

	T.Run("a login that is still going passes", func(t *testing.T) {
		t.Parallel()

		for name, opts := range map[string][]signin.ServiceOption{
			"by default":                 nil,
			"refusing superseded tokens": {signin.WithSupersededTokenRefusal()},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				e := newRefreshEnv(t, opts...)

				signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
				must.NoError(t, err)

				test.NoError(t, e.svc.CheckSignIn(t.Context(), testScope, signedIn.FamilyID, signedIn.TokenID))
			})
		}
	})

	// Every way a login ends, each read back by the next request rather than
	// by the access token's expiry.
	T.Run("a login that has ended is refused at once", func(t *testing.T) {
		t.Parallel()

		ends := map[string]func(t *testing.T, e *env, signedIn *signin.SignIn){
			"ended by name": func(t *testing.T, e *env, signedIn *signin.SignIn) {
				t.Helper()

				_, err := e.svc.EndSignIn(t.Context(), testScope, e.user.ID, signedIn.FamilyID)
				must.NoError(t, err)
			},
			"signed out": func(t *testing.T, e *env, signedIn *signin.SignIn) {
				t.Helper()

				must.NoError(t, e.svc.SignOut(t.Context(), testScope, signedIn.RefreshToken))
			},
			"signed out everywhere": func(t *testing.T, e *env, _ *signin.SignIn) {
				t.Helper()

				_, err := e.svc.RevokeRefreshTokensForSubject(t.Context(), testScope, e.user.ID)
				must.NoError(t, err)
			},
			"ended by a replayed refresh token": func(t *testing.T, e *env, signedIn *signin.SignIn) {
				t.Helper()

				_, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
				must.NoError(t, err)

				_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
				must.ErrorIs(t, err, signin.ErrRefreshTokenReused)
			},
		}

		for name, end := range ends {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				e := newRefreshEnv(t)

				signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
				must.NoError(t, err)

				// The control: the same check passes before the login ends.
				must.NoError(t, e.svc.CheckSignIn(t.Context(), testScope, signedIn.FamilyID, signedIn.TokenID))

				end(t, e, signedIn)

				err = e.svc.CheckSignIn(t.Context(), testScope, signedIn.FamilyID, signedIn.TokenID)
				test.ErrorIs(t, err, signin.ErrSignInEnded)
				test.ErrorIs(t, err, signin.ErrInvalidCredentials)
			})
		}
	})

	T.Run("a login in another scope has ended from here", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		err = e.svc.CheckSignIn(t.Context(), tenancy.Of("somebody_else"), signedIn.FamilyID, signedIn.TokenID)
		test.ErrorIs(t, err, signin.ErrSignInEnded)
	})

	// The rotated case. Opt-in: by default a refresh withdraws nothing, which
	// is the family model's premise.
	T.Run("an access token the login has since replaced", func(t *testing.T) {
		t.Parallel()

		t.Run("passes by default", func(t *testing.T) {
			t.Parallel()

			e := newRefreshEnv(t)

			first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
			must.NoError(t, err)

			_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
			must.NoError(t, err)

			test.NoError(t, e.svc.CheckSignIn(t.Context(), testScope, first.FamilyID, first.TokenID))
		})

		t.Run("is refused as superseded when the service refuses them", func(t *testing.T) {
			t.Parallel()

			e := newRefreshEnv(t, signin.WithSupersededTokenRefusal())

			first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
			must.NoError(t, err)

			second, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
			must.NoError(t, err)
			must.EqOp(t, first.FamilyID, second.FamilyID)
			must.NotEqOp(t, first.TokenID, second.TokenID, must.Sprint("the harness issued one access token twice"))

			err = e.svc.CheckSignIn(t.Context(), testScope, first.FamilyID, first.TokenID)
			test.ErrorIs(t, err, signin.ErrSignInSuperseded)
			test.ErrorIs(t, err, signin.ErrInvalidCredentials)
			test.False(t, platformerrors.Is(err, signin.ErrSignInEnded))

			// The control: the token the exchange handed back is the current
			// one.
			test.NoError(t, e.svc.CheckSignIn(t.Context(), testScope, second.FamilyID, second.TokenID))
		})

		// An ended login is ended, whichever of its access tokens asks.
		t.Run("is refused as ended once the login has ended", func(t *testing.T) {
			t.Parallel()

			e := newRefreshEnv(t, signin.WithSupersededTokenRefusal())

			first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
			must.NoError(t, err)

			second, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
			must.NoError(t, err)

			_, err = e.svc.EndSignIn(t.Context(), testScope, e.user.ID, first.FamilyID)
			must.NoError(t, err)

			for _, token := range []*signin.SignIn{first, second} {
				err = e.svc.CheckSignIn(t.Context(), testScope, token.FamilyID, token.TokenID)
				test.ErrorIs(t, err, signin.ErrSignInEnded)
			}
		})
	})

	T.Run("refuses a check that names nothing", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t, signin.WithSupersededTokenRefusal())

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		err = e.svc.CheckSignIn(t.Context(), testScope, "", signedIn.TokenID)
		test.ErrorIs(t, err, signin.ErrEmptyFamilyID)

		err = e.svc.CheckSignIn(t.Context(), testScope, signedIn.FamilyID, "")
		test.ErrorIs(t, err, signin.ErrEmptyTokenID)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})

	// A service that does not refuse superseded tokens compares nothing, so it
	// has nothing to refuse an empty one for.
	T.Run("reads no access token by default", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		test.NoError(t, e.svc.CheckSignIn(t.Context(), testScope, signedIn.FamilyID, ""))
	})

	T.Run("refuses without a refresh token store", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		err := e.svc.CheckSignIn(t.Context(), testScope, "family", "jti")
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

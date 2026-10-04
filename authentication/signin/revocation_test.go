package signin_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// requireRevocation asserts the hooks heard exactly one revocation, and hands it
// back.
func requireRevocation(t *testing.T, e *env) *signin.Revocation {
	t.Helper()

	must.SliceLen(t, 1, e.hooks.revocations)

	return e.hooks.revocations[0]
}

// liveSignIns is how many logins the suite's user can still see.
func liveSignIns(t *testing.T, e *env) int {
	t.Helper()

	signIns, err := e.svc.ListSignIns(t.Context(), testScope, e.user.ID, 0)
	must.NoError(t, err)

	return len(signIns)
}

func TestService_AfterRevokeSignIns(T *testing.T) {
	T.Parallel()

	T.Run("a sign-out reports the login it ended, as the person's own act", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		must.NoError(t, e.svc.SignOut(t.Context(), testScope, signedIn.RefreshToken))

		revocation := requireRevocation(t, e)
		test.EqOp(t, signin.RevocationSignOut, revocation.Reason)
		test.EqOp(t, e.user.ID, revocation.SubjectID)
		test.EqOp(t, e.user.ID, revocation.ActorID)
		test.Eq(t, []string{signedIn.FamilyID}, revocation.FamilyIDs)
	})

	// The anti-enumeration answer stays an answer: a sign-out that ended
	// nothing tells the hooks nothing either.
	T.Run("a sign-out that ended nothing runs no hook", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		must.NoError(t, e.svc.SignOut(t.Context(), testScope, "a token nobody was ever issued"))
		test.SliceEmpty(t, e.hooks.revocations)
	})

	T.Run("a sign-out with a spent token reports a reuse, not a sign-out", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
		must.NoError(t, err)

		must.NoError(t, e.svc.SignOut(t.Context(), testScope, signedIn.RefreshToken))

		revocation := requireRevocation(t, e)
		test.EqOp(t, signin.RevocationReuse, revocation.Reason)
		test.EqOp(t, e.user.ID, revocation.SubjectID)
		test.EqOp(t, "", revocation.ActorID)
		test.Eq(t, []string{signedIn.FamilyID}, revocation.FamilyIDs)
	})

	T.Run("a reused refresh token reports the family it ended", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		must.NoError(t, err)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		must.ErrorIs(t, err, signin.ErrRefreshTokenReused)

		revocation := requireRevocation(t, e)
		test.EqOp(t, signin.RevocationReuse, revocation.Reason)
		test.EqOp(t, e.user.ID, revocation.SubjectID)
		test.Eq(t, []string{first.FamilyID}, revocation.FamilyIDs)

		// A second replay is still refused as a reuse, and ends nothing: the
		// family was over already.
		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		must.ErrorIs(t, err, signin.ErrRefreshTokenReused)
		test.SliceLen(t, 1, e.hooks.revocations)
	})

	T.Run("signing out everywhere reports every login, as the person's own act", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		second, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		revoked, err := e.svc.SignOutEverywhere(t.Context(), testScope, e.user.ID)
		must.NoError(t, err)
		test.EqOp(t, int64(2), revoked)

		revocation := requireRevocation(t, e)
		test.EqOp(t, signin.RevocationSignOutEverywhere, revocation.Reason)
		test.EqOp(t, e.user.ID, revocation.SubjectID)
		test.EqOp(t, e.user.ID, revocation.ActorID)
		test.SliceContainsAll(t, []string{first.FamilyID, second.FamilyID}, revocation.FamilyIDs)

		test.EqOp(t, 0, liveSignIns(t, e))
	})

	T.Run("an operator's revocation of a person reports who asked", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.RevokeRefreshTokensForSubject(t.Context(), testScope, e.user.ID, signin.RevokedBy("operator_1"))
		must.NoError(t, err)

		revocation := requireRevocation(t, e)
		test.EqOp(t, signin.RevocationOperator, revocation.Reason)
		test.EqOp(t, e.user.ID, revocation.SubjectID)
		test.EqOp(t, "operator_1", revocation.ActorID)
		test.Eq(t, []string{signedIn.FamilyID}, revocation.FamilyIDs)
	})

	// Ended by its id alone, and the hook is still told whose it was.
	T.Run("an operator's revocation of a login names its owner", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		revoked, err := e.svc.RevokeRefreshTokenFamily(t.Context(), testScope, signedIn.FamilyID)
		must.NoError(t, err)
		test.EqOp(t, int64(1), revoked)

		revocation := requireRevocation(t, e)
		test.EqOp(t, signin.RevocationOperator, revocation.Reason)
		test.EqOp(t, e.user.ID, revocation.SubjectID)
		test.EqOp(t, "", revocation.ActorID)
		test.Eq(t, []string{signedIn.FamilyID}, revocation.FamilyIDs)

		// Already over: zero, and nothing heard.
		revoked, err = e.svc.RevokeRefreshTokenFamily(t.Context(), testScope, signedIn.FamilyID)
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)
		test.SliceLen(t, 1, e.hooks.revocations)
	})

	// Confined to a named person, an operator's end of one login is still
	// the operator's act, and a family that is somebody else's ends nothing.
	T.Run("an operator's revocation of a login confined to its holder", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		revoked, err := e.svc.RevokeRefreshTokenFamily(t.Context(), testScope, signedIn.FamilyID,
			signin.HeldBy("somebody_else"), signin.RevokedBy("operator_1"))
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)
		test.SliceEmpty(t, e.hooks.revocations)
		test.EqOp(t, 1, liveSignIns(t, e))

		revoked, err = e.svc.RevokeRefreshTokenFamily(t.Context(), testScope, signedIn.FamilyID,
			signin.HeldBy(e.user.ID), signin.RevokedBy("operator_1"))
		must.NoError(t, err)
		test.EqOp(t, int64(1), revoked)

		revocation := requireRevocation(t, e)
		test.EqOp(t, signin.RevocationOperator, revocation.Reason)
		test.EqOp(t, e.user.ID, revocation.SubjectID)
		test.EqOp(t, "operator_1", revocation.ActorID)
		test.Eq(t, []string{signedIn.FamilyID}, revocation.FamilyIDs)
		test.EqOp(t, 0, liveSignIns(t, e))
	})

	// A confinement to nobody fails closed rather than confining nothing.
	T.Run("an operator's revocation confined to nobody is refused", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.RevokeRefreshTokenFamily(t.Context(), testScope, signedIn.FamilyID, signin.HeldBy(""))
		must.ErrorIs(t, err, signin.ErrEmptyUserID)
		test.SliceEmpty(t, e.hooks.revocations)
		test.EqOp(t, 1, liveSignIns(t, e))
	})

	T.Run("ending one login reports it", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		ended, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.EndSignIn(t.Context(), testScope, e.user.ID, ended.FamilyID)
		must.NoError(t, err)

		revocation := requireRevocation(t, e)
		test.EqOp(t, signin.RevocationEndSignIn, revocation.Reason)
		test.EqOp(t, e.user.ID, revocation.SubjectID)
		test.EqOp(t, e.user.ID, revocation.ActorID)
		test.Eq(t, []string{ended.FamilyID}, revocation.FamilyIDs)
	})

	T.Run("ending the other logins reports each of them, and not the one asking", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		kept, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		phone, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		laptop, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.EndOtherSignIns(t.Context(), testScope, e.user.ID, kept.FamilyID)
		must.NoError(t, err)

		revocation := requireRevocation(t, e)
		test.EqOp(t, signin.RevocationEndOtherSignIns, revocation.Reason)
		test.EqOp(t, e.user.ID, revocation.SubjectID)
		test.EqOp(t, e.user.ID, revocation.ActorID)
		test.SliceContainsAll(t, []string{phone.FamilyID, laptop.FamilyID}, revocation.FamilyIDs)
	})

	T.Run("ending the other logins when there are none runs no hook", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		kept, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.EndOtherSignIns(t.Context(), testScope, e.user.ID, kept.FamilyID)
		must.NoError(t, err)
		test.SliceEmpty(t, e.hooks.revocations)
	})

	// EndSignIn's answer for somebody else's family is the answer for one that
	// never existed, and the hook must not be the difference.
	T.Run("ending a login that is not the caller's runs no hook", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		theirs, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.EndSignIn(t.Context(), testScope, "somebody_else", theirs.FamilyID)
		must.NoError(t, err)

		_, err = e.svc.EndSignIn(t.Context(), testScope, e.user.ID, "no_such_family")
		must.NoError(t, err)

		test.SliceEmpty(t, e.hooks.revocations)
	})

	T.Run("a person with no live login runs no hook", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		revoked, err := e.svc.SignOutEverywhere(t.Context(), testScope, e.user.ID)
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)

		test.SliceEmpty(t, e.hooks.revocations)
	})

	T.Run("refuses a sign-out everywhere that named nobody", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		_, err := e.svc.SignOutEverywhere(t.Context(), testScope, "")
		test.ErrorIs(t, err, signin.ErrEmptyUserID)

		_, err = newEnv(t).svc.SignOutEverywhere(t.Context(), testScope, e.user.ID)
		test.ErrorIs(t, err, signin.ErrRefreshTokensNotConfigured)
	})
}

// TestService_AfterRevokeSignIns_Refusing is the other half of one transaction:
// a service that cannot record ending a login does not end it.
func TestService_AfterRevokeSignIns_Refusing(T *testing.T) {
	T.Parallel()

	T.Run("rolls back a sign-out", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)
		e.hooks.revokeErr = errRecordingFailed

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		must.ErrorIs(t, e.svc.SignOut(t.Context(), testScope, signedIn.RefreshToken), errRecordingFailed)

		// Neither spent nor revoked: the token still exchanges.
		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
		test.NoError(t, err)
	})

	T.Run("rolls back signing out everywhere", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)
		e.hooks.revokeErr = errRecordingFailed

		_, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.SignOutEverywhere(t.Context(), testScope, e.user.ID)
		must.ErrorIs(t, err, errRecordingFailed)

		test.EqOp(t, 1, liveSignIns(t, e))
	})

	T.Run("rolls back ending one login", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)
		e.hooks.revokeErr = errRecordingFailed

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.EndSignIn(t.Context(), testScope, e.user.ID, signedIn.FamilyID)
		must.ErrorIs(t, err, errRecordingFailed)

		test.EqOp(t, 1, liveSignIns(t, e))
	})

	T.Run("rolls back an operator's revocations", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)
		e.hooks.revokeErr = errRecordingFailed

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.RevokeRefreshTokenFamily(t.Context(), testScope, signedIn.FamilyID)
		must.ErrorIs(t, err, errRecordingFailed)

		_, err = e.svc.RevokeRefreshTokensForSubject(t.Context(), testScope, e.user.ID)
		must.ErrorIs(t, err, errRecordingFailed)

		test.EqOp(t, 1, liveSignIns(t, e))
	})

	// The costly direction, stated on the hook: the reuse is not responded to.
	// The replay is answered with the hook's failure rather than the reuse, and
	// the successor is still live.
	T.Run("rolls back a reuse's revocation", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		successor, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		must.NoError(t, err)

		e.hooks.revokeErr = errRecordingFailed

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		must.ErrorIs(t, err, errRecordingFailed)
		test.False(t, platformerrors.Is(err, signin.ErrRefreshTokenReused))

		e.hooks.revokeErr = nil

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, successor.RefreshToken)
		test.NoError(t, err)
	})
}

// bareReuseStore is a store that reports a reuse with the sentinel alone,
// which is what a RefreshTokenStore written before the typed refusal would do.
type bareReuseStore struct {
	*refreshtokens.SQLStore
}

func (s bareReuseStore) Redeem(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	secret string,
) (*signin.RefreshToken, error) {
	token, err := s.SQLStore.Redeem(ctx, tx, scope, secret)
	if platformerrors.Is(err, signin.ErrRefreshTokenReused) {
		return nil, signin.ErrRefreshTokenReused
	}

	return token, err
}

// A store that reports a reuse bare has still revoked the family into the
// caller's transaction, and that revocation is the one thing about the refusal
// that must not be lost: the transaction commits it, the hook — which cannot be
// told what ended — does not run, and the caller is answered with a failure
// naming the broken contract rather than with the refusal a client would read
// past. Both doors that capture a reuse take that path.
func TestService_AfterRevokeSignIns_RefusesAStoreThatCannotNameTheFamily(T *testing.T) {
	T.Parallel()

	// replay signs in, exchanges once, and hands back the spent token with the
	// service that will be presented it and the successor it was exchanged for.
	replay := func(t *testing.T) (e *env, svc *signin.Service, spent, successor string) {
		t.Helper()

		e = newRefreshEnv(t)

		svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer, []string{"owner"}, e.hooks,
			signin.WithRefreshTokenStore(bareReuseStore{SQLStore: e.refresh}),
		)
		must.NoError(t, err)

		first, err := svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		next, err := svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		must.NoError(t, err)

		return e, svc, first.RefreshToken, next.RefreshToken
	}

	// requireCommitted asserts the family the replay ended stayed ended: the
	// successor is refused, and the login is gone from the person's list.
	requireCommitted := func(t *testing.T, e *env, svc *signin.Service, successor string) {
		t.Helper()

		_, err := svc.ExchangeRefreshToken(t.Context(), testScope, successor)
		must.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.EqOp(t, 0, liveSignIns(t, e))
	}

	T.Run("an exchange commits the revocation and fails loudly", func(t *testing.T) {
		t.Parallel()

		e, svc, spent, successor := replay(t)

		_, err := svc.ExchangeRefreshToken(t.Context(), testScope, spent)
		must.ErrorIs(t, err, signin.ErrRefreshTokenStoreContractViolated)
		test.False(t, platformerrors.Is(err, signin.ErrInvalidCredentials))
		test.SliceEmpty(t, e.hooks.revocations)

		requireCommitted(t, e, svc, successor)
	})

	T.Run("a sign-out commits the revocation and fails loudly", func(t *testing.T) {
		t.Parallel()

		e, svc, spent, successor := replay(t)

		err := svc.SignOut(t.Context(), testScope, spent)
		must.ErrorIs(t, err, signin.ErrRefreshTokenStoreContractViolated)
		test.False(t, platformerrors.Is(err, signin.ErrInvalidCredentials))
		test.SliceEmpty(t, e.hooks.revocations)

		requireCommitted(t, e, svc, successor)
	})
}

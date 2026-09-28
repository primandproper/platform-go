package signin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var errNotPermitted = errors.New("operator may not imitate users")

// admitAll is an ImpersonationPolicy with no opinion, for the tests about what
// the door does once a deployment has said it exists.
func admitAll(context.Context, *identity.User, *identity.User) error { return nil }

// newOperator registers a second user to act as e.user.
func (e *env) newOperator(t *testing.T) *identity.User {
	t.Helper()

	return e.registerPasswordless(t, "operator")
}

func TestService_IssueImpersonationToken(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		var asked [2]string

		e := newRefreshEnv(t, signin.WithImpersonationPolicy(
			func(_ context.Context, operator, subject *identity.User) error {
				asked = [2]string{operator.ID, subject.ID}
				return nil
			}))
		operator := e.newOperator(t)

		signedIn, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, e.user.ID, "")
		must.NoError(t, err)

		test.Eq(t, [2]string{operator.ID, e.user.ID}, asked)

		// The token is the subject's, and names the operator beside them.
		test.EqOp(t, "token-for-"+e.user.ID, signedIn.Token)
		test.EqOp(t, e.user.ID, signedIn.Principal.User.ID)
		test.EqOp(t, operator.ID, signedIn.ActorID)
		test.EqOp[any](t, operator.ID, e.issuer.claims[signin.ClaimActor])

		// Short-lived, ordinary, and with nothing behind it to extend it.
		test.EqOp(t, signin.DefaultImpersonationTokenTTL, e.issuer.expiry)
		test.False(t, signedIn.Administrative)
		test.EqOp[any](t, false, e.issuer.claims[signin.ClaimAdministrative])
		test.EqOp(t, "", signedIn.RefreshToken)
		test.True(t, signedIn.RefreshTokenExpiresAt.IsZero())

		var rows int
		must.NoError(t, e.client.Writer().QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM "+refreshTable(t, e)).Scan(&rows))
		test.EqOp(t, 0, rows)

		// The record, in the mint's transaction.
		test.Eq(t, []string{"authenticate", "issue"}, e.hooks.calls)
		must.SliceLen(t, 1, e.hooks.authentications)
		test.EqOp(t, signin.CredentialKindImpersonation, e.hooks.authentications[0].CredentialKind)
		test.EqOp(t, operator.ID, e.hooks.authentications[0].ActorID)
		test.EqOp(t, e.user.ID, e.hooks.authentications[0].Principal.User.ID)
		test.False(t, e.hooks.authentications[0].Administrative)
		must.SliceLen(t, 1, e.hooks.signIns)
		test.EqOp(t, operator.ID, e.hooks.signIns[0].ActorID)
		test.SliceEmpty(t, e.hooks.failures)
	})

	T.Run("the lifetime is configurable", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithImpersonationPolicy(admitAll),
			signin.WithImpersonationTokenTTL(signin.DefaultImpersonationTokenTTL/3))
		operator := e.newOperator(t)

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, e.user.ID, "")
		must.NoError(t, err)
		test.EqOp(t, signin.DefaultImpersonationTokenTTL/3, e.issuer.expiry)
	})

	T.Run("no policy means no door, and the attempt is recorded as the operator's", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		operator := e.newOperator(t)

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrImpersonationDisabled)

		test.SliceEmpty(t, e.hooks.signIns)
		must.SliceLen(t, 1, e.hooks.failures)
		test.EqOp(t, e.user.ID, e.hooks.failures[0].UserID)
		test.EqOp(t, operator.ID, e.hooks.failures[0].ActorID)
		test.ErrorIs(t, e.hooks.failures[0].Reason, signin.ErrImpersonationDisabled)
	})

	T.Run("the policy's refusal is the refusal", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithImpersonationPolicy(
			func(context.Context, *identity.User, *identity.User) error { return errNotPermitted }))
		operator := e.newOperator(t)

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, e.user.ID, "")
		test.ErrorIs(t, err, errNotPermitted)

		test.SliceEmpty(t, e.hooks.signIns)
		must.SliceLen(t, 1, e.hooks.failures)
		test.EqOp(t, operator.ID, e.hooks.failures[0].ActorID)
		test.ErrorIs(t, e.hooks.failures[0].Reason, errNotPermitted)
	})

	T.Run("a suspended operator acts as nobody", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithImpersonationPolicy(admitAll))
		operator := e.newOperator(t)

		must.NoError(t, e.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			return e.store.UpdateUserAccountStatus(t.Context(), tx, testScope, operator.ID, identity.StatusBanned, "")
		}))

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrUserBanned)
		test.SliceEmpty(t, e.hooks.signIns)
		must.SliceLen(t, 1, e.hooks.failures)
		test.EqOp(t, operator.ID, e.hooks.failures[0].ActorID)
	})

	T.Run("a suspended subject is not signed in as by the back door", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithImpersonationPolicy(admitAll))
		operator := e.newOperator(t)
		e.setStatus(t, identity.StatusTerminated, "")

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrUserTerminated)
		test.SliceEmpty(t, e.hooks.signIns)
		must.SliceLen(t, 1, e.hooks.failures)
	})

	T.Run("empty IDs are refused before anything is read", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithImpersonationPolicy(admitAll))

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, "", e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrEmptyUserID)

		_, err = e.svc.IssueImpersonationToken(t.Context(), testScope, e.user.ID, "", "")
		test.ErrorIs(t, err, signin.ErrEmptyUserID)

		test.SliceEmpty(t, e.hooks.failures)
	})

	T.Run("an invalid scope is refused", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithImpersonationPolicy(admitAll))

		_, err := e.svc.IssueImpersonationToken(t.Context(), tenancy.Scope{}, "operator", e.user.ID, "")
		test.Error(t, err)
		test.SliceEmpty(t, e.hooks.signIns)
	})

	T.Run("a builder that forgets the actor cannot lose it", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t,
			signin.WithImpersonationPolicy(admitAll),
			signin.WithClaimsBuilder(func(context.Context, *signin.ClaimsInput) (map[string]any, error) {
				return nil, nil
			}))
		operator := e.newOperator(t)

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, e.user.ID, "")
		must.NoError(t, err)
		test.Eq(t, map[string]any{signin.ClaimActor: operator.ID}, e.issuer.claims)
	})

	T.Run("a builder cannot forge an actor onto an ordinary token", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithClaimsBuilder(func(context.Context, *signin.ClaimsInput) (map[string]any, error) {
			return map[string]any{signin.ClaimActor: "somebody", "kept": true}, nil
		}))

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)
		test.EqOp(t, "", signedIn.ActorID)
		test.Eq(t, map[string]any{"kept": true}, e.issuer.claims)
	})
}

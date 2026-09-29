package signin_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

// staffScope is a directory of operators kept apart from testScope's customers.
var staffScope = tenancy.Of("staff_1")

// newOperatorIn registers an operator in a scope of their own.
func (e *env) newOperatorIn(t *testing.T, scope tenancy.Scope) *identity.User {
	t.Helper()

	registration, err := e.directory.Register(t.Context(), scope,
		&identity.User{
			Username:      "staff-operator",
			EmailAddress:  "staff-operator@example.com",
			AccountStatus: identity.StatusGood,
			Scope:         scope,
		},
		&identity.Account{Name: "staff", Scope: scope},
		[]string{"owner"},
	)
	must.NoError(t, err)

	return registration.User
}

func TestService_IssueImpersonationToken(T *testing.T) {
	T.Parallel()

	T.Run("an operator in a scope of their own acts in the subject's", func(t *testing.T) {
		t.Parallel()

		var scopes [2]tenancy.Scope

		e := newEnv(t, signin.WithImpersonationPolicy(
			func(_ context.Context, operator, subject *identity.User) error {
				scopes = [2]tenancy.Scope{operator.Scope, subject.Scope}
				return nil
			}))
		operator := e.newOperatorIn(t, staffScope)

		signedIn, err := e.svc.IssueImpersonationToken(t.Context(), staffScope, operator.ID, testScope, e.user.ID, "")
		must.NoError(t, err)

		// The policy sees both scopes, which is where a deployment compares them.
		test.Eq(t, [2]tenancy.Scope{staffScope, testScope}, scopes)

		// The token is the subject's, in the subject's scope, and names where
		// the operator lives beside who they are.
		test.EqOp(t, e.user.ID, signedIn.Principal.User.ID)
		test.EqOp[any](t, testScope.Owner(), e.issuer.claims[signin.ClaimScope])
		test.EqOp[any](t, operator.ID, e.issuer.claims[signin.ClaimActor])
		test.EqOp[any](t, staffScope.Owner(), e.issuer.claims[signin.ClaimActorScope])
		test.EqOp(t, staffScope, signedIn.ActorScope)

		must.SliceLen(t, 1, e.hooks.authentications)
		test.EqOp(t, staffScope, e.hooks.authentications[0].ActorScope)
	})

	T.Run("an operator is looked for where the caller says they are", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithImpersonationPolicy(admitAll))
		operator := e.newOperatorIn(t, staffScope)

		// Named in the subject's scope, the staff operator is nobody.
		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, testScope, e.user.ID, "")
		test.ErrorIs(t, err, identity.ErrUserNotFound)
		test.SliceEmpty(t, e.hooks.signIns)
	})

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		var asked [2]string

		e := newRefreshEnv(t, signin.WithImpersonationPolicy(
			func(_ context.Context, operator, subject *identity.User) error {
				asked = [2]string{operator.ID, subject.ID}
				return nil
			}))
		operator := e.newOperator(t)

		signedIn, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, testScope, e.user.ID, "")
		must.NoError(t, err)

		test.Eq(t, [2]string{operator.ID, e.user.ID}, asked)

		// The token is the subject's, and names the operator beside them.
		test.EqOp(t, "token-for-"+e.user.ID, signedIn.Token)
		test.EqOp(t, e.user.ID, signedIn.Principal.User.ID)
		test.EqOp(t, operator.ID, signedIn.ActorID)
		test.EqOp[any](t, operator.ID, e.issuer.claims[signin.ClaimActor])
		test.EqOp[any](t, testScope.Owner(), e.issuer.claims[signin.ClaimActorScope])
		test.EqOp(t, testScope, signedIn.ActorScope)

		// Short-lived, ordinary, and with nothing behind it to extend it.
		test.EqOp(t, signin.DefaultImpersonationTokenTTL, e.issuer.expiry)
		test.False(t, signedIn.Administrative)
		test.EqOp[any](t, false, e.issuer.claims[signin.ClaimAdministrative])
		test.EqOp(t, "", signedIn.RefreshToken)
		test.True(t, signedIn.RefreshTokenExpiresAt.IsZero())

		// One row, recording the login and no credential: nothing handed out
		// can exchange it.
		var rows int
		must.NoError(t, e.client.Writer().QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM "+refreshTable(t, e)).Scan(&rows))
		test.EqOp(t, 1, rows)

		// The record, in the mint's transaction.
		test.Eq(t, []string{"authenticate", "issue"}, e.hooks.calls)
		must.SliceLen(t, 1, e.hooks.authentications)
		test.EqOp(t, signin.CredentialKindImpersonation, e.hooks.authentications[0].CredentialKind)
		test.EqOp(t, operator.ID, e.hooks.authentications[0].ActorID)
		test.EqOp(t, testScope, e.hooks.authentications[0].ActorScope)
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

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, testScope, e.user.ID, "")
		must.NoError(t, err)
		test.EqOp(t, signin.DefaultImpersonationTokenTTL/3, e.issuer.expiry)
	})

	T.Run("no policy means no door, and the attempt is recorded as the operator's", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		operator := e.newOperator(t)

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, testScope, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrImpersonationDisabled)

		test.SliceEmpty(t, e.hooks.signIns)
		must.SliceLen(t, 1, e.hooks.failures)
		test.EqOp(t, e.user.ID, e.hooks.failures[0].UserID)
		test.EqOp(t, operator.ID, e.hooks.failures[0].ActorID)
		test.EqOp(t, testScope, e.hooks.failures[0].ActorScope)
		test.ErrorIs(t, e.hooks.failures[0].Reason, signin.ErrImpersonationDisabled)
	})

	T.Run("the policy's refusal is the refusal", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithImpersonationPolicy(
			func(context.Context, *identity.User, *identity.User) error { return errNotPermitted }))
		operator := e.newOperator(t)

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, testScope, e.user.ID, "")
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

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, testScope, e.user.ID, "")
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

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, testScope, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrUserTerminated)
		test.SliceEmpty(t, e.hooks.signIns)
		must.SliceLen(t, 1, e.hooks.failures)
	})

	T.Run("empty IDs are refused before anything is read", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithImpersonationPolicy(admitAll))

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, "", testScope, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrEmptyUserID)

		_, err = e.svc.IssueImpersonationToken(t.Context(), testScope, e.user.ID, testScope, "", "")
		test.ErrorIs(t, err, signin.ErrEmptyUserID)

		test.SliceEmpty(t, e.hooks.failures)
	})

	T.Run("an operator impersonating themselves is refused before anything is read", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithImpersonationPolicy(admitAll))

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, e.user.ID, testScope, e.user.ID, "")
		test.ErrorIs(t, err, signin.ErrSelfImpersonation)
		test.SliceEmpty(t, e.hooks.signIns)
		test.SliceEmpty(t, e.hooks.failures)
	})

	T.Run("an invalid scope is refused", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithImpersonationPolicy(admitAll))

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, "operator", tenancy.Scope{}, e.user.ID, "")
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = e.svc.IssueImpersonationToken(t.Context(), tenancy.Scope{}, "operator", testScope, e.user.ID, "")
		test.ErrorIs(t, err, tenancy.ErrNoScope)

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

		_, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, testScope, e.user.ID, "")
		must.NoError(t, err)
		test.Eq(t, map[string]any{
			signin.ClaimActor:      operator.ID,
			signin.ClaimActorScope: testScope.Owner(),
		}, e.issuer.claims)
	})

	T.Run("a builder cannot forge an actor onto an ordinary token", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithClaimsBuilder(func(context.Context, *signin.ClaimsInput) (map[string]any, error) {
			return map[string]any{signin.ClaimActor: "somebody", signin.ClaimActorScope: "elsewhere", "kept": true}, nil
		}))

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)
		test.EqOp(t, "", signedIn.ActorID)
		test.Eq(t, map[string]any{"kept": true}, e.issuer.claims)
	})
}

// An impersonation is a login everywhere one is read: the per-request check
// accepts it, the subject sees it named as somebody else's, and ending it ends
// it at once.
func TestService_IssueImpersonationToken_IsALogin(T *testing.T) {
	T.Parallel()

	e := newRefreshEnv(T, signin.WithImpersonationPolicy(admitAll))
	operator := e.newOperator(T)

	signedIn, err := e.svc.IssueImpersonationToken(T.Context(), testScope, operator.ID, testScope, e.user.ID, "")
	must.NoError(T, err)

	must.NoError(T, e.svc.CheckSignIn(T.Context(), testScope, signedIn.FamilyID, signedIn.TokenID),
		must.Sprint("a deployment checking every request refused a live impersonation"))

	signIns, err := e.svc.ListSignIns(T.Context(), testScope, e.user.ID, 0)
	must.NoError(T, err)
	must.SliceLen(T, 1, signIns)
	test.EqOp(T, signedIn.FamilyID, signIns[0].FamilyID)
	test.EqOp(T, operator.ID, signIns[0].ActorID, test.Sprint("the subject's list did not say who was signed in as them"))
	test.EqOp(T, signin.DefaultImpersonationTokenTTL,
		signIns[0].ExpiresAt.Sub(signIns[0].SignedInAt).Round(time.Minute))

	// The subject ends it from their own list, and the operator's token stops
	// on its next request.
	_, err = e.svc.EndSignIn(T.Context(), testScope, e.user.ID, signedIn.FamilyID)
	must.NoError(T, err)

	test.ErrorIs(T, e.svc.CheckSignIn(T.Context(), testScope, signedIn.FamilyID, signedIn.TokenID), signin.ErrSignInEnded)
}

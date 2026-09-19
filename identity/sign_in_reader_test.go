package identity

import (
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runSignInReaderSuite covers the two sign-in lookups and the principal read
// every authenticated request afterwards makes.
func runSignInReaderSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("reads by username and email, live only", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		byName, err := store.GetUserByUsername(t.Context(), env.reader(), testScope, "ada")
		must.NoError(t, err)
		test.EqOp(t, user.ID, byName.ID)

		byEmail, err := store.GetUserByEmailAddress(t.Context(), env.reader(), testScope, "ada@example.com")
		must.NoError(t, err)
		test.EqOp(t, user.ID, byEmail.ID)

		must.NoError(t, env.archiveUserErr(t, store, testScope, user.ID))

		// Every read by id excludes archived users too, now that they all run
		// querygen's single-row statement. A caller who wants an archived user
		// back wants a different query rather than a flag on this one.
		_, err = store.GetUserByUsername(t.Context(), env.reader(), testScope, "ada")
		must.ErrorIs(t, err, ErrUserNotFound)

		_, err = store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.ErrorIs(t, err, ErrUserNotFound)

		_, err = store.GetPrincipal(t.Context(), env.reader(), testScope, user.ID, "")
		must.ErrorIs(t, err, ErrUserNotFound)
	})

	t.Run("builds a principal", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		user := newUser("ada")
		user.ServiceRoles = []string{"service_admin"}
		seedUser(t, env, store, user)

		first := seedAccountFor(t, env, store, user, "First", "account_admin")
		second := seedAccountFor(t, env, store, user, "Second", "account_member")

		principal, err := store.GetPrincipal(t.Context(), env.reader(), testScope, user.ID, "")
		must.NoError(t, err)

		// No account named, so the default answers.
		test.EqOp(t, first.ID, principal.ActiveAccountID)
		test.EqOp(t, "", principal.User.HashedPassword)
		test.Eq(t, []string{"account_admin"}, principal.AccountRoles())
		test.Eq(t, []string{"service_admin"}, principal.ServiceRoles())

		// Roles is the union, so a PolicyResolver cannot be handed half the
		// answer.
		test.Eq(t, []string{"service_admin", "account_admin"}, principal.Roles())
		test.Eq(t, []string{first.ID, second.ID}, principal.AccountIDs())

		switched, err := store.GetPrincipal(t.Context(), env.reader(), testScope, user.ID, second.ID)
		must.NoError(t, err)
		test.EqOp(t, second.ID, switched.ActiveAccountID)
		test.Eq(t, []string{"account_member"}, switched.AccountRoles())
	})

	t.Run("refuses a principal for an account the user is not in", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		outsider := seedUser(t, env, store, newUser("mallory"))

		account := seedAccountFor(t, env, store, owner, "Acme")
		seedAccountFor(t, env, store, outsider, "Elsewhere")

		// The check every hand-built session context eventually forgets. Without
		// it everything downstream trusts the ID it was handed.
		_, err := store.GetPrincipal(t.Context(), env.reader(), testScope, outsider.ID, account.ID)
		must.ErrorIs(t, err, ErrMembershipNotFound)
	})

	t.Run("builds a principal for a user who belongs to no account", func(t *testing.T) {
		t.Parallel()

		// The read every authenticated request makes has to answer for an
		// operator who has never been put in an account, because signing in is
		// how they get put in one — see ErrNoDefaultAccount.
		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		principal, err := store.GetPrincipal(t.Context(), env.reader(), testScope, user.ID, "")
		must.NoError(t, err)
		test.EqOp(t, user.ID, principal.User.ID)
		test.EqOp(t, "", principal.ActiveAccountID)
		test.Nil(t, principal.ActiveMembership())
		test.SliceEmpty(t, principal.Memberships)
		test.SliceEmpty(t, principal.AccountRoles())
	})

	t.Run("refuses an account a user who belongs to none names", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))
		owner := seedUser(t, env, store, newUser("brian"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		_, err := store.GetPrincipal(t.Context(), env.reader(), testScope, user.ID, account.ID)
		must.ErrorIs(t, err, ErrMembershipNotFound)
	})

	// A ban that took effect at token expiry was a ban the operator believed they
	// had applied. This is the read every authenticated request makes, so this is
	// where it becomes true.
	t.Run("refuses a principal for a status that admits no sign-in", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, user, "Acme")

		// Good standing first, so the refusal below is the status changing rather
		// than anything else about the row.
		principal, err := store.GetPrincipal(t.Context(), env.reader(), testScope, user.ID, account.ID)
		must.NoError(t, err)
		test.EqOp(t, account.ID, principal.ActiveAccountID)

		must.NoError(t, env.updateUserAccountStatus(t, store, testScope, user.ID, StatusBanned, "spam"))

		refused, err := store.GetPrincipal(t.Context(), env.reader(), testScope, user.ID, account.ID)
		test.Nil(t, refused)
		must.ErrorIs(t, err, ErrSignInNotAdmitted)

		// Named in the wrapped message and not in the sentinel: the door tells the
		// three statuses apart, a log can, and a client is told one thing.
		test.StrContains(t, err.Error(), string(StatusBanned))

		// A reinstatement is effective on the next read, the same way.
		must.NoError(t, env.updateUserAccountStatus(t, store, testScope, user.ID, StatusGood, ""))

		readmitted, err := store.GetPrincipal(t.Context(), env.reader(), testScope, user.ID, account.ID)
		must.NoError(t, err)
		test.EqOp(t, account.ID, readmitted.ActiveAccountID)
	})

	// The other two refusing statuses, and the point of the loop is that the
	// answer is one sentinel rather than three. StatusUnverified is in it because
	// it is the status CreateUser assigns by default, so a directory whose
	// registrations never move to StatusGood answers no principal at all.
	t.Run("refuses every status but good", func(t *testing.T) {
		t.Parallel()

		for _, status := range []AccountStatus{StatusUnverified, StatusTerminated} {
			t.Run(status.String(), func(t *testing.T) {
				t.Parallel()

				store := env.newStore(t)
				user := seedUser(t, env, store, newUser("ada_"+status.String()))
				seedAccountFor(t, env, store, user, "Acme "+status.String())

				must.NoError(t, env.updateUserAccountStatus(t, store, testScope, user.ID, status, ""))

				_, err := store.GetPrincipal(t.Context(), env.reader(), testScope, user.ID, "")
				must.ErrorIs(t, err, ErrSignInNotAdmitted)
			})
		}
	})
}

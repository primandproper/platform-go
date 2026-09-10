package identity

import (
	"testing"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runMembershipWriterSuite covers who belongs to an account and what they may
// do there.
func runMembershipWriterSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("keeps exactly one default", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		first := seedAccountFor(t, env, store, owner, "First")
		second := seedAccountFor(t, env, store, owner, "Second")

		must.NoError(t, env.setDefaultAccount(t, store, testScope, owner.ID, second.ID))

		memberships, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, owner.ID)
		must.NoError(t, err)
		must.SliceLen(t, 2, memberships)

		// Default first, so a caller taking the head gets the one the user lands
		// in.
		test.EqOp(t, second.ID, memberships[0].BelongsToAccount)
		test.True(t, memberships[0].DefaultAccount)

		defaults := 0

		for _, membership := range memberships {
			if membership.DefaultAccount {
				defaults++
			}
		}

		test.EqOp(t, 1, defaults)
		test.EqOp(t, first.ID, memberships[1].BelongsToAccount)

		err = env.setDefaultAccount(t, store, testScope, owner.ID, "not-an-account")
		must.ErrorIs(t, err, ErrMembershipNotFound)

		// Setting the default a second time is not the membership going
		// missing. It matters on MySQL, whose :execrows count is rows *changed*
		// rather than rows matched — the statement stamps last_updated_at from
		// the server's clock, so a write that assigns the flag it already held
		// still changes the row and still counts.
		must.NoError(t, env.setDefaultAccount(t, store, testScope, owner.ID, second.ID))

		again, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, owner.ID)
		must.NoError(t, err)
		must.SliceLen(t, 2, again)
		test.EqOp(t, second.ID, again[0].BelongsToAccount)
		test.True(t, again[0].DefaultAccount)
		test.False(t, again[1].DefaultAccount)
	})

	t.Run("replaces roles rather than merging them", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")
		member := seedUserInto(t, env, store, newUser("brian"), account.ID, "account_member", "billing_admin")

		before, err := store.GetMembership(t.Context(), env.reader(), testScope, member.ID, account.ID)
		must.NoError(t, err)
		test.Eq(t, []string{"account_member", "billing_admin"}, before.Roles)

		// Revocation is the operation that matters, and a merging setter cannot
		// express it.
		must.NoError(t, env.setMembershipRoles(t, store, testScope, member.ID, account.ID,
			[]string{"account_member"}))

		after, err := store.GetMembership(t.Context(), env.reader(), testScope, member.ID, account.ID)
		must.NoError(t, err)
		test.Eq(t, []string{"account_member"}, after.Roles)

		must.ErrorIs(t,
			env.setMembershipRoles(t, store, testScope, member.ID, account.ID, nil),
			platformerrors.ErrEmptyInputParameter,
		)

		must.ErrorIs(t,
			env.setMembershipRoles(t, store, testScope, member.ID, "not-an-account", []string{"x"}),
			ErrMembershipNotFound,
		)
	})

	t.Run("admits an empty role set for the owner and nobody else", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme", "account_admin", "billing_admin")
		member := seedUserInto(t, env, store, newUser("brian"), account.ID)

		// The owner's standing is the ownership, so stripping their roles
		// leaves them able to administer the account rather than belonging to
		// it and able to do nothing in it. It is also the state
		// TransferAccountOwnership mints, which is why this door has to open on
		// it too.
		must.NoError(t, env.setMembershipRoles(t, store, testScope, owner.ID, account.ID, nil))

		stripped, err := store.GetMembership(t.Context(), env.reader(), testScope, owner.ID, account.ID)
		must.NoError(t, err)
		test.SliceEmpty(t, stripped.Roles)

		// Everybody else keeps the refusal, which is the reason it exists.
		must.ErrorIs(t,
			env.setMembershipRoles(t, store, testScope, member.ID, account.ID, nil),
			platformerrors.ErrEmptyInputParameter,
		)

		unchanged, err := store.GetMembership(t.Context(), env.reader(), testScope, member.ID, account.ID)
		must.NoError(t, err)
		test.Eq(t, []string{"account_member"}, unchanged.Roles)

		// The exception is decided by an account read, so an account that is
		// not there reports as much rather than as an empty slice somebody
		// passed.
		must.ErrorIs(t,
			env.setMembershipRoles(t, store, testScope, owner.ID, "not-an-account", nil),
			ErrAccountNotFound,
		)
	})

	t.Run("refuses to remove the last owner", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		// An ownerless account fails every permission check that resolves
		// through its owner, far from the removal that caused it.
		must.ErrorIs(t,
			env.removeMembership(t, store, testScope, owner.ID, account.ID),
			ErrLastAccountOwner,
		)

		still, err := store.GetMembership(t.Context(), env.reader(), testScope, owner.ID, account.ID)
		must.NoError(t, err)
		test.False(t, still.Archived())
	})

	t.Run("moves the default when the default is removed", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		member := seedUser(t, env, store, newUser("brian"))

		first := seedAccountFor(t, env, store, owner, "First")
		second := seedAccountFor(t, env, store, owner, "Second")

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			for _, accountID := range []string{first.ID, second.ID} {
				if _, err := store.CreateMembership(t.Context(), tx, testScope, &Membership{
					BelongsToUser:    member.ID,
					BelongsToAccount: accountID,
					Roles:            []string{"account_member"},
				}); err != nil {
					return err
				}
			}

			return nil
		}))

		memberships, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, member.ID)
		must.NoError(t, err)
		must.SliceLen(t, 2, memberships)

		defaultAccountID := memberships[0].BelongsToAccount
		test.True(t, memberships[0].DefaultAccount)

		must.NoError(t, env.removeMembership(t, store, testScope, member.ID, defaultAccountID))

		// A user left with memberships and no default cannot build a Principal,
		// and the failure would surface at their next request rather than here.
		remaining, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, member.ID)
		must.NoError(t, err)
		must.SliceLen(t, 1, remaining)
		test.True(t, remaining[0].DefaultAccount)

		must.ErrorIs(t,
			env.removeMembership(t, store, testScope, member.ID, defaultAccountID),
			ErrMembershipNotFound,
		)
	})

	t.Run("leaves a user with nothing when their only membership is removed", func(t *testing.T) {
		t.Parallel()

		// The removal clears the default flag and then archives, in that
		// order, because the clear reaches live rows only. What is observable
		// is the pair of it: the user has no membership and no default, and a
		// rejoin makes the revived membership their default again because it
		// is once more their first live one.
		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")
		member := seedUserInto(t, env, store, newUser("brian"), account.ID)

		must.NoError(t, env.removeMembership(t, store, testScope, member.ID, account.ID))

		memberships, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, member.ID)
		must.NoError(t, err)
		test.SliceEmpty(t, memberships)

		_, err = store.GetPrincipal(t.Context(), env.reader(), testScope, member.ID, "")
		must.ErrorIs(t, err, ErrNoDefaultAccount)

		// Rejoining converges onto the archived row rather than inserting a
		// second one, and it is their first live membership again.
		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			_, createErr := store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    member.ID,
				BelongsToAccount: account.ID,
				Roles:            []string{"account_member"},
			})

			return createErr
		}))

		rejoined, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, member.ID)
		must.NoError(t, err)
		must.SliceLen(t, 1, rejoined)
		test.True(t, rejoined[0].DefaultAccount)
	})

	t.Run("transfers ownership and keeps the old owner on", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")
		successor := seedUser(t, env, store, newUser("grace"))

		// The successor is not yet a member. An owner who is not on the roster
		// is refused by every roster-driven permission check.
		must.NoError(t, env.transferAccountOwnership(t, store, testScope, account.ID, successor.ID))

		read, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.EqOp(t, successor.ID, read.OwnerUserID)

		minted, err := store.GetMembership(t.Context(), env.reader(), testScope, successor.ID, account.ID)
		must.NoError(t, err)

		// It carries no roles, which is the state rather than a gap in it: the
		// ownership is the standing, and a role invented here would be a name
		// this package does not define written into somebody's authorization.
		// SetMembershipRoles admits the same set for the same reason.
		test.SliceEmpty(t, minted.Roles)
		must.NoError(t, env.setMembershipRoles(t, store, testScope, successor.ID, account.ID, nil))

		// It is the successor's first membership anywhere, so it is their
		// default — the rule the other two doors that mint one apply. A minted
		// membership that was not the default would hand somebody an account
		// and no way to land in it: the sign-in read names no account, and a
		// user with memberships and no default has nowhere to go.
		test.True(t, minted.DefaultAccount)

		landing, err := store.GetPrincipal(t.Context(), env.reader(), testScope, successor.ID, "")
		must.NoError(t, err)
		test.EqOp(t, account.ID, landing.ActiveAccountID)

		// Transferring and ejecting are different acts; handing over and staying
		// on is the common case.
		_, err = store.GetMembership(t.Context(), env.reader(), testScope, owner.ID, account.ID)
		must.NoError(t, err)

		// The former owner can now be removed, which they could not be before.
		must.NoError(t, env.removeMembership(t, store, testScope, owner.ID, account.ID))

		// Transferring to the current owner is a no-op rather than an error.
		must.NoError(t, env.transferAccountOwnership(t, store, testScope, account.ID, successor.ID))

		must.ErrorIs(t,
			env.transferAccountOwnership(t, store, testScope, account.ID, ""),
			platformerrors.ErrEmptyInputParameter,
		)
	})

	t.Run("leaves a new owner's existing default where it was", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		// The successor already belongs somewhere and already lands there. The
		// membership the transfer mints is not their first, so it does not
		// become their default: handing somebody an account is not choosing
		// where they sign in, and the rule is "a first membership defaults"
		// rather than "the newest one wins".
		successor := seedUser(t, env, store, newUser("grace"))
		home := seedAccountFor(t, env, store, successor, "Home")

		must.NoError(t, env.transferAccountOwnership(t, store, testScope, account.ID, successor.ID))

		minted, err := store.GetMembership(t.Context(), env.reader(), testScope, successor.ID, account.ID)
		must.NoError(t, err)
		test.False(t, minted.DefaultAccount)

		landing, err := store.GetPrincipal(t.Context(), env.reader(), testScope, successor.ID, "")
		must.NoError(t, err)
		test.EqOp(t, home.ID, landing.ActiveAccountID)
	})

	t.Run("refuses a new owner from another directory", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		// A neighbor whose id is perfectly real and whose directory is not this
		// one. owner_user_id carries no foreign key at all and the membership's
		// carries no scope, so the store's own scoped read is the only thing
		// between this id and the account it would come to own.
		neighbor := newUser("eve")
		neighbor.Scope = otherScope
		seedUser(t, env, store, neighbor)

		must.ErrorIs(t,
			env.transferAccountOwnership(t, store, testScope, account.ID, neighbor.ID),
			ErrUserNotFound,
		)

		// Neither half of the transfer landed: the account keeps the owner it
		// had, and the roster gains no stranger.
		read, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.EqOp(t, owner.ID, read.OwnerUserID)

		_, err = store.GetMembership(t.Context(), env.reader(), testScope, neighbor.ID, account.ID)
		must.ErrorIs(t, err, ErrMembershipNotFound)

		members, err := store.ListAccountMembers(t.Context(), env.reader(), testScope, account.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, members.Data)
		test.EqOp(t, owner.ID, members.Data[0].User.ID)
	})

	t.Run("keeps an existing member's roles on transfer", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")
		successor := seedUserInto(t, env, store, newUser("grace"), account.ID, "billing_admin")

		must.NoError(t, env.transferAccountOwnership(t, store, testScope, account.ID, successor.ID))

		membership, err := store.GetMembership(t.Context(), env.reader(), testScope, successor.ID, account.ID)
		must.NoError(t, err)
		test.Eq(t, []string{"billing_admin"}, membership.Roles)
	})
}

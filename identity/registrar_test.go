package identity

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runRegistrarSuite covers the three writes that make a registration, each
// through the caller's executor.
func runRegistrarSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("creates a user and reads it back", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		test.NotEq(t, "", user.ID)
		test.False(t, user.CreatedAt.IsZero())

		read, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, "ada", read.Username)
		test.EqOp(t, "ada@example.com", read.EmailAddress)

		// GetUser is the credential read, so the hash comes back — this is what
		// a sign-in flow compares against.
		test.EqOp(t, user.HashedPassword, read.HashedPassword)
	})

	t.Run("generates an ID when none is given", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		user := newUser("grace")
		user.ID = ""

		seedUser(t, env, store, user)
		test.NotEq(t, "", user.ID)
	})

	t.Run("refuses a taken username", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		seedUser(t, env, store, newUser("ada"))

		second := newUser("ada")
		second.EmailAddress = "different@example.com"

		err := env.inTx(t, func(tx database.Tx) error {
			return store.CreateUser(t.Context(), tx, second.Scope, second)
		})
		must.ErrorIs(t, err, ErrUsernameTaken)
	})

	t.Run("refuses a taken email address", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		seedUser(t, env, store, newUser("ada"))

		second := newUser("grace")
		second.EmailAddress = "ada@example.com"

		err := env.inTx(t, func(tx database.Tx) error {
			return store.CreateUser(t.Context(), tx, second.Scope, second)
		})
		must.ErrorIs(t, err, ErrEmailAddressTaken)
	})

	t.Run("keeps an archived user's handle taken", func(t *testing.T) {
		t.Parallel()

		// The unique indexes cover archived rows, so the collision check must
		// see them: freeing a username when its owner is soft-deleted means a
		// later registrant can take it, and every audit row naming that handle
		// then refers to two people. The check is a generated read now, and
		// querygen derives its archived predicate from the column list it is
		// handed — so this is what pins the empty list that read is rendered
		// from. Adding columns to it would hand the second registration to the
		// index instead, which reports a driver error rather than this
		// sentinel.
		store := env.newStore(t)
		ada := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.archiveUser(t, store, testScope, ada.ID))

		second := newUser("ada")
		second.EmailAddress = "different@example.com"

		err := env.inTx(t, func(tx database.Tx) error {
			return store.CreateUser(t.Context(), tx, second.Scope, second)
		})
		must.ErrorIs(t, err, ErrUsernameTaken)
	})

	t.Run("allows the same username in another directory", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		seedUser(t, env, store, newUser("ada"))

		// The whole point of the scope being in the unique index: two
		// directories are two namespaces, and one customer's handles do not
		// exhaust another's.
		neighbor := newUser("ada")
		neighbor.Scope = otherScope

		seedUser(t, env, store, neighbor)

		read, err := store.GetUser(t.Context(), env.reader(), otherScope, neighbor.ID)
		must.NoError(t, err)
		test.EqOp(t, "ada", read.Username)
	})

	t.Run("refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.ErrorIs(t, store.CreateUser(t.Context(), nil, testScope, newUser("ada")), ErrNilExecutor)
	})

	t.Run("refuses a nil value on each of the three writes", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// Each guard sits behind the executor check, so reaching it needs a
		// live transaction — which is also the shape a registration flow that
		// dropped a value on the floor arrives in.
		must.ErrorIs(t, env.inTx(t, func(tx database.Tx) error {
			return store.CreateUser(t.Context(), tx, testScope, nil)
		}), ErrNilUser)

		must.ErrorIs(t, env.inTx(t, func(tx database.Tx) error {
			return store.CreateAccount(t.Context(), tx, testScope, nil)
		}), ErrNilAccount)

		must.ErrorIs(t, env.inTx(t, func(tx database.Tx) error {
			return store.CreateMembership(t.Context(), tx, testScope, nil)
		}), ErrNilMembership)
	})

	t.Run("refuses a user that fails validation", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		noUsername := newUser("ada")
		noUsername.Username = ""

		err := env.inTx(t, func(tx database.Tx) error {
			return store.CreateUser(t.Context(), tx, noUsername.Scope, noUsername)
		})
		must.Error(t, err)

		noScope := newUser("grace")
		noScope.Scope = tenancy.Scope{}

		err = env.inTx(t, func(tx database.Tx) error {
			return store.CreateUser(t.Context(), tx, noScope.Scope, noScope)
		})
		must.ErrorIs(t, err, tenancy.ErrNoScope)

		badEmail := newUser("grace")
		badEmail.EmailAddress = "Grace <grace@example.com>"

		err = env.inTx(t, func(tx database.Tx) error {
			return store.CreateUser(t.Context(), tx, badEmail.Scope, badEmail)
		})
		// ozzo collects field errors into a map that does not unwrap, so the
		// sentinel is asserted against the rendered message here and against the
		// error chain in the value-level test.
		must.ErrorContains(t, err, "is not a bare address")
	})

	t.Run("creates an account and reads it back", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		read, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.EqOp(t, "Acme", read.Name)
		test.EqOp(t, owner.ID, read.OwnerUserID)
		test.EqOp(t, BillingUnpaid, read.BillingStatus)
		test.True(t, read.BillingAddress.Zero())
	})

	t.Run("refuses an ownerless account", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		orphan := newAccount("Acme", "")

		err := env.inTx(t, func(tx database.Tx) error {
			return store.CreateAccount(t.Context(), tx, orphan.Scope, orphan)
		})
		must.Error(t, err)
	})

	t.Run("makes the first membership the default", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		first := seedAccountFor(t, env, store, owner, "First")

		membership, err := store.GetMembership(t.Context(), env.reader(), testScope, owner.ID, first.ID)
		must.NoError(t, err)

		// The caller said nothing about the default, and a user with
		// memberships and none is a user with nowhere to land.
		test.True(t, membership.DefaultAccount)
		test.Eq(t, []string{"account_admin"}, membership.Roles)

		second := seedAccountFor(t, env, store, owner, "Second")

		later, err := store.GetMembership(t.Context(), env.reader(), testScope, owner.ID, second.ID)
		must.NoError(t, err)
		test.False(t, later.DefaultAccount)
	})

	t.Run("stamps a new membership with the database's creation time", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")
		member := seedUser(t, env, store, newUser("brian"))

		membership := &Membership{
			Scope:            testScope,
			BelongsToUser:    member.ID,
			BelongsToAccount: account.ID,
			Roles:            []string{"account_member"},
		}

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			return store.CreateMembership(t.Context(), tx, membership.Scope, membership)
		}))

		// created_at is database-owned on this table as on every other, so the
		// value the caller is left holding has to be the one the row carries —
		// not the zero time, and not this process's clock.
		test.False(t, membership.CreatedAt.IsZero())

		stored, err := store.GetMembership(t.Context(), env.reader(), testScope, member.ID, account.ID)
		must.NoError(t, err)
		test.EqOp(t, stored.CreatedAt, membership.CreatedAt)
	})

	t.Run("revives an archived membership rather than duplicating it", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")
		member := seedUserInto(t, env, store, newUser("brian"), account.ID)

		original, err := store.GetMembership(t.Context(), env.reader(), testScope, member.ID, account.ID)
		must.NoError(t, err)

		must.NoError(t, env.removeMembership(t, store, testScope, member.ID, account.ID))

		// Rejoining. The pair is unique across live and archived rows, so this
		// has to revive rather than insert — and it keeps the ID it was created
		// with, which is what the roles are written against.
		rejoined := &Membership{
			Scope:            testScope,
			BelongsToUser:    member.ID,
			BelongsToAccount: account.ID,
			Roles:            []string{"account_admin"},
		}

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			return store.CreateMembership(t.Context(), tx, rejoined.Scope, rejoined)
		}))

		// The struct the caller is holding carries the row that is actually
		// there, not the one it asked for: the upsert converged on the pair, so
		// the ID it generated and the creation time it never sent are both read
		// back off the row.
		test.EqOp(t, original.ID, rejoined.ID)
		test.EqOp(t, original.CreatedAt, rejoined.CreatedAt)

		revived, err := store.GetMembership(t.Context(), env.reader(), testScope, member.ID, account.ID)
		must.NoError(t, err)
		test.EqOp(t, original.ID, revived.ID)
		test.EqOp(t, original.CreatedAt, revived.CreatedAt)
		test.Eq(t, []string{"account_admin"}, revived.Roles)

		roster, err := store.ListAccountMembers(t.Context(), env.reader(), testScope, account.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 2, roster.Data)
	})

	t.Run("refuses a membership whose endpoints are in another directory", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		neighbor := newUser("eve")
		neighbor.Scope = otherScope
		seedUser(t, env, store, neighbor)

		// The user exists and the foreign key is satisfied. The directory is
		// the part that does not match, and it is the part that decides whose
		// roster they appear on.
		err := env.inTx(t, func(tx database.Tx) error {
			return store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    neighbor.ID,
				BelongsToAccount: account.ID,
				Roles:            []string{"account_member"},
			})
		})
		must.ErrorIs(t, err, ErrUserNotFound)

		// The other endpoint, and the other sentinel — each names the endpoint
		// this scope cannot see, and neither says it exists elsewhere.
		neighborAccount := newAccount("Neighbor", neighbor.ID)
		neighborAccount.Scope = otherScope

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			return store.CreateAccount(t.Context(), tx, neighborAccount.Scope, neighborAccount)
		}))

		err = env.inTx(t, func(tx database.Tx) error {
			return store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    owner.ID,
				BelongsToAccount: neighborAccount.ID,
				Roles:            []string{"account_member"},
			})
		})
		must.ErrorIs(t, err, ErrAccountNotFound)

		// A membership naming its own scope honestly is still written: this is
		// a congruence check, not a ban on the neighbor having a directory.
		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			return store.CreateMembership(t.Context(), tx, otherScope, &Membership{
				BelongsToUser:    neighbor.ID,
				BelongsToAccount: neighborAccount.ID,
				Roles:            []string{"account_admin"},
			})
		}))

		members, err := store.ListAccountMembers(t.Context(), env.reader(), testScope, account.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, members.Data)
		test.EqOp(t, owner.ID, members.Data[0].User.ID)
	})

	t.Run("refuses a membership that carries no roles", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")
		member := seedUser(t, env, store, newUser("brian"))

		// A user who belongs to an account and may do nothing in it reads at
		// runtime as an authorization bug rather than as a missing field.
		err := env.inTx(t, func(tx database.Tx) error {
			return store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    member.ID,
				BelongsToAccount: account.ID,
			})
		})
		must.Error(t, err)
	})
}

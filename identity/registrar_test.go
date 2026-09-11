package identity

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/identifiers"
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

	t.Run("each create answers with the row and leaves the argument alone", func(t *testing.T) {
		t.Parallel()

		// The whole of what #572 settled for this package, on the three writes
		// that used to deliver it by writing onto the caller's value. What comes
		// back is the row: the defaults the columns supplied, the creation time
		// the database stamped, and — for the user — the service roles the same
		// transaction wrote a statement earlier.
		store := env.newStore(t)

		user := newUser("ada")
		user.ID = ""
		user.AccountStatus = ""
		user.ServiceRoles = []string{"service_user"}

		registered, err := env.createUser(t, store, testScope, user)
		must.NoError(t, err)

		test.NotEq(t, "", registered.ID)
		test.False(t, registered.CreatedAt.IsZero())
		test.EqOp(t, StatusUnverified, registered.AccountStatus)
		test.Eq(t, []string{"service_user"}, registered.ServiceRoles)

		// And none of it landed on the caller's value.
		test.EqOp(t, "", user.ID)
		test.EqOp(t, AccountStatus(""), user.AccountStatus)
		test.True(t, user.CreatedAt.IsZero())

		account := newAccount("Acme", registered.ID)
		account.ID = ""
		account.BillingStatus = ""

		created, err := env.createAccount(t, store, testScope, account)
		must.NoError(t, err)

		test.NotEq(t, "", created.ID)
		test.False(t, created.CreatedAt.IsZero())
		test.EqOp(t, BillingUnpaid, created.BillingStatus)
		test.EqOp(t, "", account.ID)
		test.True(t, account.CreatedAt.IsZero())

		membership := &Membership{
			BelongsToUser:    registered.ID,
			BelongsToAccount: created.ID,
			Roles:            []string{"account_admin"},
		}

		joined, err := env.createMembership(t, store, testScope, membership)
		must.NoError(t, err)

		test.NotEq(t, "", joined.ID)
		test.False(t, joined.CreatedAt.IsZero())
		test.Eq(t, []string{"account_admin"}, joined.Roles)

		// The first membership a user holds anywhere is their default whatever
		// the value said, and the row is where that shows.
		test.True(t, joined.DefaultAccount)
		test.False(t, membership.DefaultAccount)
		test.EqOp(t, "", membership.ID)

		// Each row is the one a later read returns.
		read, err := store.GetUser(t.Context(), env.reader(), testScope, registered.ID)
		must.NoError(t, err)
		test.EqOp(t, registered.CreatedAt, read.CreatedAt)
		test.Eq(t, registered.ServiceRoles, read.ServiceRoles)

		storedAccount, err := store.GetAccount(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, created.CreatedAt, storedAccount.CreatedAt)

		storedMembership, err := store.GetMembership(t.Context(), env.reader(), testScope, registered.ID, created.ID)
		must.NoError(t, err)
		test.EqOp(t, joined.ID, storedMembership.ID)
	})

	t.Run("a refused create answers with no row", func(t *testing.T) {
		t.Parallel()

		// The row comes back only beside a nil error, which is what lets a
		// caller read the answer without checking twice.
		store := env.newStore(t)
		seedUser(t, env, store, newUser("ada"))

		taken := newUser("ada")
		taken.EmailAddress = "different@example.com"

		created, err := env.createUser(t, store, testScope, taken)
		must.ErrorIs(t, err, ErrUsernameTaken)
		test.Nil(t, created)

		orphan, err := env.createMembership(t, store, testScope, &Membership{
			BelongsToUser:    identifiers.New(),
			BelongsToAccount: identifiers.New(),
			Roles:            []string{"account_member"},
		})
		must.ErrorIs(t, err, ErrUserNotFound)
		test.Nil(t, orphan)
	})

	t.Run("generates an ID when none is given", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		user := newUser("grace")
		user.ID = ""

		created := seedUser(t, env, store, user)

		// The generated id is on what the write answered with, and the value
		// the caller handed over still names none.
		test.NotEq(t, "", created.ID)
		test.EqOp(t, "", user.ID)
	})

	t.Run("refuses a taken username", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		seedUser(t, env, store, newUser("ada"))

		second := newUser("ada")
		second.EmailAddress = "different@example.com"

		err := env.inTx(t, func(tx database.Tx) error {
			_, err := store.CreateUser(t.Context(), tx, second.Scope, second)

			return err
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
			_, err := store.CreateUser(t.Context(), tx, second.Scope, second)

			return err
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

		must.NoError(t, env.archiveUserErr(t, store, testScope, ada.ID))

		second := newUser("ada")
		second.EmailAddress = "different@example.com"

		err := env.inTx(t, func(tx database.Tx) error {
			_, err := store.CreateUser(t.Context(), tx, second.Scope, second)

			return err
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

		created, err := store.CreateUser(t.Context(), nil, testScope, newUser("ada"))
		must.ErrorIs(t, err, ErrNilExecutor)
		test.Nil(t, created)
	})

	t.Run("refuses a nil value on each of the three writes", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// Each guard sits behind the executor check, so reaching it needs a
		// live transaction — which is also the shape a registration flow that
		// dropped a value on the floor arrives in.
		must.ErrorIs(t, env.inTx(t, func(tx database.Tx) error {
			_, err := store.CreateUser(t.Context(), tx, testScope, nil)

			return err
		}), ErrNilUser)

		must.ErrorIs(t, env.inTx(t, func(tx database.Tx) error {
			_, err := store.CreateAccount(t.Context(), tx, testScope, nil)

			return err
		}), ErrNilAccount)

		must.ErrorIs(t, env.inTx(t, func(tx database.Tx) error {
			_, err := store.CreateMembership(t.Context(), tx, testScope, nil)

			return err
		}), ErrNilMembership)
	})

	t.Run("refuses a user that fails validation", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		noUsername := newUser("ada")
		noUsername.Username = ""

		err := env.inTx(t, func(tx database.Tx) error {
			_, err := store.CreateUser(t.Context(), tx, noUsername.Scope, noUsername)

			return err
		})
		must.Error(t, err)

		noScope := newUser("grace")
		noScope.Scope = tenancy.Scope{}

		err = env.inTx(t, func(tx database.Tx) error {
			_, createErr := store.CreateUser(t.Context(), tx, noScope.Scope, noScope)

			return createErr
		})
		must.ErrorIs(t, err, tenancy.ErrNoScope)

		badEmail := newUser("grace")
		badEmail.EmailAddress = "Grace <grace@example.com>"

		err = env.inTx(t, func(tx database.Tx) error {
			_, createErr := store.CreateUser(t.Context(), tx, badEmail.Scope, badEmail)

			return createErr
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
			_, err := store.CreateAccount(t.Context(), tx, orphan.Scope, orphan)

			return err
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

		written, err := env.createMembership(t, store, membership.Scope, membership)
		must.NoError(t, err)

		// created_at is database-owned on this table as on every other, so the
		// value the write answers with has to be the one the row carries — not
		// the zero time, and not this process's clock. The caller's own struct
		// is not written to, so it still holds the zero time.
		test.False(t, written.CreatedAt.IsZero())
		test.True(t, membership.CreatedAt.IsZero())

		stored, err := store.GetMembership(t.Context(), env.reader(), testScope, member.ID, account.ID)
		must.NoError(t, err)
		test.EqOp(t, stored.CreatedAt, written.CreatedAt)
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

		written, err := env.createMembership(t, store, rejoined.Scope, rejoined)
		must.NoError(t, err)

		// What the write answers with is the row that is actually there, not
		// the one it was asked for: the upsert converged on the pair, so the ID
		// it generated and the creation time it never sent are both read back
		// off the row.
		test.EqOp(t, original.ID, written.ID)
		test.EqOp(t, original.CreatedAt, written.CreatedAt)

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
			_, err := store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    neighbor.ID,
				BelongsToAccount: account.ID,
				Roles:            []string{"account_member"},
			})

			return err
		})
		must.ErrorIs(t, err, ErrUserNotFound)

		// The other endpoint, and the other sentinel — each names the endpoint
		// this scope cannot see, and neither says it exists elsewhere.
		neighborAccount := newAccount("Neighbor", neighbor.ID)
		neighborAccount.Scope = otherScope

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			_, createErr := store.CreateAccount(t.Context(), tx, neighborAccount.Scope, neighborAccount)

			return createErr
		}))

		err = env.inTx(t, func(tx database.Tx) error {
			_, createErr := store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    owner.ID,
				BelongsToAccount: neighborAccount.ID,
				Roles:            []string{"account_member"},
			})

			return createErr
		})
		must.ErrorIs(t, err, ErrAccountNotFound)

		// A membership naming its own scope honestly is still written: this is
		// a congruence check, not a ban on the neighbor having a directory.
		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			_, err = store.CreateMembership(t.Context(), tx, otherScope, &Membership{
				BelongsToUser:    neighbor.ID,
				BelongsToAccount: neighborAccount.ID,
				Roles:            []string{"account_admin"},
			})

			return err
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
			_, err := store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    member.ID,
				BelongsToAccount: account.ID,
			})

			return err
		})
		must.Error(t, err)
	})
}

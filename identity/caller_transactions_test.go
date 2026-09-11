package identity

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errCompanionWrite stands in for whatever a consumer writes beside an identity
// row — an audit entry, an outbox event — and refuses. It is the reason every
// write here takes the caller's transaction rather than opening one.
var errCompanionWrite = platformerrors.New("the companion write refused")

// runCallerTransactionSuite covers the property the whole store is shaped
// around: the transaction belongs to the caller, so what an identity row commits
// with is theirs to decide, and so is whether it commits at all.
//
// It is a suite of its own rather than cases spread through the other nine,
// because the other nine are about what each write does and this one is about
// where it happens. Every case here runs on all three dialects.
func runCallerTransactionSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a write and a later read inside one transaction observe each other", func(t *testing.T) {
		t.Parallel()

		// The read taking the wider executor is what this is about. A
		// registration that could not read back the user it just wrote would
		// have to carry every value forward by hand, and the service layer above
		// this store is written as a sequence of writes and reads in one
		// transaction.
		store := env.newStore(t)

		user := newUser("ada")
		account := newAccount("Ada's account", user.ID)

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if err := store.CreateUser(t.Context(), tx, testScope, user); err != nil {
				return err
			}

			// Read on the transaction, before anything has committed.
			read, err := store.GetUser(t.Context(), tx, testScope, user.ID)
			if err != nil {
				return err
			}

			test.EqOp(t, "ada", read.Username)

			if err = store.CreateAccount(t.Context(), tx, testScope, account); err != nil {
				return err
			}

			if err = store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    user.ID,
				BelongsToAccount: account.ID,
				Roles:            []string{"account_admin"},
			}); err != nil {
				return err
			}

			// And the principal, which is four statements over three tables the
			// same transaction has just written.
			principal, err := store.GetPrincipal(t.Context(), tx, testScope, user.ID, "")
			if err != nil {
				return err
			}

			test.EqOp(t, account.ID, principal.ActiveAccountID)

			return nil
		}))

		// The reader is only consulted once the transaction is over: this suite
		// runs against a single connection, and a read on the client while a
		// transaction holds it would wait for a commit that is waiting for it.
		principal, err := store.GetPrincipal(t.Context(), env.reader(), testScope, user.ID, "")
		must.NoError(t, err)
		test.EqOp(t, account.ID, principal.ActiveAccountID)
	})

	t.Run("a uniqueness check sees the transaction's own write", func(t *testing.T) {
		t.Parallel()

		// The check runs on the executor the write runs on, so a handle taken a
		// statement ago in this transaction reads as taken. On a handle of the
		// store's own it would read as free, and the second registration would
		// reach the unique index and come back as a driver's constraint
		// violation rather than as this sentinel.
		store := env.newStore(t)

		err := env.inTx(t, func(tx database.Tx) error {
			if createErr := store.CreateUser(t.Context(), tx, testScope, newUser("ada")); createErr != nil {
				return createErr
			}

			second := newUser("ada")
			second.EmailAddress = "different@example.com"

			return store.CreateUser(t.Context(), tx, testScope, second)
		})
		must.ErrorIs(t, err, ErrUsernameTaken)
	})

	t.Run("a refused companion takes the whole registration back", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		user := newUser("ada")
		account := newAccount("Ada's account", user.ID)

		err := env.inTx(t, func(tx database.Tx) error {
			if txErr := store.CreateUser(t.Context(), tx, testScope, user); txErr != nil {
				return txErr
			}

			if txErr := store.CreateAccount(t.Context(), tx, testScope, account); txErr != nil {
				return txErr
			}

			if txErr := store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    user.ID,
				BelongsToAccount: account.ID,
				Roles:            []string{"account_admin"},
			}); txErr != nil {
				return txErr
			}

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		// The ids and the creation times were written onto the values on the way
		// through, and nothing undoes that. What rolled back is the rows.
		test.NotEqOp(t, "", user.ID)
		test.False(t, user.CreatedAt.IsZero())

		_, err = store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		test.ErrorIs(t, err, ErrUserNotFound)

		_, err = store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		test.ErrorIs(t, err, ErrAccountNotFound)
	})

	t.Run("an ownership transfer's two membership writes roll back together", func(t *testing.T) {
		t.Parallel()

		// The sharpest instance of what the auto-transaction form could not
		// express. A transfer to somebody who was not a member mints them one
		// and moves owner_user_id, and while the store owned the transaction
		// those two were reachable together and in no other combination — a
		// caller could not put anything of their own beside them, and could not
		// take them back.
		store := env.newStore(t)

		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")
		stranger := seedUser(t, env, store, newUser("grace"))

		err := env.inTx(t, func(tx database.Tx) error {
			if txErr := store.TransferAccountOwnership(
				t.Context(), tx, testScope, account.ID, stranger.ID); txErr != nil {
				return txErr
			}

			// Both halves are visible from inside, which is what makes the
			// assertions below about a rollback rather than about a transfer
			// that never ran.
			moved, readErr := store.GetAccount(t.Context(), tx, testScope, account.ID)
			if readErr != nil {
				return readErr
			}

			test.EqOp(t, stranger.ID, moved.OwnerUserID)

			if _, readErr = store.GetMembership(t.Context(), tx, testScope, stranger.ID, account.ID); readErr != nil {
				return readErr
			}

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		unmoved, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.EqOp(t, owner.ID, unmoved.OwnerUserID)

		_, err = store.GetMembership(t.Context(), env.reader(), testScope, stranger.ID, account.ID)
		test.ErrorIs(t, err, ErrMembershipNotFound)
	})

	t.Run("an archival's row and its memberships roll back together", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")
		member := seedUserInto(t, env, store, newUser("grace"), account.ID)

		err := env.inTx(t, func(tx database.Tx) error {
			if txErr := store.ArchiveAccount(t.Context(), tx, testScope, account.ID); txErr != nil {
				return txErr
			}

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		// The account is live, and so is the membership the archival would have
		// ended — the fan-out and the row it belongs to are one fact.
		_, err = store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)

		membership, err := store.GetMembership(t.Context(), env.reader(), testScope, member.ID, account.ID)
		must.NoError(t, err)
		test.EqOp(t, account.ID, membership.BelongsToAccount)
	})

	t.Run("every method refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		// A nil executor is a wiring mistake, and every one of the forty-three
		// says so by name rather than dereferencing three frames down. The
		// closures are a struct rather than an interface so that a method
		// dropped from this list is a compile error in the case that named it.
		store := env.newStore(t)

		calls := []struct {
			run  func() error
			name string
		}{
			{name: "CreateUser", run: func() error {
				return store.CreateUser(t.Context(), nil, testScope, newUser("ada"))
			}},
			{name: "CreateAccount", run: func() error {
				return store.CreateAccount(t.Context(), nil, testScope, newAccount("a", "u"))
			}},
			{name: "CreateMembership", run: func() error {
				return store.CreateMembership(t.Context(), nil, testScope, &Membership{})
			}},
			{name: "GetUserByEmailVerificationToken", run: func() error {
				_, err := store.GetUserByEmailVerificationToken(t.Context(), nil, testScope, "tok")

				return err
			}},
			{name: "UpdateUserPassword", run: func() error {
				return store.UpdateUserPassword(t.Context(), nil, testScope, "u", "hash")
			}},
			{name: "SetUserRequiresPasswordChange", run: func() error {
				return store.SetUserRequiresPasswordChange(t.Context(), nil, testScope, "u", true)
			}},
			{name: "UpdateUserTwoFactorSecret", run: func() error {
				return store.UpdateUserTwoFactorSecret(t.Context(), nil, testScope, "u", "secret")
			}},
			{name: "MarkUserTwoFactorSecretVerified", run: func() error {
				return store.MarkUserTwoFactorSecretVerified(t.Context(), nil, testScope, "u")
			}},
			{name: "SetUserEmailAddressVerificationToken", run: func() error {
				return store.SetUserEmailAddressVerificationToken(t.Context(), nil, testScope, "u", "tok")
			}},
			{name: "MarkUserEmailAddressVerified", run: func() error {
				return store.MarkUserEmailAddressVerified(t.Context(), nil, testScope, "u", "tok")
			}},
			{name: "MarkUserEmailAddressUnverified", run: func() error {
				return store.MarkUserEmailAddressUnverified(t.Context(), nil, testScope, "u")
			}},
			{name: "GetUserByUsername", run: func() error {
				_, err := store.GetUserByUsername(t.Context(), nil, testScope, "ada")

				return err
			}},
			{name: "GetUserByEmailAddress", run: func() error {
				_, err := store.GetUserByEmailAddress(t.Context(), nil, testScope, "ada@example.com")

				return err
			}},
			{name: "GetPrincipal", run: func() error {
				_, err := store.GetPrincipal(t.Context(), nil, testScope, "u", "")

				return err
			}},
			{name: "GetUser", run: func() error {
				_, err := store.GetUser(t.Context(), nil, testScope, "u")

				return err
			}},
			{name: "ListUsers", run: func() error {
				_, err := store.ListUsers(t.Context(), nil, testScope, nil)

				return err
			}},
			{name: "ListUsersByIDs", run: func() error {
				_, err := store.ListUsersByIDs(t.Context(), nil, testScope, []string{"u"})

				return err
			}},
			{name: "SearchUsersByUsername", run: func() error {
				_, err := store.SearchUsersByUsername(t.Context(), nil, testScope, "a", nil)

				return err
			}},
			{name: "GetAccount", run: func() error {
				_, err := store.GetAccount(t.Context(), nil, testScope, "a")

				return err
			}},
			{name: "ListAccounts", run: func() error {
				_, err := store.ListAccounts(t.Context(), nil, testScope, nil)

				return err
			}},
			{name: "ListAccountsForUser", run: func() error {
				_, err := store.ListAccountsForUser(t.Context(), nil, testScope, "u", nil)

				return err
			}},
			{name: "GetMembership", run: func() error {
				_, err := store.GetMembership(t.Context(), nil, testScope, "u", "a")

				return err
			}},
			{name: "ListMembershipsForUser", run: func() error {
				_, err := store.ListMembershipsForUser(t.Context(), nil, testScope, "u")

				return err
			}},
			{name: "ListAccountMembers", run: func() error {
				_, err := store.ListAccountMembers(t.Context(), nil, testScope, "a", nil)

				return err
			}},
			{name: "UpdateUser", run: func() error {
				return store.UpdateUser(t.Context(), nil, testScope, newUser("ada"))
			}},
			{name: "UpdateAccount", run: func() error {
				return store.UpdateAccount(t.Context(), nil, testScope, newAccount("a", "u"))
			}},
			{name: "RecordAgreement", run: func() error {
				return store.RecordAgreement(t.Context(), nil, testScope, "u", TermsOfService)
			}},
			{name: "SetMembershipRoles", run: func() error {
				return store.SetMembershipRoles(t.Context(), nil, testScope, "u", "a", []string{"r"})
			}},
			{name: "SetDefaultAccount", run: func() error {
				return store.SetDefaultAccount(t.Context(), nil, testScope, "u", "a")
			}},
			{name: "TransferAccountOwnership", run: func() error {
				return store.TransferAccountOwnership(t.Context(), nil, testScope, "a", "u")
			}},
			{name: "RemoveMembership", run: func() error {
				return store.RemoveMembership(t.Context(), nil, testScope, "u", "a")
			}},
			{name: "UpdateUserAccountStatus", run: func() error {
				return store.UpdateUserAccountStatus(t.Context(), nil, testScope, "u", StatusBanned, "")
			}},
			{name: "SetUserServiceRoles", run: func() error {
				return store.SetUserServiceRoles(t.Context(), nil, testScope, "u", []string{"r"})
			}},
			{name: "ArchiveUser", run: func() error {
				return store.ArchiveUser(t.Context(), nil, testScope, "u")
			}},
			{name: "EraseUser", run: func() error {
				_, err := store.EraseUser(t.Context(), nil, testScope, "u")

				return err
			}},
			{name: "ArchiveAccount", run: func() error {
				return store.ArchiveAccount(t.Context(), nil, testScope, "a")
			}},
			{name: "RecordAccountSubscription", run: func() error {
				return store.RecordAccountSubscription(t.Context(), nil, testScope, "a", BillingPaid, "plan")
			}},
			{name: "RecordAccountSubscriptionEnded", run: func() error {
				return store.RecordAccountSubscriptionEnded(t.Context(), nil, testScope, "a", BillingUnpaid)
			}},
			{name: "SetAccountBillingStatus", run: func() error {
				return store.SetAccountBillingStatus(t.Context(), nil, testScope, "a", BillingUnpaid)
			}},
			{name: "SetAccountPaymentProcessorCustomerID", run: func() error {
				return store.SetAccountPaymentProcessorCustomerID(t.Context(), nil, testScope, "a", "cus_1")
			}},
			{name: "MarkAccountBillingSynced", run: func() error {
				return store.MarkAccountBillingSynced(t.Context(), nil, testScope, "a")
			}},
			{name: "CreateInvitation", run: func() error {
				return store.CreateInvitation(t.Context(), nil, testScope, newInvitation(
					newUser("ada"), "a", "grace@example.com", identifiers.New(), time.Now().Add(time.Hour)))
			}},
			{name: "GetInvitation", run: func() error {
				_, err := store.GetInvitation(t.Context(), nil, testScope, "i")

				return err
			}},
			{name: "GetInvitationByToken", run: func() error {
				_, err := store.GetInvitationByToken(t.Context(), nil, testScope, "i", "tok")

				return err
			}},
			{name: "ListInvitationsFromUser", run: func() error {
				_, err := store.ListInvitationsFromUser(t.Context(), nil, testScope, "u", nil)

				return err
			}},
			{name: "ListInvitationsForEmailAddress", run: func() error {
				_, err := store.ListInvitationsForEmailAddress(t.Context(), nil, testScope, "g@example.com", nil)

				return err
			}},
			{name: "AcceptInvitation", run: func() error {
				_, err := store.AcceptInvitation(t.Context(), nil, testScope, "i", "tok", "u", "")

				return err
			}},
			{name: "SetInvitationStatus", run: func() error {
				return store.SetInvitationStatus(t.Context(), nil, testScope, "i", InvitationRejected, "")
			}},
		}

		// Every method on Store, and the count says so: a method added without a
		// case here fails on the number rather than going untested.
		test.EqOp(t, storeMethodCount(), len(calls))

		for _, call := range calls {
			test.ErrorIs(t, call.run(), ErrNilExecutor, test.Sprintf("%s", call.name))
		}
	})

	t.Run("an entity naming another directory is refused, and one naming none adopts", func(t *testing.T) {
		t.Parallel()

		// The scope the call names is what the statement binds, so an entity
		// naming a different one is a caller holding one directory's value and
		// writing it into another. That is a stale value or a mix-up, and
		// neither is a thing to guess at — see adoptScope.
		store := env.newStore(t)

		stray := newUser("ada")
		stray.Scope = otherScope

		must.ErrorIs(t, env.createUser(t, store, testScope, stray), ErrScopeMismatch)

		// Naming none adopts the argument, which is what a single-directory
		// deployment writes.
		adopted := newUser("grace")
		adopted.Scope = tenancy.Scope{}

		must.NoError(t, env.createUser(t, store, testScope, adopted))
		test.EqOp(t, testScope, adopted.Scope)

		read, err := store.GetUser(t.Context(), env.reader(), testScope, adopted.ID)
		must.NoError(t, err)
		test.EqOp(t, testScope, read.Scope)

		// And the same reading on the other five entity writes.
		strayAccount := newAccount("Acme", adopted.ID)
		strayAccount.Scope = otherScope

		must.ErrorIs(t, env.createAccount(t, store, testScope, strayAccount), ErrScopeMismatch)

		strayMembership := &Membership{
			Scope:            otherScope,
			BelongsToUser:    adopted.ID,
			BelongsToAccount: "whatever",
		}

		must.ErrorIs(t, env.createMembership(t, store, testScope, strayMembership), ErrScopeMismatch)

		account := seedAccountFor(t, env, store, adopted, "Acme")

		moved := *adopted
		moved.Scope = otherScope

		must.ErrorIs(t, env.updateUser(t, store, testScope, &moved), ErrScopeMismatch)

		movedAccount := *account
		movedAccount.Scope = otherScope

		must.ErrorIs(t, env.updateAccount(t, store, testScope, &movedAccount), ErrScopeMismatch)

		strayInvitation := newInvitation(
			adopted, account.ID, "grace@example.com", identifiers.New(), time.Now().Add(time.Hour))
		strayInvitation.Scope = otherScope

		must.ErrorIs(t, env.createInvitation(t, store, testScope, strayInvitation), ErrScopeMismatch)
	})
}

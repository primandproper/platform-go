package identity

import (
	"testing"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runAdminWriterSuite covers the operator's half, whose exposure through an
// ordinary request handler is a privilege escalation.
func runAdminWriterSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("moves a user between statuses", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		user := newUser("ada")
		user.AccountStatus = StatusUnverified
		seedUser(t, env, store, user)

		must.NoError(t, env.updateUserAccountStatus(t, store, testScope, user.ID, StatusBanned, "spam"))

		banned, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusBanned, banned.AccountStatus)
		test.EqOp(t, "spam", banned.AccountStatusExplanation)
		test.False(t, banned.AccountStatus.AdmitsSignIn())

		err = env.updateUserAccountStatus(t, store, testScope, user.ID, AccountStatus("nonsense"), "")
		must.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)
	})

	t.Run("carries service roles on every read", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		user := newUser("ada")
		user.ServiceRoles = []string{"service_user", "service_admin"}
		seedUser(t, env, store, user)

		byID, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.Eq(t, []string{"service_admin", "service_user"}, byID.ServiceRoles)

		// The same set on the sign-in read, so an admin check does not need a
		// second query and cannot be skipped by forgetting one.
		byName, err := store.GetUserByUsername(t.Context(), env.reader(), testScope, "ada")
		must.NoError(t, err)
		test.Eq(t, []string{"service_admin", "service_user"}, byName.ServiceRoles)

		must.NoError(t, env.setUserServiceRoles(t, store, testScope, user.ID, []string{"service_user"}))

		revoked, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.Eq(t, []string{"service_user"}, revoked.ServiceRoles)

		// Replacing with nothing is how operator access is withdrawn.
		must.NoError(t, env.setUserServiceRoles(t, store, testScope, user.ID, nil))

		cleared, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.SliceEmpty(t, cleared.ServiceRoles)
	})

	t.Run("refuses service roles for a user in another directory", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		err := env.setUserServiceRoles(t, store, otherScope, user.ID, []string{"service_admin"})
		must.ErrorIs(t, err, ErrUserNotFound)
	})

	t.Run("refuses an empty service role name", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		user := newUser("ada")
		user.ServiceRoles = []string{"service_admin"}
		seedUser(t, env, store, user)

		// An empty role name is a row nothing matches on, so the grant reads as
		// applied and resolves to nothing at the next permission check.
		must.ErrorIs(t,
			env.setUserServiceRoles(t, store, testScope, user.ID, []string{"service_user", ""}),
			platformerrors.ErrEmptyInputParameter,
		)

		// The refusal is ahead of the write, so the roles they had are intact.
		read, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
		test.Eq(t, []string{"service_admin"}, read.ServiceRoles)
	})

	t.Run("each archival answers with the row it hid", func(t *testing.T) {
		t.Parallel()

		// The last moment anything can say who was removed. Once the write
		// commits, the subject is absent from every read this package has, so
		// the row the archival returns is what a consumer's entry is written
		// from — and it carries the stamp the write just made rather than
		// describing the user as they stood a statement earlier.
		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")
		member := seedUserInto(t, env, store, newUser("brian"), account.ID)

		must.NoError(t, env.setUserServiceRoles(t, store, testScope, member.ID, []string{"service_support"}))

		archived, err := env.archiveUser(t, store, testScope, member.ID)
		must.NoError(t, err)

		test.EqOp(t, member.ID, archived.ID)
		test.EqOp(t, "brian", archived.Username)
		test.True(t, archived.Archived())
		must.NotNil(t, archived.ArchivedAt)

		// A grant is not archived with its owner — the role tables carry no
		// archived column — so the roles the subject held are on the row, and a
		// consumer recording what was withdrawn can name them.
		test.Eq(t, []string{"service_support"}, archived.ServiceRoles)

		// And the read that would have described it is the one that cannot.
		_, err = store.GetUser(t.Context(), env.reader(), testScope, member.ID)
		must.ErrorIs(t, err, ErrUserNotFound)

		// A refusal answers with no row, on both of the two doors this write
		// closes: a subject already archived, and one who still owns an account.
		replayed, err := env.archiveUser(t, store, testScope, member.ID)
		must.ErrorIs(t, err, ErrUserNotFound)
		test.Nil(t, replayed)

		refused, err := env.archiveUser(t, store, testScope, owner.ID)
		must.ErrorIs(t, err, ErrLastAccountOwner)
		test.Nil(t, refused)

		closed, err := env.archiveAccount(t, store, testScope, account.ID)
		must.NoError(t, err)

		test.EqOp(t, account.ID, closed.ID)
		test.EqOp(t, "Acme", closed.Name)
		test.EqOp(t, owner.ID, closed.OwnerUserID)
		test.True(t, closed.Archived())

		gone, err := env.archiveAccount(t, store, testScope, account.ID)
		must.ErrorIs(t, err, ErrAccountNotFound)
		test.Nil(t, gone)
	})

	t.Run("ends memberships when a user is archived", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		member := seedUserInto(t, env, store, newUser("brian"), account.ID)

		must.NoError(t, env.archiveUserErr(t, store, testScope, member.ID))

		// A user archived with live memberships is still on the rosters of the
		// accounts they belonged to, which is what an application discovers when
		// a deleted colleague is still listed.
		_, err := store.GetMembership(t.Context(), env.reader(), testScope, member.ID, account.ID)
		must.ErrorIs(t, err, ErrMembershipNotFound)

		// Archiving twice must not move the timestamp and lose when it first
		// happened.
		must.ErrorIs(t, env.archiveUserErr(t, store, testScope, member.ID), ErrUserNotFound)
	})

	t.Run("ends only the archived user's memberships", func(t *testing.T) {
		t.Parallel()

		// The archival clears the default flag across the user's memberships
		// and then archives them, both keyed on the user. A clear keyed on the
		// account instead would take the flag off everybody else in the
		// accounts they belonged to, which is what this pins.
		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		first := seedAccountFor(t, env, store, owner, "First")
		second := seedAccountFor(t, env, store, owner, "Second")

		leaving := seedUserInto(t, env, store, newUser("brian"), first.ID)
		staying := seedUserInto(t, env, store, newUser("carol"), first.ID)

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			_, err := store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    leaving.ID,
				BelongsToAccount: second.ID,
				Roles:            []string{"account_member"},
			})

			return err
		}))

		must.NoError(t, env.archiveUserErr(t, store, testScope, leaving.ID))

		gone, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, leaving.ID)
		must.NoError(t, err)
		test.SliceEmpty(t, gone)

		// The other member of the account they shared is untouched, flag and
		// all.
		kept, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, staying.ID)
		must.NoError(t, err)
		must.SliceLen(t, 1, kept)
		test.True(t, kept[0].DefaultAccount)

		// So is the owner, who belongs to both accounts.
		owned, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, owner.ID)
		must.NoError(t, err)
		test.SliceLen(t, 2, owned)
	})

	t.Run("refuses to archive an owner out from under their accounts", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		// The same failure RemoveMembership refuses, reached through the other
		// door: an account left live and answering to a user every scoped read
		// now reports as absent.
		err := env.archiveUserErr(t, store, testScope, owner.ID)
		must.ErrorIs(t, err, ErrLastAccountOwner)

		// The refusal names the account that has to move first, which is the
		// only thing the operator needs and the one thing a bare sentinel
		// cannot say.
		test.StrContains(t, err.Error(), account.ID)

		// Nothing was written: the guard shares the transaction with the
		// archive, so a refusal cannot leave the memberships ended behind it.
		read, err := store.GetUser(t.Context(), env.reader(), testScope, owner.ID)
		must.NoError(t, err)
		test.False(t, read.Archived())

		membership, err := store.GetMembership(t.Context(), env.reader(), testScope, owner.ID, account.ID)
		must.NoError(t, err)
		test.False(t, membership.Archived())

		// Transferring is one of the two ways out, and it is the one that keeps
		// the account.
		successor := seedUserInto(t, env, store, newUser("grace"), account.ID)
		must.NoError(t, env.transferAccountOwnership(t, store, testScope, account.ID, successor.ID))
		must.NoError(t, env.archiveUserErr(t, store, testScope, owner.ID))

		// Archiving the account is the other, and it unblocks the new owner —
		// an archived account is not one whose ownership has to move.
		must.ErrorIs(t, env.archiveUserErr(t, store, testScope, successor.ID), ErrLastAccountOwner)
		must.NoError(t, env.archiveAccountErr(t, store, testScope, account.ID))
		must.NoError(t, env.archiveUserErr(t, store, testScope, successor.ID))
	})

	t.Run("refuses to archive an owner named by another directory's account", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		seedAccountFor(t, env, store, owner, "Acme")

		// The guard is scoped like every other read here, so a neighbor
		// directory's accounts neither block an archive nor are consulted by
		// one — and the archive of a user who is not in this directory is still
		// the missing user it always was.
		must.ErrorIs(t, env.archiveUserErr(t, store, otherScope, owner.ID), ErrUserNotFound)
	})

	t.Run("erases a user and everything keyed to them", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")
		member := seedUserInto(t, env, store, newUser("brian"), account.ID)

		var erased int64

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			var err error
			erased, err = store.EraseUser(t.Context(), tx, testScope, member.ID)

			return err
		}))
		test.EqOp(t, int64(1), erased)

		_, err := store.GetUser(t.Context(), env.reader(), testScope, member.ID)
		must.ErrorIs(t, err, ErrUserNotFound)

		// The membership went with the row, through the schema's cascade — an
		// erasure that left one behind would leave the subject's account list
		// intact.
		roster, err := store.ListAccountMembers(t.Context(), env.reader(), testScope, account.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, roster.Data)
		test.EqOp(t, owner.ID, roster.Data[0].BelongsToUser)

		// The handle is free again, which a soft delete deliberately does not do.
		seedUser(t, env, store, newUser("brian"))
	})

	t.Run("erases a user who was archived first", func(t *testing.T) {
		t.Parallel()

		// The order a right-to-be-forgotten request actually runs in: the
		// subject is hidden, and destroyed afterwards. Every other single-row
		// statement in this package requires archived_at IS NULL, so the
		// erasure is the one that must not — and it is now a generated DELETE,
		// where the predicate's absence is a property of the emitted text
		// rather than of a hand-written string.
		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.archiveUserErr(t, store, testScope, user.ID))

		var erased int64

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			var err error
			erased, err = store.EraseUser(t.Context(), tx, testScope, user.ID)

			return err
		}))
		test.EqOp(t, int64(1), erased)
	})

	t.Run("refuses to erase a user in another directory", func(t *testing.T) {
		t.Parallel()

		// The scope is a predicate on the delete rather than a check above it,
		// so a neighbor's erasure matches nothing and reports zero.
		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		var erased int64

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			var err error
			erased, err = store.EraseUser(t.Context(), tx, otherScope, user.ID)

			return err
		}))
		test.EqOp(t, int64(0), erased)

		_, err := store.GetUser(t.Context(), env.reader(), testScope, user.ID)
		must.NoError(t, err)
	})

	t.Run("leaves an erased owner's accounts naming an id that is gone", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		// Erasure is the one path that reaches an ownerless account, and it
		// does so deliberately: it cannot refuse the way ArchiveUser refuses,
		// because a right-to-be-forgotten transaction spans every domain and
		// has to commit.
		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			_, err := store.EraseUser(t.Context(), tx, testScope, owner.ID)

			return err
		}))

		_, err := store.GetUser(t.Context(), env.reader(), testScope, owner.ID)
		must.ErrorIs(t, err, ErrUserNotFound)

		// The account is still live and still names the id, which is the
		// documented post-condition rather than an accident — there is no
		// foreign key to have taken it, and taking it would put the other
		// members offline because one of them exercised a right.
		read, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.False(t, read.Archived())
		test.EqOp(t, owner.ID, read.OwnerUserID)

		// Resolving it is a transfer, and nothing in the store stands in the
		// way of one — the account is reachable by every scoped read.
		successor := seedUser(t, env, store, newUser("grace"))
		must.NoError(t, env.transferAccountOwnership(t, store, testScope, account.ID, successor.ID))

		resolved, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.EqOp(t, successor.ID, resolved.OwnerUserID)
	})

	t.Run("ends memberships when an account is archived", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")
		member := seedUserInto(t, env, store, newUser("brian"), account.ID)

		must.NoError(t, env.archiveAccountErr(t, store, testScope, account.ID))

		// The read by id excludes it, as every read by id now does.
		_, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.ErrorIs(t, err, ErrAccountNotFound)

		// A page admits it when the filter asks for archived rows, which is the
		// read a caller reconciling an invoice against a closed account wants.
		archived, err := store.ListAccounts(t.Context(), env.reader(), testScope, &filtering.QueryFilter{
			IncludeArchived: pointer.To(true),
		})
		must.NoError(t, err)
		must.SliceLen(t, 1, archived.Data)
		test.True(t, archived.Data[0].Archived())

		// It is out of the unfiltered page, though.
		page, err := store.ListAccounts(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, page.Data)

		// Members left live against an archived account keep it in their
		// switcher and keep resolving permissions through it.
		_, err = store.GetMembership(t.Context(), env.reader(), testScope, member.ID, account.ID)
		must.ErrorIs(t, err, ErrMembershipNotFound)

		mine, err := store.ListAccountsForUser(t.Context(), env.reader(), testScope, member.ID, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, mine.Data)

		must.ErrorIs(t, env.archiveAccountErr(t, store, testScope, account.ID), ErrAccountNotFound)
	})

	t.Run("ends only the archived account's memberships", func(t *testing.T) {
		t.Parallel()

		// The account's archival clears the default flag across its
		// memberships and then archives them, both keyed on the account. A
		// clear keyed on the user instead would take the flag off the members'
		// other accounts, leaving them with memberships and nowhere to land.
		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		closing := seedAccountFor(t, env, store, owner, "Closing")
		surviving := seedAccountFor(t, env, store, owner, "Surviving")

		member := seedUserInto(t, env, store, newUser("brian"), closing.ID)

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			_, err := store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    member.ID,
				BelongsToAccount: surviving.ID,
				Roles:            []string{"account_member"},
			})

			return err
		}))

		// The first membership is the default; the second is not.
		before, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, member.ID)
		must.NoError(t, err)
		must.SliceLen(t, 2, before)
		test.EqOp(t, closing.ID, before[0].BelongsToAccount)
		test.True(t, before[0].DefaultAccount)

		must.NoError(t, env.archiveAccountErr(t, store, testScope, closing.ID))

		// The surviving membership is still there, and it is now the default:
		// closing an account is RemoveMembership performed on every member at
		// once, and neither leaves somebody with memberships and nowhere to
		// land.
		after, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, member.ID)
		must.NoError(t, err)
		must.SliceLen(t, 1, after)
		test.EqOp(t, surviving.ID, after[0].BelongsToAccount)
		test.True(t, after[0].DefaultAccount)

		landing, err := store.GetPrincipal(t.Context(), env.reader(), testScope, member.ID, "")
		must.NoError(t, err)
		test.EqOp(t, surviving.ID, landing.ActiveAccountID)

		// The owner belonged to both and defaulted to the one that closed, so
		// they move too — the archival reads every member whose default this
		// was rather than the one it was handed.
		ownerLanding, err := store.GetPrincipal(t.Context(), env.reader(), testScope, owner.ID, "")
		must.NoError(t, err)
		test.EqOp(t, surviving.ID, ownerLanding.ActiveAccountID)
	})

	t.Run("leaves a member of nothing else with no default", func(t *testing.T) {
		t.Parallel()

		// A default is a pointer at a live membership, so a member whose only
		// account closed keeps none: there is nothing to point at, and that is
		// the state ErrNoDefaultAccount describes rather than one the archival
		// invented.
		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		closing := seedAccountFor(t, env, store, owner, "Closing")
		member := seedUserInto(t, env, store, newUser("brian"), closing.ID)

		must.NoError(t, env.archiveAccountErr(t, store, testScope, closing.ID))

		after, err := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, member.ID)
		must.NoError(t, err)
		test.SliceEmpty(t, after)

		_, err = store.GetPrincipal(t.Context(), env.reader(), testScope, member.ID, "")
		must.ErrorIs(t, err, ErrNoDefaultAccount)
	})

	t.Run("moves a member's default off a closing account and not off another's", func(t *testing.T) {
		t.Parallel()

		// Two members of the closing account, each with a second account of
		// their own, and one of them defaulting somewhere else already. The
		// move is per member — the fallback is a different account for each —
		// and it reaches only the members the closure actually strands.
		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		closing := seedAccountFor(t, env, store, owner, "Closing")

		stranded := seedUserInto(t, env, store, newUser("brian"), closing.ID)
		strandedHome := seedAccountFor(t, env, store, stranded, "Brian's")

		settled := seedUserInto(t, env, store, newUser("cleo"), closing.ID)
		settledHome := seedAccountFor(t, env, store, settled, "Cleo's")
		must.NoError(t, env.setDefaultAccount(t, store, testScope, settled.ID, settledHome.ID))

		must.NoError(t, env.archiveAccountErr(t, store, testScope, closing.ID))

		strandedLanding, err := store.GetPrincipal(t.Context(), env.reader(), testScope, stranded.ID, "")
		must.NoError(t, err)
		test.EqOp(t, strandedHome.ID, strandedLanding.ActiveAccountID)

		settledLanding, err := store.GetPrincipal(t.Context(), env.reader(), testScope, settled.ID, "")
		must.NoError(t, err)
		test.EqOp(t, settledHome.ID, settledLanding.ActiveAccountID)

		// One default per member survives the move, which is the invariant
		// SetDefaultAccount states and the archival must not break by writing a
		// second flag.
		for _, userID := range []string{stranded.ID, settled.ID} {
			memberships, listErr := store.ListMembershipsForUser(t.Context(), env.reader(), testScope, userID)
			must.NoError(t, listErr)
			must.SliceLen(t, 1, memberships)
			test.True(t, memberships[0].DefaultAccount)
		}
	})
}

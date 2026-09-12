package waitlists

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errCompanionWrite stands in for the consumer's own write failing beside one
// of this package's — the audit entry that would not go in, the outbox row that
// would not enqueue.
var errCompanionWrite = platformerrors.New("the companion write failed")

// runTransactionSuite is the commit boundary, which is the whole of what this
// store's signatures buy its caller.
//
// Every write takes the caller's transaction and every read takes an executor, so
// what is under test here is not that the statements work — the list, signup and
// withdrawal suites cover that — but which side of a commit each of them lands
// on, and what a read handed the transaction can see. Those are the questions a
// store that opened its own transaction answered for its caller, and answered
// wrong.
func runTransactionSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a write and a read inside one transaction observe each other", func(t *testing.T) {
		t.Parallel()

		// The property the reads were widened for, and the one no auto-committing
		// write could express: inside the transaction the signup is there, and
		// from outside it is not there yet. A read narrowed to the client's
		// reader would have been reading a database that does not hold the row
		// its own caller just wrote.
		store := env.newStore(t)

		list := mustCreateList(t, env, store, testScope, openList("Launch"))

		var joined *Signup

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			var err error
			if joined, err = store.Join(t.Context(), tx, testScope, list.ID,
				&Signup{Contact: "ada@example.com"}); err != nil {
				return err
			}

			read, err := store.GetSignup(t.Context(), tx, testScope, list.ID, joined.ID)
			if err != nil {
				return err
			}

			test.EqOp(t, "ada@example.com", read.Contact)

			page, err := store.ListSignups(t.Context(), tx, testScope, list.ID, nil)
			if err != nil {
				return err
			}

			must.SliceLen(t, 1, page.Data)

			// And the same read, on the client, cannot see it: the transaction
			// has not committed, so this is the other half of the same fact
			// rather than a second one.
			outside, err := store.ListSignups(t.Context(), env.reader(), testScope, list.ID, nil)
			if err != nil {
				return err
			}

			test.SliceEmpty(t, outside.Data)

			return nil
		}))

		// After the commit both executors agree, which is what makes the reading
		// above about visibility rather than about two different rows.
		read, err := store.GetSignup(t.Context(), env.reader(), testScope, list.ID, joined.ID)
		must.NoError(t, err)
		test.EqOp(t, joined.ID, read.ID)
	})

	t.Run("every write commits with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		renamed := mustCreateList(t, env, store, testScope, openList("before"))
		doomed := mustCreateList(t, env, store, testScope, openList("on the way out"))
		converted := mustJoin(t, env, store, testScope, renamed.ID, &Signup{Contact: "converted@example.com"})
		withdrawn := mustJoin(t, env, store, testScope, renamed.ID, &Signup{Contact: "withdrawn@example.com"})
		archived := mustJoin(t, env, store, testScope, renamed.ID, &Signup{Contact: "archived@example.com"})

		var (
			opened  *List
			retired *List
			joined  *Signup
			noted   *Signup
			left    *Signup
			putAway *Signup
		)

		must.NoError(t, env.inTx(t, func(tx database.Tx) (err error) {
			if opened, err = store.CreateList(t.Context(), tx, testScope, openList("opened inside")); err != nil {
				return err
			}

			renamed.Name = "after"
			if _, err = store.UpdateList(t.Context(), tx, testScope, renamed); err != nil {
				return err
			}

			if retired, err = store.ArchiveList(t.Context(), tx, testScope, doomed.ID); err != nil {
				return err
			}

			if joined, err = store.Join(t.Context(), tx, testScope, renamed.ID,
				&Signup{Contact: "joined@example.com", Subject: testSubject}); err != nil {
				return err
			}

			if noted, err = store.UpdateSignupNotes(
				t.Context(), tx, testScope, renamed.ID, joined.ID, "noted"); err != nil {
				return err
			}

			if _, err = store.Invite(t.Context(), tx, testScope, renamed.ID, converted.ID); err != nil {
				return err
			}

			if _, err = store.Convert(t.Context(), tx, testScope, renamed.ID, converted.ID); err != nil {
				return err
			}

			if left, err = store.Withdraw(t.Context(), tx, testScope, renamed.ID, withdrawn.ID); err != nil {
				return err
			}

			putAway, err = store.ArchiveSignup(t.Context(), tx, testScope, renamed.ID, archived.ID)

			return err
		}))

		// The creates read their creation times back through the caller's
		// executor, so the values the caller is handed are the rows this
		// transaction wrote rather than zero times waiting on a commit.
		must.NotNil(t, opened)
		test.NotEqOp(t, "", opened.ID)
		test.False(t, opened.CreatedAt.IsZero())
		must.NotNil(t, joined)
		test.False(t, joined.CreatedAt.IsZero())

		// And so do the seven that answer with a row. Each of these was read on
		// the same transaction the write ran on — the two retirements through a
		// statement written to see a hidden row, the withdrawal in front of the
		// statement rather than behind it — so they describe rows nothing
		// outside this transaction could see when they were read.
		must.NotNil(t, retired)
		test.NotNil(t, retired.ArchivedAt)
		must.NotNil(t, noted)
		test.EqOp(t, "noted", noted.Notes)
		must.NotNil(t, left)
		test.EqOp(t, "withdrawn@example.com", left.Contact)
		must.NotNil(t, putAway)
		test.EqOp(t, "archived@example.com", putAway.Contact)
		test.NotNil(t, putAway.ArchivedAt)

		list, err := store.GetList(t.Context(), env.reader(), testScope, opened.ID)
		must.NoError(t, err)
		test.EqOp(t, "opened inside", list.Name)

		list, err = store.GetList(t.Context(), env.reader(), testScope, renamed.ID)
		must.NoError(t, err)
		test.EqOp(t, "after", list.Name)

		_, err = store.GetList(t.Context(), env.reader(), testScope, doomed.ID)
		must.ErrorIs(t, err, ErrListNotFound)

		signup, err := store.GetSignup(t.Context(), env.reader(), testScope, renamed.ID, joined.ID)
		must.NoError(t, err)
		test.EqOp(t, "noted", signup.Notes)
		test.EqOp(t, StatusWaiting, signup.Status)

		signup, err = store.GetSignup(t.Context(), env.reader(), testScope, renamed.ID, converted.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusConverted, signup.Status)

		signup, err = store.GetSignup(t.Context(), env.reader(), testScope, renamed.ID, withdrawn.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusWithdrawn, signup.Status)
		test.EqOp(t, "", signup.Contact)

		_, err = store.GetSignup(t.Context(), env.reader(), testScope, renamed.ID, archived.ID)
		must.ErrorIs(t, err, ErrSignupNotFound)
	})

	t.Run("a rolled back transaction takes every write with it", func(t *testing.T) {
		t.Parallel()

		// This is the whole point of the signatures, seen from the side that
		// matters: the consumer's companion write fails, and the row goes back
		// with it rather than surviving in a transaction it was never part of.
		store := env.newStore(t)

		kept := mustCreateList(t, env, store, testScope, openList("the original"))
		spared := mustCreateList(t, env, store, testScope, openList("still here"))
		waiting := mustJoin(t, env, store, testScope, kept.ID, &Signup{Contact: "waiting@example.com"})
		staying := mustJoin(t, env, store, testScope, kept.ID,
			&Signup{Contact: "staying@example.com", Notes: "as written"})
		visible := mustJoin(t, env, store, testScope, kept.ID, &Signup{Contact: "visible@example.com"})

		var (
			opened *List
			joined *Signup
		)

		err := env.inTx(t, func(tx database.Tx) (err error) {
			if opened, err = store.CreateList(t.Context(), tx, testScope, openList("never committed")); err != nil {
				return err
			}

			edited := *kept
			edited.Name = "the edit"
			if _, err = store.UpdateList(t.Context(), tx, testScope, &edited); err != nil {
				return err
			}

			if _, err = store.ArchiveList(t.Context(), tx, testScope, spared.ID); err != nil {
				return err
			}

			if joined, err = store.Join(t.Context(), tx, testScope, kept.ID,
				&Signup{Contact: "joined@example.com"}); err != nil {
				return err
			}

			if _, err = store.UpdateSignupNotes(
				t.Context(), tx, testScope, kept.ID, staying.ID, "the edit"); err != nil {
				return err
			}

			if _, err = store.Invite(t.Context(), tx, testScope, kept.ID, waiting.ID); err != nil {
				return err
			}

			if _, err = store.Withdraw(t.Context(), tx, testScope, kept.ID, staying.ID); err != nil {
				return err
			}

			if _, err = store.ArchiveSignup(t.Context(), tx, testScope, kept.ID, visible.ID); err != nil {
				return err
			}

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		// The ids were minted onto the returned values on the way through.
		// Nothing undoes that, and nothing should: what rolled back is the row.
		must.NotNil(t, opened)
		must.NotNil(t, joined)

		_, err = store.GetList(t.Context(), env.reader(), testScope, opened.ID)
		must.ErrorIs(t, err, ErrListNotFound)

		list, err := store.GetList(t.Context(), env.reader(), testScope, kept.ID)
		must.NoError(t, err)
		test.EqOp(t, "the original", list.Name)

		list, err = store.GetList(t.Context(), env.reader(), testScope, spared.ID)
		must.NoError(t, err)
		test.Nil(t, list.ArchivedAt)

		_, err = store.GetSignup(t.Context(), env.reader(), testScope, kept.ID, joined.ID)
		must.ErrorIs(t, err, ErrSignupNotFound)

		signup, err := store.GetSignup(t.Context(), env.reader(), testScope, kept.ID, waiting.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusWaiting, signup.Status)

		signup, err = store.GetSignup(t.Context(), env.reader(), testScope, kept.ID, staying.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusWaiting, signup.Status)
		test.EqOp(t, "staying@example.com", signup.Contact)
		test.EqOp(t, "as written", signup.Notes)

		signup, err = store.GetSignup(t.Context(), env.reader(), testScope, kept.ID, visible.ID)
		must.NoError(t, err)
		test.Nil(t, signup.ArchivedAt)
	})

	t.Run("a signup joins a list opened in the same transaction", func(t *testing.T) {
		t.Parallel()

		// The list read and the suppression check run on the caller's executor
		// rather than on a transaction of the store's own, so a launch opened
		// and seeded in one transaction resolves instead of reporting the list
		// absent.
		store := env.newStore(t)

		var (
			opened *List
			joined *Signup
		)

		must.NoError(t, env.inTx(t, func(tx database.Tx) (err error) {
			if opened, err = store.CreateList(t.Context(), tx, testScope, openList("Launch")); err != nil {
				return err
			}

			joined, err = store.Join(t.Context(), tx, testScope, opened.ID, &Signup{Contact: "ada@example.com"})

			return err
		}))

		signup, err := store.GetSignup(t.Context(), env.reader(), testScope, opened.ID, joined.ID)
		must.NoError(t, err)
		test.EqOp(t, opened.ID, signup.ListID)
	})

	t.Run("the suppression check sees a signup this transaction just wrote", func(t *testing.T) {
		t.Parallel()

		// The refusal made inside the transaction and committed by it, which is
		// the check that a read outside the transaction could not have made.
		store := env.newStore(t)

		list := mustCreateList(t, env, store, testScope, openList("Launch"))

		var second error

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if _, err := store.Join(t.Context(), tx, testScope, list.ID,
				&Signup{Contact: "taken@example.com"}); err != nil {
				return err
			}

			_, second = store.Join(t.Context(), tx, testScope, list.ID, &Signup{Contact: "TAKEN@example.com"})

			return nil
		}))

		must.ErrorIs(t, second, ErrAlreadySignedUp)

		page, err := store.ListSignups(t.Context(), env.reader(), testScope, list.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
	})

	t.Run("a lost transition explains itself against the transaction's own row", func(t *testing.T) {
		t.Parallel()

		// The read on the losing path goes through the executor the write ran
		// on. Anything else would be reading a row this transaction has not
		// committed — and on a one-connection SQLite client, would be waiting
		// on itself for the connection to do it with.
		store := env.newStore(t)

		list := mustCreateList(t, env, store, testScope, openList("Launch"))

		var invitedTwice, convertedFirst, withdrawnTwice error

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			joined, err := store.Join(t.Context(), tx, testScope, list.ID, &Signup{Contact: "ada@example.com"})
			if err != nil {
				return err
			}

			_, convertedFirst = store.Convert(t.Context(), tx, testScope, list.ID, joined.ID)

			if _, err = store.Invite(t.Context(), tx, testScope, list.ID, joined.ID); err != nil {
				return err
			}

			_, invitedTwice = store.Invite(t.Context(), tx, testScope, list.ID, joined.ID)

			if _, err = store.Withdraw(t.Context(), tx, testScope, list.ID, joined.ID); err != nil {
				return err
			}

			_, withdrawnTwice = store.Withdraw(t.Context(), tx, testScope, list.ID, joined.ID)

			return nil
		}))

		must.ErrorIs(t, convertedFirst, ErrWrongStatus)
		must.ErrorIs(t, invitedTwice, ErrWrongStatus)
		must.ErrorIs(t, withdrawnTwice, ErrAlreadyWithdrawn)
	})

	t.Run("every method refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		// Every one of the seventeen, not a representative one. There is no
		// connection of the store's own to fall back to, so a method that did
		// anything but refuse would be reaching for something that is not there
		// — and for a write, would be writing outside the transaction its caller
		// believes it is in.
		store := env.newStore(t)

		_, err := store.CreateList(t.Context(), nil, testScope, openList("Launch"))
		must.ErrorIs(t, err, ErrNilExecutor)
		_, err = store.UpdateList(t.Context(), nil, testScope, openList("Launch"))
		must.ErrorIs(t, err, ErrNilExecutor)
		_, err = store.ArchiveList(t.Context(), nil, testScope, "wl_1")
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetList(t.Context(), nil, testScope, "wl_1")
		must.ErrorIs(t, err, ErrNilExecutor)
		_, err = store.ListLists(t.Context(), nil, testScope, nil)
		must.ErrorIs(t, err, ErrNilExecutor)
		_, err = store.ListOpenLists(t.Context(), nil, testScope, nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.Join(t.Context(), nil, testScope, "wl_1", &Signup{Contact: "ada@example.com"})
		must.ErrorIs(t, err, ErrNilExecutor)
		_, err = store.UpdateSignupNotes(t.Context(), nil, testScope, "wl_1", "sg_1", "note")
		must.ErrorIs(t, err, ErrNilExecutor)
		_, err = store.Invite(t.Context(), nil, testScope, "wl_1", "sg_1")
		must.ErrorIs(t, err, ErrNilExecutor)
		_, err = store.Convert(t.Context(), nil, testScope, "wl_1", "sg_1")
		must.ErrorIs(t, err, ErrNilExecutor)
		_, err = store.Withdraw(t.Context(), nil, testScope, "wl_1", "sg_1")
		must.ErrorIs(t, err, ErrNilExecutor)
		_, err = store.ArchiveSignup(t.Context(), nil, testScope, "wl_1", "sg_1")
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetSignup(t.Context(), nil, testScope, "wl_1", "sg_1")
		must.ErrorIs(t, err, ErrNilExecutor)
		_, err = store.GetSignupByContact(t.Context(), nil, testScope, "wl_1", "ada@example.com")
		must.ErrorIs(t, err, ErrNilExecutor)
		_, err = store.ListSignups(t.Context(), nil, testScope, "wl_1", nil)
		must.ErrorIs(t, err, ErrNilExecutor)
		_, err = store.ListSignupsForSubject(t.Context(), nil, testScope, testSubject, nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.WithdrawSignupsForSubject(t.Context(), nil, testScope, testSubject)
		must.ErrorIs(t, err, ErrNilExecutor)

		// And it wraps the module-wide sentinel, so a caller checking for a
		// nil input generally is answered too.
		must.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	t.Run("the erasure and the writes it follows share the caller's transaction", func(t *testing.T) {
		t.Parallel()

		// The shape a data privacy erasure has: several domains' erasers, one
		// transaction, and this one's rows withdrawn or restored with the rest.
		store := env.newStore(t)

		list := mustCreateList(t, env, store, testScope, openList("Launch"))
		mine := mustJoin(t, env, store, testScope, list.ID,
			&Signup{Contact: "ada@example.com", Subject: testSubject})

		err := env.inTx(t, func(tx database.Tx) error {
			withdrawn, txErr := store.WithdrawSignupsForSubject(t.Context(), tx, testScope, testSubject)
			if txErr != nil {
				return txErr
			}

			test.EqOp(t, int64(1), withdrawn)

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		signup, err := store.GetSignup(t.Context(), env.reader(), testScope, list.ID, mine.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusWaiting, signup.Status)
		test.EqOp(t, "ada@example.com", signup.Contact)
		test.EqOp(t, testSubject, signup.Subject)

		everything := filtering.DefaultQueryFilter()
		everything.IncludeArchived = new(true)

		page, err := store.ListSignupsForSubject(t.Context(), env.reader(), testScope, testSubject, everything)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
	})
}

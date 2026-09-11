package waitlists

import (
	"testing"
	"time"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runSignupSuite is every assertion about joining a list, reading the queue, and
// moving through it.
func runSignupSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("Join", func(T *testing.T) {
		T.Run("stores the contact as given and the digest of its normalization", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)
			list := mustCreateList(t, env, store, testScope, openList("Launch"))

			joined := mustJoin(t, env, store, testScope, list.ID, &Signup{
				Contact: "Ada@Example.com",
				Notes:   "met at the conference",
				Subject: testSubject,
			})

			test.NotEqOp(t, "", joined.ID)
			test.EqOp(t, "Ada@Example.com", joined.Contact)
			test.EqOp(t, store.Digest("ada@example.com"), joined.ContactDigest)
			test.EqOp(t, StatusWaiting, joined.Status)
			test.Nil(t, joined.StatusChangedAt)
			test.False(t, joined.CreatedAt.IsZero())
		})

		T.Run("the status and the stamps are the store's, not the caller's", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)
			list := mustCreateList(t, env, store, testScope, openList("Launch"))

			// A caller describing a signup that has already happened would
			// otherwise put somebody straight into a status the lifecycle
			// guards exist to move them through.
			joined := mustJoin(t, env, store, testScope, list.ID, &Signup{
				Contact:         "ada@example.com",
				Status:          StatusConverted,
				StatusChangedAt: pointer.To(testNow),
				ArchivedAt:      pointer.To(testNow),
			})

			test.EqOp(t, StatusWaiting, joined.Status)
			test.Nil(t, joined.StatusChangedAt)
			test.Nil(t, joined.ArchivedAt)
		})

		T.Run("refuses a closed list, an archived one, and a missing one", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			closed := mustCreateList(t, env, store, testScope, closedList("closed"))
			archived := mustCreateList(t, env, store, testScope, openList("archived"))
			must.NoError(t, env.archiveList(t, store, testScope, archived.ID))

			_, err := env.join(t, store, testScope, closed.ID, &Signup{Contact: "ada@example.com"})
			test.ErrorIs(t, err, ErrListClosed)

			// An archived list is not found rather than closed: the read that
			// resolves it excludes archived rows, and a caller holding the id
			// of a retired list is holding a broken link.
			_, err = env.join(t, store, testScope, archived.ID, &Signup{Contact: "ada@example.com"})
			test.ErrorIs(t, err, ErrListNotFound)

			_, err = env.join(t, store, testScope, "nope", &Signup{Contact: "ada@example.com"})
			test.ErrorIs(t, err, ErrListNotFound)
		})

		T.Run("refuses a signup naming nothing to write to", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)
			list := mustCreateList(t, env, store, testScope, openList("Launch"))

			_, err := env.join(t, store, testScope, list.ID, nil)
			test.ErrorIs(t, err, ErrNilSignup)

			_, err = env.join(t, store, testScope, list.ID, &Signup{Contact: "   "})
			test.ErrorIs(t, err, ErrEmptyContact)
		})

		T.Run("refuses half a subject", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)
			list := mustCreateList(t, env, store, testScope, openList("Launch"))

			_, err := env.join(t, store, testScope, list.ID, &Signup{
				Contact: "ada@example.com",
				Subject: Subject{Type: SubjectUser},
			})
			test.ErrorIs(t, err, ErrEmptySubjectID)

			_, err = env.join(t, store, testScope, list.ID, &Signup{
				Contact: "ada@example.com",
				Subject: Subject{ID: "user-1"},
			})
			test.ErrorIs(t, err, ErrEmptySubjectType)
		})

		T.Run("refuses a signup that names another scope than the write", func(t *testing.T) {
			t.Parallel()

			// The same ruling the list writes take, on the other table: the
			// argument is what the statement binds, so a signup carrying a
			// different scope is refused rather than corrected — see Store. One
			// carrying none adopts the argument, which is every other case in
			// this suite.
			store := env.newStore(t)
			list := mustCreateList(t, env, store, testScope, openList("Launch"))

			_, err := env.join(t, store, testScope, list.ID, &Signup{
				Contact: "ada@example.com",
				Scope:   otherScope,
			})
			test.ErrorIs(t, err, ErrScopeMismatch)

			page, err := store.ListSignups(t.Context(), env.reader(), testScope, list.ID, nil)
			must.NoError(t, err)
			test.SliceEmpty(t, page.Data)
		})

		T.Run("refuses a contact already on the list, whatever its capitalization", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)
			list := mustCreateList(t, env, store, testScope, openList("Launch"))

			mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

			_, err := env.join(t, store, testScope, list.ID, &Signup{Contact: "  ADA@Example.com "})
			test.ErrorIs(t, err, ErrAlreadySignedUp)
		})

		T.Run("refuses a contact whose signup was archived", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)
			list := mustCreateList(t, env, store, testScope, openList("Launch"))

			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})
			must.NoError(t, env.archiveSignup(t, store, testScope, list.ID, signup.ID))

			// The uniqueness covers archived rows, so the check that stands in
			// for it has to see them — otherwise this is a driver error naming
			// an index rather than something a caller can show somebody.
			_, err := env.join(t, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})
			test.ErrorIs(t, err, ErrAlreadySignedUp)
		})

		T.Run("one contact may be on two lists and in two scopes", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			first := mustCreateList(t, env, store, testScope, openList("first"))
			second := mustCreateList(t, env, store, testScope, openList("second"))
			theirs := mustCreateList(t, env, store, otherScope, openList("theirs"))

			mustJoin(t, env, store, testScope, first.ID, &Signup{Contact: "ada@example.com"})
			mustJoin(t, env, store, testScope, second.ID, &Signup{Contact: "ada@example.com"})
			mustJoin(t, env, store, otherScope, theirs.ID, &Signup{Contact: "ada@example.com"})
		})
	})

	t.Run("GetSignup", func(T *testing.T) {
		T.Run("keys on the list as well as the id and the scope", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			other := mustCreateList(t, env, store, testScope, openList("other"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

			_, err := store.GetSignup(t.Context(), env.reader(), testScope, other.ID, signup.ID)
			test.ErrorIs(t, err, ErrSignupNotFound)

			_, err = store.GetSignup(t.Context(), env.reader(), otherScope, list.ID, signup.ID)
			test.ErrorIs(t, err, ErrSignupNotFound)

			_, err = store.GetSignup(t.Context(), env.reader(), testScope, list.ID, "")
			test.ErrorIs(t, err, platformerrors.ErrInvalidIDProvided)
		})
	})

	t.Run("GetSignupByContact", func(T *testing.T) {
		T.Run("finds a signup under any capitalization of its address", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "Ada@Example.com"})

			read, err := store.GetSignupByContact(t.Context(), env.reader(), testScope, list.ID, " ada@EXAMPLE.com ")
			must.NoError(t, err)
			test.EqOp(t, signup.ID, read.ID)
		})

		T.Run("reports an archived signup as missing and a withdrawn one as withdrawn", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)
			list := mustCreateList(t, env, store, testScope, openList("Launch"))

			archived := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "archived@example.com"})
			must.NoError(t, env.archiveSignup(t, store, testScope, list.ID, archived.ID))

			_, err := store.GetSignupByContact(t.Context(), env.reader(), testScope, list.ID, "archived@example.com")
			test.ErrorIs(t, err, ErrSignupNotFound)

			gone := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "gone@example.com"})
			must.NoError(t, env.withdraw(t, store, testScope, list.ID, gone.ID))

			// A withdrawn row is live, which is what lets an unsubscribe page
			// say "you are already off this list" rather than "we have never
			// heard of you". The address it was found by is no longer in it.
			read, err := store.GetSignupByContact(t.Context(), env.reader(), testScope, list.ID, "gone@example.com")
			must.NoError(t, err)
			test.EqOp(t, StatusWithdrawn, read.Status)
			test.EqOp(t, "", read.Contact)
		})

		T.Run("refuses an empty contact rather than answering", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)
			list := mustCreateList(t, env, store, testScope, openList("Launch"))

			_, err := store.GetSignupByContact(t.Context(), env.reader(), testScope, list.ID, "  ")
			test.ErrorIs(t, err, ErrEmptyContact)
		})
	})

	t.Run("ListSignups", func(T *testing.T) {
		T.Run("pages one list's queue in the order people joined", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			other := mustCreateList(t, env, store, testScope, openList("other"))

			first := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "first@example.com"})
			second := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "second@example.com"})
			mustJoin(t, env, store, testScope, other.ID, &Signup{Contact: "elsewhere@example.com"})

			page, err := store.ListSignups(t.Context(), env.reader(), testScope, list.ID, nil)
			must.NoError(t, err)
			must.SliceLen(t, 2, page.Data)
			test.EqOp(t, first.ID, page.Data[0].ID)
			test.EqOp(t, second.ID, page.Data[1].ID)

			descending := filtering.DefaultQueryFilter()
			descending.SortBy = filtering.SortDescending

			page, err = store.ListSignups(t.Context(), env.reader(), testScope, list.ID, descending)
			must.NoError(t, err)
			must.SliceLen(t, 2, page.Data)
			test.EqOp(t, second.ID, page.Data[0].ID)
		})

		T.Run("does not cross scopes", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

			page, err := store.ListSignups(t.Context(), env.reader(), otherScope, list.ID, nil)
			must.NoError(t, err)
			test.SliceEmpty(t, page.Data)
		})
	})

	t.Run("ListSignupsForSubject", func(T *testing.T) {
		T.Run("pages one principal's signups across every list", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			first := mustCreateList(t, env, store, testScope, openList("first"))
			second := mustCreateList(t, env, store, testScope, openList("second"))

			mine := mustJoin(t, env, store, testScope, first.ID, &Signup{Contact: "ada@example.com", Subject: testSubject})
			alsoMine := mustJoin(t, env, store, testScope, second.ID, &Signup{Contact: "ada@example.com", Subject: testSubject})
			mustJoin(t, env, store, testScope, first.ID, &Signup{Contact: "anon@example.com"})

			page, err := store.ListSignupsForSubject(t.Context(), env.reader(), testScope, testSubject, nil)
			must.NoError(t, err)
			must.SliceLen(t, 2, page.Data)
			test.EqOp(t, mine.ID, page.Data[0].ID)
			test.EqOp(t, alsoMine.ID, page.Data[1].ID)

			descending := filtering.DefaultQueryFilter()
			descending.SortBy = filtering.SortDescending

			page, err = store.ListSignupsForSubject(t.Context(), env.reader(), testScope, testSubject, descending)
			must.NoError(t, err)
			must.SliceLen(t, 2, page.Data)
			test.EqOp(t, alsoMine.ID, page.Data[0].ID)
		})

		T.Run("reaches archived signups when asked", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			first := mustCreateList(t, env, store, testScope, openList("first"))
			second := mustCreateList(t, env, store, testScope, openList("second"))

			live := mustJoin(t, env, store, testScope, first.ID, &Signup{Contact: "ada@example.com", Subject: testSubject})
			retired := mustJoin(t, env, store, testScope, second.ID, &Signup{Contact: "ada@example.com", Subject: testSubject})
			must.NoError(t, env.archiveSignup(t, store, testScope, second.ID, retired.ID))

			page, err := store.ListSignupsForSubject(t.Context(), env.reader(), testScope, testSubject, nil)
			must.NoError(t, err)
			must.SliceLen(t, 1, page.Data)
			test.EqOp(t, live.ID, page.Data[0].ID)

			// An archived signup still holds the address it was made with, and
			// the read an export walks has to be able to say so.
			everything := filtering.DefaultQueryFilter()
			everything.IncludeArchived = new(true)

			page, err = store.ListSignupsForSubject(t.Context(), env.reader(), testScope, testSubject, everything)
			must.NoError(t, err)
			must.SliceLen(t, 2, page.Data)
			test.EqOp(t, retired.ID, page.Data[1].ID)
			test.NotNil(t, page.Data[1].ArchivedAt)
			test.EqOp(t, "ada@example.com", page.Data[1].Contact)
		})

		T.Run("refuses the anonymous subject rather than paging everybody", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "anon@example.com"})

			// Both columns default to the empty string, so a read bound to
			// nobody would page every signup nobody claimed — the widest
			// possible answer to a question about one person.
			_, err := store.ListSignupsForSubject(t.Context(), env.reader(), testScope, Subject{}, nil)
			test.ErrorIs(t, err, ErrEmptySubjectType)

			_, err = store.ListSignupsForSubject(t.Context(), env.reader(), testScope, Subject{Type: SubjectUser}, nil)
			test.ErrorIs(t, err, ErrEmptySubjectID)
		})
	})

	t.Run("UpdateSignupNotes", func(T *testing.T) {
		T.Run("moves nobody", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

			must.NoError(t, env.invite(t, store, testScope, list.ID, signup.ID))

			invited, err := store.GetSignup(t.Context(), env.reader(), testScope, list.ID, signup.ID)
			must.NoError(t, err)
			must.NotNil(t, invited.StatusChangedAt)

			must.NoError(t, env.updateNotes(t, store, testScope, list.ID, signup.ID, "typo fixed"))

			read, err := store.GetSignup(t.Context(), env.reader(), testScope, list.ID, signup.ID)
			must.NoError(t, err)
			test.EqOp(t, "typo fixed", read.Notes)
			test.EqOp(t, StatusInvited, read.Status)

			// The reminder that goes out three days after an invitation is
			// scheduled off StatusChangedAt, and a typo must not reschedule it.
			must.NotNil(t, read.StatusChangedAt)
			test.EqOp(t, *invited.StatusChangedAt, *read.StatusChangedAt)
		})

		T.Run("does not cross lists or scopes", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			other := mustCreateList(t, env, store, testScope, openList("other"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

			test.ErrorIs(t, env.updateNotes(t, store, otherScope, list.ID, signup.ID, "x"), ErrSignupNotFound)
			test.ErrorIs(t, env.updateNotes(t, store, testScope, other.ID, signup.ID, "x"), ErrSignupNotFound)
			test.ErrorIs(t, env.updateNotes(t, store, testScope, list.ID, "", "x"),
				platformerrors.ErrInvalidIDProvided)
		})
	})

	t.Run("Invite and Convert", func(T *testing.T) {
		T.Run("walk the lifecycle and stamp each move", func(t *testing.T) {
			t.Parallel()

			c := newStubClock()
			store := env.newStore(t, WithClock(c))

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

			c.advance(time.Hour)
			must.NoError(t, env.invite(t, store, testScope, list.ID, signup.ID))

			invited, err := store.GetSignup(t.Context(), env.reader(), testScope, list.ID, signup.ID)
			must.NoError(t, err)
			test.EqOp(t, StatusInvited, invited.Status)
			must.NotNil(t, invited.StatusChangedAt)

			c.advance(time.Hour)
			must.NoError(t, env.convert(t, store, testScope, list.ID, signup.ID))

			converted, err := store.GetSignup(t.Context(), env.reader(), testScope, list.ID, signup.ID)
			must.NoError(t, err)
			test.EqOp(t, StatusConverted, converted.Status)
			must.NotNil(t, converted.StatusChangedAt)
			test.True(t, converted.StatusChangedAt.After(*invited.StatusChangedAt))
		})

		T.Run("happen once, and say what the signup is instead", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

			// Converting somebody who was never invited is the out-of-order
			// move, and the guard is what refuses it.
			test.ErrorIs(t, env.convert(t, store, testScope, list.ID, signup.ID), ErrWrongStatus)

			must.NoError(t, env.invite(t, store, testScope, list.ID, signup.ID))

			// The second invitation is the one that would send a second email.
			// It loses on the affected-row count rather than on a read.
			test.ErrorIs(t, env.invite(t, store, testScope, list.ID, signup.ID), ErrWrongStatus)
		})

		T.Run("report a missing signup rather than a wrong status", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

			// A statement that matched nothing cannot tell the two apart, which
			// is why the losing path reads once more to find out.
			test.ErrorIs(t, env.invite(t, store, testScope, list.ID, "nope"), ErrSignupNotFound)
			test.ErrorIs(t, env.invite(t, store, otherScope, list.ID, signup.ID), ErrSignupNotFound)
			test.ErrorIs(t, env.invite(t, store, testScope, list.ID, ""), platformerrors.ErrInvalidIDProvided)
		})
	})

	t.Run("ArchiveSignup", func(T *testing.T) {
		T.Run("hides the row and suppresses nothing", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

			must.NoError(t, env.archiveSignup(t, store, testScope, list.ID, signup.ID))

			_, err := store.GetSignup(t.Context(), env.reader(), testScope, list.ID, signup.ID)
			test.ErrorIs(t, err, ErrSignupNotFound)

			page, err := store.ListSignups(t.Context(), env.reader(), testScope, list.ID, nil)
			must.NoError(t, err)
			test.SliceEmpty(t, page.Data)

			// Archiving is not withdrawing: nothing was erased, so what the
			// next attempt gets is the collision rather than the suppression.
			_, err = env.join(t, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})
			test.ErrorIs(t, err, ErrAlreadySignedUp)

			test.ErrorIs(t, env.archiveSignup(t, store, testScope, list.ID, signup.ID), ErrSignupNotFound)
			test.ErrorIs(t, env.archiveSignup(t, store, testScope, list.ID, ""),
				platformerrors.ErrInvalidIDProvided)
		})
	})
}

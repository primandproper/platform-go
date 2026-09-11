package waitlists

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runWithdrawalSuite is every assertion about the obligation this package is
// shaped around: somebody asks to come off a list, and stays off it.
//
// It is its own suite rather than a subtest of the signup one because it is the
// property the schema, the digest column and the guarded write all exist for,
// and a reader looking for "does the unsubscribe actually hold" should find it
// in one place.
func runWithdrawalSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("Withdraw", func(T *testing.T) {
		T.Run("erases what the row said about a person and keeps the digest", func(t *testing.T) {
			t.Parallel()

			c := newStubClock()
			store := env.newStore(t, WithClock(c))

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{
				Contact: "ada@example.com",
				Notes:   "met at the conference",
				Subject: testSubject,
			})

			c.advance(time.Hour)
			must.NoError(t, env.withdraw(t, store, testScope, list.ID, signup.ID))

			read, err := store.GetSignup(t.Context(), env.reader(), testScope, list.ID, signup.ID)
			must.NoError(t, err)

			test.EqOp(t, StatusWithdrawn, read.Status)
			test.True(t, read.Withdrawn())
			test.EqOp(t, "", read.Contact)
			test.EqOp(t, "", read.Notes)
			test.True(t, read.Subject.Anonymous())
			must.NotNil(t, read.StatusChangedAt)

			// The digest is the whole point: it is what remembers somebody the
			// table no longer holds.
			test.EqOp(t, signup.ContactDigest, read.ContactDigest)
		})

		T.Run("holds against a later signup from the same address", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "Ada@Example.com"})

			must.NoError(t, env.withdraw(t, store, testScope, list.ID, signup.ID))

			// The failure a hand-rolled table has: delete the row and the next
			// form submission re-subscribes whoever asked to be left alone.
			_, err := env.join(t, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})
			test.ErrorIs(t, err, ErrContactWithdrawn)

			// And under a different capitalization, which is why the digest is
			// taken of Normalize's output rather than of the address as typed.
			_, err = env.join(t, store, testScope, list.ID, &Signup{Contact: "  ADA@EXAMPLE.COM "})
			test.ErrorIs(t, err, ErrContactWithdrawn)
		})

		T.Run("is per list, not per person", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			first := mustCreateList(t, env, store, testScope, openList("first"))
			second := mustCreateList(t, env, store, testScope, openList("second"))

			signup := mustJoin(t, env, store, testScope, first.ID, &Signup{Contact: "ada@example.com"})
			must.NoError(t, env.withdraw(t, store, testScope, first.ID, signup.ID))

			// Coming off one list is not coming off every list. The uniqueness
			// and the suppression are both keyed on (scope, list, digest).
			mustJoin(t, env, store, testScope, second.ID, &Signup{Contact: "ada@example.com"})
		})

		T.Run("takes the row out of the subject's own reads", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{
				Contact: "ada@example.com",
				Subject: testSubject,
			})

			must.NoError(t, env.withdraw(t, store, testScope, list.ID, signup.ID))

			// The subject reference goes with the contact, so the row that
			// remembers a suppression no longer says whose it was.
			page, err := store.ListSignupsForSubject(t.Context(), env.reader(), testScope, testSubject, nil)
			must.NoError(t, err)
			test.SliceEmpty(t, page.Data)
		})

		T.Run("moves somebody at any point in the lifecycle", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))

			invited := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "invited@example.com"})
			must.NoError(t, env.invite(t, store, testScope, list.ID, invited.ID))
			must.NoError(t, env.withdraw(t, store, testScope, list.ID, invited.ID))

			converted := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "converted@example.com"})
			must.NoError(t, env.invite(t, store, testScope, list.ID, converted.ID))
			must.NoError(t, env.convert(t, store, testScope, list.ID, converted.ID))

			// Somebody who took the invitation up may still ask to stop hearing
			// from the list, so the withdrawal is guarded on not-yet-withdrawn
			// rather than on any particular status.
			must.NoError(t, env.withdraw(t, store, testScope, list.ID, converted.ID))
		})

		T.Run("a replay reports rather than restamping", func(t *testing.T) {
			t.Parallel()

			c := newStubClock()
			store := env.newStore(t, WithClock(c))

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

			must.NoError(t, env.withdraw(t, store, testScope, list.ID, signup.ID))

			first, err := store.GetSignup(t.Context(), env.reader(), testScope, list.ID, signup.ID)
			must.NoError(t, err)
			must.NotNil(t, first.StatusChangedAt)

			c.advance(24 * time.Hour)
			test.ErrorIs(t, env.withdraw(t, store, testScope, list.ID, signup.ID), ErrAlreadyWithdrawn)

			// The moment somebody asked to come off a list is a fact about
			// them, and a second request must not move it.
			second, err := store.GetSignup(t.Context(), env.reader(), testScope, list.ID, signup.ID)
			must.NoError(t, err)
			must.NotNil(t, second.StatusChangedAt)
			test.EqOp(t, *first.StatusChangedAt, *second.StatusChangedAt)
		})

		T.Run("does not cross lists or scopes", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			other := mustCreateList(t, env, store, testScope, openList("other"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

			test.ErrorIs(t, env.withdraw(t, store, otherScope, list.ID, signup.ID), ErrSignupNotFound)
			test.ErrorIs(t, env.withdraw(t, store, testScope, other.ID, signup.ID), ErrSignupNotFound)
		})

		T.Run("outlives the list closing", func(t *testing.T) {
			t.Parallel()

			c := newStubClock()
			store := env.newStore(t, WithClock(c))

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

			c.advance(31 * 24 * time.Hour)

			// A closed list still lets somebody leave it. Nothing about the
			// withdrawal reads the list, which is deliberate: an obligation
			// that expired with the list would be an obligation.
			must.NoError(t, env.withdraw(t, store, testScope, list.ID, signup.ID))
		})
	})

	t.Run("WithdrawSignupsForSubject", func(T *testing.T) {
		T.Run("withdraws every signup the subject holds, archived included, and keeps each digest", func(t *testing.T) {
			t.Parallel()

			c := newStubClock()
			store := env.newStore(t, WithClock(c))

			first := mustCreateList(t, env, store, testScope, openList("first"))
			second := mustCreateList(t, env, store, testScope, openList("second"))

			live := mustJoin(t, env, store, testScope, first.ID, &Signup{
				Contact: "ada@example.com",
				Notes:   "met at the conference",
				Subject: testSubject,
			})
			retired := mustJoin(t, env, store, testScope, second.ID, &Signup{Contact: "ada@example.com", Subject: testSubject})
			must.NoError(t, env.archiveSignup(t, store, testScope, second.ID, retired.ID))

			// Two rows the erasure must leave alone: somebody else's, and one
			// naming nobody — which is the row a predicate bound to the empty
			// subject would have reached.
			theirs := mustJoin(t, env, store, testScope, first.ID, &Signup{
				Contact: "grace@example.com",
				Subject: Subject{Type: SubjectUser, ID: "user-2"},
			})
			nobodys := mustJoin(t, env, store, testScope, first.ID, &Signup{Contact: "anon@example.com"})

			c.advance(time.Hour)

			withdrawn := eraseSubject(t, env, store, testScope, testSubject)
			test.EqOp(t, int64(2), withdrawn)

			// The live one is left exactly as Withdraw leaves a row.
			read, err := store.GetSignup(t.Context(), env.reader(), testScope, first.ID, live.ID)
			must.NoError(t, err)
			test.EqOp(t, StatusWithdrawn, read.Status)
			test.EqOp(t, "", read.Contact)
			test.EqOp(t, "", read.Notes)
			test.True(t, read.Subject.Anonymous())
			test.EqOp(t, live.ContactDigest, read.ContactDigest)
			must.NotNil(t, read.StatusChangedAt)
			test.EqOp(t, c.read(), *read.StatusChangedAt)

			// And so is the archived one, which the single-row withdrawal
			// cannot reach and which still held the address until now.
			everything := filtering.DefaultQueryFilter()
			everything.IncludeArchived = new(true)

			page, err := store.ListSignups(t.Context(), env.reader(), testScope, second.ID, everything)
			must.NoError(t, err)
			must.SliceLen(t, 1, page.Data)
			test.EqOp(t, retired.ID, page.Data[0].ID)
			test.EqOp(t, StatusWithdrawn, page.Data[0].Status)
			test.EqOp(t, "", page.Data[0].Contact)
			test.True(t, page.Data[0].Subject.Anonymous())
			test.NotNil(t, page.Data[0].ArchivedAt)

			// Nothing names the subject any more, archived rows included.
			page, err = store.ListSignupsForSubject(t.Context(), env.reader(), testScope, testSubject, everything)
			must.NoError(t, err)
			test.SliceEmpty(t, page.Data)

			// The suppression holds on both lists: an erasure that freed the
			// key would let the next form submission re-subscribe somebody
			// erased at their own request.
			_, err = env.join(t, store, testScope, first.ID, &Signup{Contact: "Ada@Example.com"})
			test.ErrorIs(t, err, ErrContactWithdrawn)
			_, err = env.join(t, store, testScope, second.ID, &Signup{Contact: "ada@example.com"})
			test.ErrorIs(t, err, ErrContactWithdrawn)

			// The other two are untouched.
			read, err = store.GetSignup(t.Context(), env.reader(), testScope, first.ID, theirs.ID)
			must.NoError(t, err)
			test.EqOp(t, "grace@example.com", read.Contact)
			test.EqOp(t, StatusWaiting, read.Status)

			read, err = store.GetSignup(t.Context(), env.reader(), testScope, first.ID, nobodys.ID)
			must.NoError(t, err)
			test.EqOp(t, "anon@example.com", read.Contact)
			test.EqOp(t, StatusWaiting, read.Status)
		})

		T.Run("does not cross scopes", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			elsewhere := mustCreateList(t, env, store, otherScope, openList("Launch"))
			mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com", Subject: testSubject})
			theirs := mustJoin(t, env, store, otherScope, elsewhere.ID, &Signup{Contact: "ada@example.com", Subject: testSubject})

			test.EqOp(t, int64(1), eraseSubject(t, env, store, testScope, testSubject))

			read, err := store.GetSignup(t.Context(), env.reader(), otherScope, elsewhere.ID, theirs.ID)
			must.NoError(t, err)
			test.EqOp(t, "ada@example.com", read.Contact)
			test.EqOp(t, testSubject, read.Subject)
		})

		T.Run("a subject with nothing here is zero, not an error", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			test.EqOp(t, int64(0), eraseSubject(t, env, store, testScope, testSubject))
		})

		T.Run("refuses half a subject and the anonymous one", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			nobodys := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "anon@example.com"})

			var anonymous, typeless, idless error

			must.NoError(t, env.inTx(t, func(tx database.Tx) error {
				_, anonymous = store.WithdrawSignupsForSubject(t.Context(), tx, testScope, Subject{})
				_, typeless = store.WithdrawSignupsForSubject(t.Context(), tx, testScope, Subject{ID: "user-1"})
				_, idless = store.WithdrawSignupsForSubject(t.Context(), tx, testScope, Subject{Type: SubjectUser})

				return nil
			}))

			test.ErrorIs(t, anonymous, ErrEmptySubjectType)
			test.ErrorIs(t, typeless, ErrEmptySubjectType)
			test.ErrorIs(t, idless, ErrEmptySubjectID)

			// The row naming nobody is the one an unrefused anonymous subject
			// would have withdrawn.
			read, err := store.GetSignup(t.Context(), env.reader(), testScope, list.ID, nobodys.ID)
			must.NoError(t, err)
			test.EqOp(t, StatusWaiting, read.Status)
		})
	})
}

// eraseSubject runs the erasure in a transaction of its own and hands back the
// count, for the assertions that are about what it did rather than about the
// transaction it did it in.
func eraseSubject(tb testing.TB, e *storeEnv, store *SQLStore, scope tenancy.Scope, subject Subject) int64 {
	tb.Helper()

	var withdrawn int64

	must.NoError(tb, e.inTx(tb, func(tx database.Tx) (err error) {
		withdrawn, err = store.WithdrawSignupsForSubject(tb.Context(), tx, scope, subject)

		return err
	}))

	return withdrawn
}

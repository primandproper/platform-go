package waitlists

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/audit"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestStatus(T *testing.T) {
	T.Parallel()

	T.Run("Valid is the closed set and nothing else", func(t *testing.T) {
		t.Parallel()

		for _, status := range []Status{StatusWaiting, StatusInvited, StatusConverted, StatusWithdrawn} {
			test.True(t, status.Valid(), test.Sprintf("%s should be valid", status))
			test.EqOp(t, string(status), status.String())
		}

		test.False(t, Status("").Valid())
		test.False(t, Status("declined").Valid())
	})
}

func TestSubject(T *testing.T) {
	T.Parallel()

	T.Run("is whole or absent, never half", func(t *testing.T) {
		t.Parallel()

		// The read that finds a signup by subject binds both columns, so half a
		// subject is a row nothing will list or a row the wrong list finds.
		test.NoError(t, Subject{}.Validate())
		test.True(t, Subject{}.Anonymous())

		test.NoError(t, Subject{Type: SubjectUser, ID: "user-1"}.Validate())
		test.False(t, Subject{Type: SubjectUser, ID: "user-1"}.Anonymous())

		test.ErrorIs(t, Subject{ID: "user-1"}.Validate(), ErrEmptySubjectType)
		test.ErrorIs(t, Subject{Type: SubjectAccount}.Validate(), ErrEmptySubjectID)
	})

	T.Run("renders the type as it is stored", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "user", SubjectUser.String())
		test.EqOp(t, "account", SubjectAccount.String())
	})
}

func TestNormalize(T *testing.T) {
	T.Parallel()

	T.Run("folds case and trims, and stops there", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "ada@example.com", Normalize("  Ada@Example.COM "))
		test.EqOp(t, "", Normalize("   "))

		// Plus-addressing and dots are a provider's own policy about which
		// addresses are the same mailbox, so nothing here touches them.
		test.EqOp(t, "ada+beta@example.com", Normalize("Ada+Beta@example.com"))
		test.EqOp(t, "a.da@example.com", Normalize("A.Da@example.com"))
	})
}

func TestList_OpenAt(T *testing.T) {
	T.Parallel()

	T.Run("is closed at its closing instant, not after it", func(t *testing.T) {
		t.Parallel()

		list := &List{ClosesAt: testNow}

		test.True(t, list.OpenAt(testNow.Add(-time.Nanosecond)))

		// The boundary is exclusive on the open side, which is the reading that
		// leaves no instant at which a list is neither open nor closed.
		test.False(t, list.OpenAt(testNow))
		test.False(t, list.OpenAt(testNow.Add(time.Nanosecond)))
	})

	T.Run("an archived list is closed whatever its closing time says", func(t *testing.T) {
		t.Parallel()

		archived := &List{ClosesAt: testNow.Add(time.Hour), ArchivedAt: &testNow}

		test.False(t, archived.OpenAt(testNow))
		test.False(t, (*List)(nil).OpenAt(testNow))
	})
}

func TestList_validate(T *testing.T) {
	T.Parallel()

	T.Run("names what a list cannot be stored without", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, (*List)(nil).validate(), ErrNilList)
		test.ErrorIs(t, (&List{ClosesAt: testNow}).validate(), ErrEmptyListName)
		test.ErrorIs(t, (&List{Name: "Launch"}).validate(), ErrEmptyClosesAt)
		test.NoError(t, (&List{Name: "Launch", ClosesAt: testNow}).validate())
	})
}

func TestSignup_auditDiffOmitsContact(T *testing.T) {
	T.Parallel()

	before := &Signup{Notes: "before", Contact: "before@example.com"}
	after := &Signup{Notes: "after", Contact: "after@example.com"}

	changes, err := audit.Diff(before, after)
	must.NoError(T, err)

	test.MapContainsKey(T, changes, "notes")
	test.MapNotContainsKey(T, changes, "contact")
}

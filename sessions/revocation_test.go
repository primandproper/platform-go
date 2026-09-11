package sessions

import (
	stderrors "errors"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testHolder is the holder these tests attribute sessions to.
func testHolder() Holder {
	return Holder{Scope: tenancy.Of("acct_1"), Principal: "u_1"}
}

func TestBackendStore_NewFor(T *testing.T) {
	T.Parallel()

	T.Run("stamps the holder and the metadata onto the session", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		metadata := Metadata{
			DeviceName:  "Jeffrey's laptop",
			IPAddress:   "203.0.113.4",
			UserAgent:   "Mozilla/5.0",
			LoginMethod: "passkey",
		}

		session, err := store.NewFor(t.Context(), testHolder(), metadata, &principal{UserID: "u_1"})
		must.NoError(t, err)

		test.EqOp(t, testHolder(), session.Holder)
		test.EqOp(t, metadata, session.Metadata)
	})

	// The by-identifier path is the one every request takes, and it has to
	// answer with the same holder the enumeration does — a session whose owner
	// depended on which door was used would be two sources of truth again.
	T.Run("and the by-identifier read gives them back", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		metadata := Metadata{DeviceName: "phone", LoginMethod: "password"}

		created, err := store.NewFor(t.Context(), testHolder(), metadata, nil)
		must.NoError(t, err)

		read, err := store.Get(t.Context(), created.ID)
		must.NoError(t, err)

		test.EqOp(t, testHolder(), read.Holder)
		test.EqOp(t, metadata, read.Metadata)
	})

	// An unset scope and an empty principal both produce a session that stores
	// fine, reads fine, and appears in no list — so both are refused before
	// anything is written rather than after.
	T.Run("refuses a scopeless holder without writing", func(t *testing.T) {
		t.Parallel()

		store, backend, _ := newTestStore(t)

		_, err := store.NewFor(t.Context(), Holder{Principal: "u_1"}, Metadata{}, nil)
		test.ErrorIs(t, err, tenancy.ErrNoScope)
		test.SliceEmpty(t, backend.ids())
	})

	T.Run("refuses an empty principal without writing", func(t *testing.T) {
		t.Parallel()

		store, backend, _ := newTestStore(t)

		_, err := store.NewFor(t.Context(), Holder{Scope: tenancy.Global()}, Metadata{}, nil)
		test.ErrorIs(t, err, ErrPrincipalRequired)
		test.SliceEmpty(t, backend.ids())
	})

	// New is the anonymous door, and what makes it anonymous is that the
	// session it writes is held by nobody — global scope, empty principal —
	// which is a decision rather than the zero Scope no query accepts.
	T.Run("New attributes to nobody in the global scope", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		session, err := store.New(t.Context(), nil)
		must.NoError(t, err)

		test.True(t, session.Holder.Scope.IsGlobal())
		test.EqOp(t, "", session.Holder.Principal)
	})
}

func TestBackendStore_List(T *testing.T) {
	T.Parallel()

	T.Run("returns the holder's sessions, newest first", func(t *testing.T) {
		t.Parallel()

		store, _, c := newTestStore(t)

		first, err := store.NewFor(t.Context(), testHolder(), Metadata{DeviceName: "laptop"}, nil)
		must.NoError(t, err)

		c.advance(time.Minute)

		second, err := store.NewFor(t.Context(), testHolder(), Metadata{DeviceName: "phone"}, nil)
		must.NoError(t, err)

		listed, err := store.List(t.Context(), testHolder(), "")
		must.NoError(t, err)
		must.SliceLen(t, 2, listed)

		test.EqOp(t, second.ID, listed[0].ID)
		test.EqOp(t, first.ID, listed[1].ID)
		test.EqOp(t, "phone", listed[0].Metadata.DeviceName)
	})

	// The scope is half the key, and the half a single-tenant test would never
	// exercise: two tenants that spell a principal identically are two people.
	T.Run("does not reach another scope's sessions", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		mine, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		_, err = store.NewFor(t.Context(),
			Holder{Scope: tenancy.Of("acct_2"), Principal: "u_1"}, Metadata{}, nil)
		must.NoError(t, err)

		listed, err := store.List(t.Context(), testHolder(), "")
		must.NoError(t, err)
		must.SliceLen(t, 1, listed)
		test.EqOp(t, mine.ID, listed[0].ID)
	})

	T.Run("does not reach another principal's sessions", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		_, err := store.NewFor(t.Context(),
			Holder{Scope: tenancy.Of("acct_1"), Principal: "u_2"}, Metadata{}, nil)
		must.NoError(t, err)

		listed, err := store.List(t.Context(), testHolder(), "")
		must.NoError(t, err)
		test.SliceEmpty(t, listed)
	})

	// IsCurrent is the caller's session, marked rather than removed: a page
	// that hid the reader's own session would be listing the wrong set.
	T.Run("marks the caller's own session and lists it anyway", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		mine, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		_, err = store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		listed, err := store.List(t.Context(), testHolder(), mine.ID)
		must.NoError(t, err)
		must.SliceLen(t, 2, listed)

		for _, session := range listed {
			test.EqOp(t, session.ID == mine.ID, session.IsCurrent,
				test.Sprintf("session %q", session.ID))
		}
	})

	T.Run("marks nothing current when the caller names no session", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		_, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		listed, err := store.List(t.Context(), testHolder(), "")
		must.NoError(t, err)
		must.SliceLen(t, 1, listed)
		test.False(t, listed[0].IsCurrent)
	})

	// A session the list shows is a session Get would answer, and the store's
	// policy is what decides both — so an idled-out session drops out of the
	// list at exactly the moment it stops being usable.
	T.Run("leaves out sessions the policy has expired", func(t *testing.T) {
		t.Parallel()

		// The grace keeps the expired record present in the backend, so what
		// the list leaves out is a record the store refused rather than one the
		// backend had already reclaimed.
		store, _, c := newTestStore(t, WithIdleTimeout(time.Hour), WithRetentionGrace(24*time.Hour))

		stale, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		c.advance(2 * time.Hour)

		fresh, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		listed, err := store.List(t.Context(), testHolder(), "")
		must.NoError(t, err)
		must.SliceLen(t, 1, listed)
		test.EqOp(t, fresh.ID, listed[0].ID)

		_, err = store.Get(t.Context(), stale.ID)
		test.ErrorIs(t, err, ErrExpired)
	})

	// Opening a security page must not keep every session on it alive, so the
	// enumeration writes nothing at all.
	T.Run("refreshes no idle deadlines", func(t *testing.T) {
		t.Parallel()

		store, backend, _ := newTestStore(t)

		_, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		before := backend.callCount("Update")

		_, err = store.List(t.Context(), testHolder(), "")
		must.NoError(t, err)

		test.EqOp(t, before, backend.callCount("Update"))
	})

	T.Run("refuses a holder that names nobody", func(t *testing.T) {
		t.Parallel()

		store, backend, _ := newTestStore(t)

		_, err := store.List(t.Context(), Holder{Scope: tenancy.Global()}, "")
		test.ErrorIs(t, err, ErrPrincipalRequired)

		_, err = store.List(t.Context(), Holder{Principal: "u_1"}, "")
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		test.EqOp(t, 0, backend.callCount("ListHeld"))
	})

	T.Run("surfaces a backend failure", func(t *testing.T) {
		t.Parallel()

		store, backend, _ := newTestStore(t)
		backend.fail("ListHeld", stderrors.New("store is down"))

		_, err := store.List(t.Context(), testHolder(), "")
		test.Error(t, err)
	})
}

func TestBackendStore_Revoke(T *testing.T) {
	T.Parallel()

	// Revocation is what the by-identifier path has to see immediately: the
	// point of the control is that the revoked session stops working.
	T.Run("ends the session on the by-identifier path", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		session, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		must.NoError(t, store.Revoke(t.Context(), testHolder(), session.ID))

		_, err = store.Get(t.Context(), session.ID)
		test.ErrorIs(t, err, ErrNotFound)
	})

	// Somebody else's session is answered "not found" rather than "not yours",
	// so the answer confirms nothing about an identifier a caller guessed.
	T.Run("will not end another holder's session, and says nothing about it", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		theirs, err := store.NewFor(t.Context(),
			Holder{Scope: tenancy.Of("acct_2"), Principal: "u_9"}, Metadata{}, nil)
		must.NoError(t, err)

		err = store.Revoke(t.Context(), testHolder(), theirs.ID)
		test.ErrorIs(t, err, ErrNotFound)

		// And it is still there.
		read, err := store.Get(t.Context(), theirs.ID)
		must.NoError(t, err)
		test.EqOp(t, theirs.ID, read.ID)
	})

	T.Run("reports an already-revoked session as absent", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		session, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		must.NoError(t, store.Revoke(t.Context(), testHolder(), session.ID))
		test.ErrorIs(t, store.Revoke(t.Context(), testHolder(), session.ID), ErrNotFound)
	})

	T.Run("refuses an empty identifier", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		test.ErrorIs(t, store.Revoke(t.Context(), testHolder(), ""), ErrIDRequired)
	})

	T.Run("refuses a holder that names nobody", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		test.ErrorIs(t, store.Revoke(t.Context(), Holder{Scope: tenancy.Global()}, "id"),
			ErrPrincipalRequired)
	})
}

func TestBackendStore_RevokeAll(T *testing.T) {
	T.Parallel()

	T.Run("ends every session the holder holds", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		first, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)
		second, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		revoked, err := store.RevokeAll(t.Context(), testHolder())
		must.NoError(t, err)
		test.EqOp(t, 2, revoked)

		for _, id := range []string{first.ID, second.ID} {
			_, getErr := store.Get(t.Context(), id)
			test.ErrorIs(t, getErr, ErrNotFound, test.Sprintf("session %q", id))
		}
	})

	T.Run("leaves other holders alone", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		_, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		theirs, err := store.NewFor(t.Context(),
			Holder{Scope: tenancy.Of("acct_1"), Principal: "u_2"}, Metadata{}, nil)
		must.NoError(t, err)

		revoked, err := store.RevokeAll(t.Context(), testHolder())
		must.NoError(t, err)
		test.EqOp(t, 1, revoked)

		_, err = store.Get(t.Context(), theirs.ID)
		must.NoError(t, err)
	})

	T.Run("reports nothing revoked for a holder with no sessions", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		revoked, err := store.RevokeAll(t.Context(), testHolder())
		must.NoError(t, err)
		test.EqOp(t, 0, revoked)
	})
}

func TestBackendStore_RevokeAllExcept(T *testing.T) {
	T.Parallel()

	T.Run("spares the named session and ends the rest", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		kept, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)
		other, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		revoked, err := store.RevokeAllExcept(t.Context(), testHolder(), kept.ID)
		must.NoError(t, err)
		test.EqOp(t, 1, revoked)

		read, err := store.Get(t.Context(), kept.ID)
		must.NoError(t, err)
		test.EqOp(t, kept.ID, read.ID)

		_, err = store.Get(t.Context(), other.ID)
		test.ErrorIs(t, err, ErrNotFound)
	})

	// An empty keepID spares nothing, which is the honest reading: a caller
	// with no current session is signing out of everywhere.
	T.Run("spares nothing when no session is named", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		_, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		revoked, err := store.RevokeAllExcept(t.Context(), testHolder(), "")
		must.NoError(t, err)
		test.EqOp(t, 1, revoked)
	})

	// Sparing is by the same key everything else is, so an identifier the
	// holder does not hold spares nothing of theirs.
	T.Run("spares nothing for an identifier the holder does not hold", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		_, err := store.NewFor(t.Context(), testHolder(), Metadata{}, nil)
		must.NoError(t, err)

		revoked, err := store.RevokeAllExcept(t.Context(), testHolder(), "some-other-session")
		must.NoError(t, err)
		test.EqOp(t, 1, revoked)
	})

	// The anonymous sessions New writes are held by nobody, and nobody's
	// sessions are not enumerable or revocable — which is what stops a
	// revocation over the empty principal from signing out every visitor.
	T.Run("cannot reach the sessions New establishes", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		anonymous, err := store.New(t.Context(), nil)
		must.NoError(t, err)

		_, err = store.RevokeAll(t.Context(), Holder{Scope: tenancy.Global()})
		test.ErrorIs(t, err, ErrPrincipalRequired)

		read, err := store.Get(t.Context(), anonymous.ID)
		must.NoError(t, err)
		test.EqOp(t, anonymous.ID, read.ID)
	})
}

// TestRenew_CarriesTheHolderAcross is the property that keeps rotation safe to
// do on every privilege change: a renewed session is the same person's, still
// listed, still revocable.
func TestRenew_CarriesTheHolderAcross(t *testing.T) {
	t.Parallel()

	store, _, _ := newTestStore(t)

	metadata := Metadata{DeviceName: "laptop", LoginMethod: "password"}

	session, err := store.NewFor(t.Context(), testHolder(), metadata, nil)
	must.NoError(t, err)

	renewedID, err := store.Renew(t.Context(), session.ID)
	must.NoError(t, err)

	read, err := store.Get(t.Context(), renewedID)
	must.NoError(t, err)
	test.EqOp(t, testHolder(), read.Holder)
	test.EqOp(t, metadata, read.Metadata)

	listed, err := store.List(t.Context(), testHolder(), renewedID)
	must.NoError(t, err)
	must.SliceLen(t, 1, listed)
	test.EqOp(t, renewedID, listed[0].ID)
	test.True(t, listed[0].IsCurrent)
}

// TestSave_DoesNotMoveTheHolder is the other half: the writes a live session
// takes must not be able to hand it to somebody else.
func TestSave_DoesNotMoveTheHolder(t *testing.T) {
	t.Parallel()

	store, _, _ := newTestStore(t)

	metadata := Metadata{DeviceName: "laptop"}

	session, err := store.NewFor(t.Context(), testHolder(), metadata, nil)
	must.NoError(t, err)

	must.NoError(t, store.Save(t.Context(), session.ID, &principal{UserID: "u_1", Admin: true}))

	read, err := store.Get(t.Context(), session.ID)
	must.NoError(t, err)
	test.EqOp(t, testHolder(), read.Holder)
	test.EqOp(t, metadata, read.Metadata)
}

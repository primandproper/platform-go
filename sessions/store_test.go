package sessions

import (
	stderrors "errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("requires a backend", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore[principal](nil)
		test.ErrorIs(t, err, ErrNilBackend)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("rejects a policy with no timeout", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore[principal](newFakeBackend[principal](newFakeClock()),
			WithAbsoluteTimeout(0), WithIdleTimeout(0))
		test.ErrorIs(t, err, ErrNoTimeout)
	})

	T.Run("rejects an explicit touch interval that does not fit the idle window", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore[principal](newFakeBackend[principal](newFakeClock()),
			WithIdleTimeout(time.Minute), WithTouchInterval(time.Minute))
		test.ErrorIs(t, err, ErrTouchExceedsIdleTimeout)
	})

	// The default touch interval is one minute, so an idle timeout shorter than
	// that would otherwise fail construction over a value nobody supplied.
	T.Run("clamps the default touch interval to a short idle window", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore[principal](newFakeBackend[principal](newFakeClock()),
			WithIdleTimeout(30*time.Second))
		must.NoError(t, err)
		test.EqOp(t, 15*time.Second, store.Policy().Touch)
	})

	T.Run("reports the policy it enforces", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t, WithAbsoluteTimeout(time.Hour), WithIdleTimeout(20*time.Minute))
		test.EqOp(t, time.Hour, store.Policy().Absolute)
		test.EqOp(t, 20*time.Minute, store.Policy().Idle)
	})
}

func TestStore_New(T *testing.T) {
	T.Parallel()

	T.Run("stores the payload under a fresh identifier", func(t *testing.T) {
		t.Parallel()

		store, backend, _ := newTestStore(t)

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)
		must.NotNil(t, session)

		test.NotEq(t, "", session.ID)
		test.EqOp(t, "u_1", session.Data.UserID)
		test.SliceContains(t, backend.ids(), session.ID)
	})

	T.Run("accepts a nil payload", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		session, err := store.New(t.Context(), nil)
		must.NoError(t, err)
		must.Nil(t, session.Data)

		read, err := store.Get(t.Context(), session.ID)
		must.NoError(t, err)
		test.Nil(t, read.Data)
	})

	// Two sessions minted back to back must not share an identifier, which is
	// the property that stops one user reading another's session.
	T.Run("mints a distinct identifier per session", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		first, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		second, err := store.New(t.Context(), &principal{UserID: "u_2"})
		must.NoError(t, err)

		test.NotEqOp(t, first.ID, second.ID)
	})

	T.Run("expires at the nearer of the two deadlines", func(t *testing.T) {
		t.Parallel()

		store, _, c := newTestStore(t, WithAbsoluteTimeout(time.Hour), WithIdleTimeout(10*time.Minute))

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		test.EqOp(t, c.Now().UTC().Truncate(time.Microsecond).Add(10*time.Minute), session.ExpiresAt)
	})

	// Stamped times are truncated so that a record round-tripped through a
	// database, which keeps microseconds, comes back equal to what New
	// returned.
	T.Run("stamps times at microsecond resolution", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		test.EqOp(t, session.CreatedAt, session.CreatedAt.Truncate(time.Microsecond))
		test.EqOp(t, session.CreatedAt, session.LastSeenAt)
	})

	T.Run("surfaces a backend failure", func(t *testing.T) {
		t.Parallel()

		store, backend, _ := newTestStore(t)
		backend.fail("Create", errors.New("store is down"))

		_, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.Error(t, err)
	})
}

func TestStore_Get(T *testing.T) {
	T.Parallel()

	T.Run("returns the stored payload", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		session, err := store.New(t.Context(), &principal{UserID: "u_1", Admin: true})
		must.NoError(t, err)

		read, err := store.Get(t.Context(), session.ID)
		must.NoError(t, err)
		test.EqOp(t, "u_1", read.Data.UserID)
		test.True(t, read.Data.Admin)
		test.EqOp(t, session.ID, read.ID)
	})

	T.Run("rejects an empty identifier", func(t *testing.T) {
		t.Parallel()

		store, backend, _ := newTestStore(t)

		_, err := store.Get(t.Context(), "")
		test.ErrorIs(t, err, ErrIDRequired)
		// Refused before the backend was troubled with it.
		test.EqOp(t, 0, backend.callCount("Load"))
	})

	T.Run("reports an unknown identifier as not found", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		_, err := store.Get(t.Context(), "nobody-minted-this")
		test.ErrorIs(t, err, ErrNotFound)
	})

	T.Run("reports an idled-out session and removes it", func(t *testing.T) {
		t.Parallel()

		store, backend, c := newTestStore(t, WithIdleTimeout(10*time.Minute))

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		c.advance(10 * time.Minute)

		_, err = store.Get(t.Context(), session.ID)
		test.ErrorIs(t, err, ErrIdleTimeout)
		test.ErrorIs(t, err, ErrExpired)
		// ErrExpired wraps ErrNotFound, so a caller that does not care why the
		// session is unusable checks one thing.
		test.ErrorIs(t, err, ErrNotFound)

		test.SliceNotContains(t, backend.ids(), session.ID)
	})

	// The grace period is what makes the expiry reasons reachable at all: a
	// backend that reclaims the record at the deadline can only report absence.
	T.Run("reports a bare not-found once the grace period is given up", func(t *testing.T) {
		t.Parallel()

		store, _, c := newTestStore(t, WithIdleTimeout(10*time.Minute), WithRetentionGrace(0))

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		c.advance(10 * time.Minute)

		_, err = store.Get(t.Context(), session.ID)
		test.ErrorIs(t, err, ErrNotFound)
		test.False(t, stderrors.Is(err, ErrExpired))
	})

	// The one the absolute timeout exists for: a session that is read
	// constantly still ends on schedule.
	T.Run("reports an absolutely expired session even while it is being used", func(t *testing.T) {
		t.Parallel()

		store, _, c := newTestStore(t,
			WithAbsoluteTimeout(30*time.Minute), WithIdleTimeout(10*time.Minute), WithTouchInterval(0))

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		for range 5 {
			c.advance(5 * time.Minute)

			if _, err = store.Get(t.Context(), session.ID); err != nil {
				break
			}
		}

		c.advance(10 * time.Minute)

		_, err = store.Get(t.Context(), session.ID)
		test.ErrorIs(t, err, ErrAbsoluteTimeout)
	})

	T.Run("refreshes the idle deadline once the touch interval has elapsed", func(t *testing.T) {
		t.Parallel()

		store, backend, c := newTestStore(t, WithIdleTimeout(10*time.Minute), WithTouchInterval(time.Minute))

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		// Inside the touch interval: read, but no write.
		c.advance(30 * time.Second)

		read, err := store.Get(t.Context(), session.ID)
		must.NoError(t, err)
		test.EqOp(t, 0, backend.callCount("Update"))
		test.EqOp(t, session.ExpiresAt, read.ExpiresAt)

		// Past it: the deadline moves.
		c.advance(time.Minute)

		read, err = store.Get(t.Context(), session.ID)
		must.NoError(t, err)
		test.EqOp(t, 1, backend.callCount("Update"))
		test.True(t, read.ExpiresAt.After(session.ExpiresAt))
	})

	// A touch that fails is not a session that ended. Failing the read would
	// sign a user out over a blip in the store.
	T.Run("serves the session when the touch cannot be written", func(t *testing.T) {
		t.Parallel()

		store, backend, c := newTestStore(t, WithIdleTimeout(10*time.Minute), WithTouchInterval(time.Minute))

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		backend.fail("Update", errors.New("store is down"))
		c.advance(2 * time.Minute)

		read, err := store.Get(t.Context(), session.ID)
		must.NoError(t, err)
		test.EqOp(t, "u_1", read.Data.UserID)
		// The old deadline stands, so the session still ends on its original
		// schedule rather than being extended by a write that did not happen.
		test.EqOp(t, session.ExpiresAt, read.ExpiresAt)
	})

	// Activity has to actually keep a session alive, or the idle timeout is an
	// absolute one wearing a disguise.
	T.Run("a session read regularly outlives its idle window", func(t *testing.T) {
		t.Parallel()

		store, _, c := newTestStore(t,
			WithAbsoluteTimeout(24*time.Hour), WithIdleTimeout(10*time.Minute), WithTouchInterval(time.Minute))

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		for range 10 {
			c.advance(9 * time.Minute)

			_, err = store.Get(t.Context(), session.ID)
			must.NoError(t, err)
		}
	})

	T.Run("discards a record written by another version", func(t *testing.T) {
		t.Parallel()

		store, backend, c := newTestStore(t)

		backend.entries["from-another-deploy"] = entry[principal]{
			record: &Record[principal]{
				CreatedAt:  c.Now(),
				LastSeenAt: c.Now(),
				Data:       &principal{UserID: "u_1"},
				Version:    recordVersion + 1,
			},
			expiresAt: c.Now().Add(time.Hour),
		}

		_, err := store.Get(t.Context(), "from-another-deploy")
		test.ErrorIs(t, err, ErrNotFound)
		// Removed rather than left to be re-read on every request until it
		// expires.
		test.SliceNotContains(t, backend.ids(), "from-another-deploy")
	})

	T.Run("surfaces a backend failure as itself", func(t *testing.T) {
		t.Parallel()

		store, backend, _ := newTestStore(t)
		errDown := errors.New("store is down")
		backend.fail("Load", errDown)

		_, err := store.Get(t.Context(), "anything")
		test.ErrorIs(t, err, errDown)
		// Emphatically not reported as an absent session: that would sign every
		// user out at once and look exactly like everybody's session expiring.
		test.False(t, stderrors.Is(err, ErrNotFound))
	})
}

func TestStore_Save(T *testing.T) {
	T.Parallel()

	T.Run("replaces the payload", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		must.NoError(t, store.Save(t.Context(), session.ID, &principal{UserID: "u_1", Admin: true}))

		read, err := store.Get(t.Context(), session.ID)
		must.NoError(t, err)
		test.True(t, read.Data.Admin)
	})

	T.Run("leaves the absolute deadline where it was", func(t *testing.T) {
		t.Parallel()

		store, _, c := newTestStore(t, WithAbsoluteTimeout(time.Hour), WithIdleTimeout(50*time.Minute))

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		c.advance(30 * time.Minute)
		must.NoError(t, store.Save(t.Context(), session.ID, &principal{UserID: "u_1", Admin: true}))

		read, err := store.Get(t.Context(), session.ID)
		must.NoError(t, err)
		test.EqOp(t, session.CreatedAt, read.CreatedAt)
		test.EqOp(t, session.CreatedAt.Add(time.Hour), read.ExpiresAt)
	})

	T.Run("refuses an expired session", func(t *testing.T) {
		t.Parallel()

		store, _, c := newTestStore(t, WithIdleTimeout(time.Minute))

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		c.advance(time.Minute)

		test.ErrorIs(t, store.Save(t.Context(), session.ID, &principal{UserID: "u_1"}), ErrExpired)
	})

	T.Run("refuses an unknown identifier", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)
		test.ErrorIs(t, store.Save(t.Context(), "nobody-minted-this", &principal{}), ErrNotFound)
	})
}

func TestStore_Renew(T *testing.T) {
	T.Parallel()

	T.Run("rotates the identifier and carries the payload across", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		session, err := store.New(t.Context(), &principal{UserID: "u_1", Admin: true})
		must.NoError(t, err)

		newID, err := store.Renew(t.Context(), session.ID)
		must.NoError(t, err)
		test.NotEqOp(t, session.ID, newID)

		read, err := store.Get(t.Context(), newID)
		must.NoError(t, err)
		test.EqOp(t, "u_1", read.Data.UserID)
		test.True(t, read.Data.Admin)
	})

	// The whole point: after renewal the identifier an attacker may hold is
	// worthless.
	T.Run("the old identifier stops resolving", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		_, err = store.Renew(t.Context(), session.ID)
		must.NoError(t, err)

		_, err = store.Get(t.Context(), session.ID)
		test.ErrorIs(t, err, ErrNotFound)
	})

	// If renewal reset CreatedAt, an application doing the correct thing —
	// renewing on every privilege change — would give its sessions an unbounded
	// life, and the absolute timeout would silently stop meaning anything.
	T.Run("does not extend the absolute deadline", func(t *testing.T) {
		t.Parallel()

		store, _, c := newTestStore(t, WithAbsoluteTimeout(time.Hour), WithIdleTimeout(55*time.Minute))

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		id := session.ID
		for range 3 {
			c.advance(15 * time.Minute)

			id, err = store.Renew(t.Context(), id)
			must.NoError(t, err)
		}

		c.advance(20 * time.Minute)

		_, err = store.Get(t.Context(), id)
		test.ErrorIs(t, err, ErrAbsoluteTimeout)
	})

	T.Run("refuses an expired session", func(t *testing.T) {
		t.Parallel()

		store, _, c := newTestStore(t, WithIdleTimeout(time.Minute))

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		c.advance(time.Minute)

		_, err = store.Renew(t.Context(), session.ID)
		test.ErrorIs(t, err, ErrExpired)
	})

	// A caller that sees an error has to assume the old identifier still works
	// and refuse the privilege change — which is only actionable if it did not
	// also receive a new identifier to carry on with.
	T.Run("returns no identifier when the rename fails", func(t *testing.T) {
		t.Parallel()

		store, backend, _ := newTestStore(t)

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		backend.fail("Rename", errors.New("store is down"))

		newID, err := store.Renew(t.Context(), session.ID)
		must.Error(t, err)
		test.EqOp(t, "", newID)
	})
}

func TestStore_Delete(T *testing.T) {
	T.Parallel()

	T.Run("ends the session", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		must.NoError(t, store.Delete(t.Context(), session.ID))

		_, err = store.Get(t.Context(), session.ID)
		test.ErrorIs(t, err, ErrNotFound)
	})

	// Sign-out is idempotent; a second click is not an error page.
	T.Run("an unknown identifier is not an error", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)
		must.NoError(t, store.Delete(t.Context(), "nobody-minted-this"))
	})

	T.Run("rejects an empty identifier", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)
		test.ErrorIs(t, store.Delete(t.Context(), ""), ErrIDRequired)
	})

	// The reason Backend.Update requires the record to exist: a request that
	// read a session just before sign-out must not write it back afterwards.
	T.Run("a concurrent save does not resurrect a signed-out session", func(t *testing.T) {
		t.Parallel()

		store, _, _ := newTestStore(t)

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		must.NoError(t, store.Delete(t.Context(), session.ID))

		test.ErrorIs(t, store.Save(t.Context(), session.ID, &principal{UserID: "u_1"}), ErrNotFound)

		_, err = store.Get(t.Context(), session.ID)
		test.ErrorIs(t, err, ErrNotFound)
	})
}

func TestStore_Close(T *testing.T) {
	T.Parallel()

	T.Run("closes the backend", func(t *testing.T) {
		t.Parallel()

		store, backend, _ := newTestStore(t)

		must.NoError(t, store.Close())
		test.True(t, backend.closed)
	})
}

package database

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/sessions"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// heldHolder is who these tests attribute sessions to.
func heldHolder() sessions.Holder {
	return sessions.Holder{Scope: tenancy.Of("acct_1"), Principal: "u_1"}
}

// store writes one attributed record and returns the identifier it went under.
func store(t *testing.T, backend *Backend[principal], c *fakeClock, holder sessions.Holder, id string) string {
	t.Helper()

	record := testHeldRecord(c, holder, sessions.Metadata{
		DeviceName:  "laptop-" + id,
		IPAddress:   "203.0.113.4",
		UserAgent:   "Mozilla/5.0",
		LoginMethod: "passkey",
	}, holder.Principal)

	must.NoError(t, backend.Create(t.Context(), id, record, time.Hour))

	return id
}

func TestBackend_ListHeld(T *testing.T) {
	T.Parallel()

	T.Run("round-trips the metadata", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)
		store(t, backend, c, heldHolder(), "s_1")

		held, err := backend.ListHeld(t.Context(), heldHolder())
		must.NoError(t, err)
		must.SliceLen(t, 1, held)

		test.EqOp(t, "s_1", held[0].ID)
		test.EqOp(t, heldHolder(), held[0].Record.Holder)
		test.EqOp(t, "laptop-s_1", held[0].Record.Metadata.DeviceName)
		test.EqOp(t, "203.0.113.4", held[0].Record.Metadata.IPAddress)
		test.EqOp(t, "Mozilla/5.0", held[0].Record.Metadata.UserAgent)
		test.EqOp(t, "passkey", held[0].Record.Metadata.LoginMethod)
		must.NotNil(t, held[0].Record.Data)
		test.EqOp(t, "u_1", held[0].Record.Data.UserID)
	})

	// The ORDER BY is the statement's, and it is what a security page renders
	// in: most recent first, with a deterministic tie-break so two sessions
	// established in the same instant come back the same way twice.
	T.Run("orders newest first", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		store(t, backend, c, heldHolder(), "s_old")
		c.advance(time.Minute)
		store(t, backend, c, heldHolder(), "s_new")

		held, err := backend.ListHeld(t.Context(), heldHolder())
		must.NoError(t, err)
		must.SliceLen(t, 2, held)

		test.EqOp(t, "s_new", held[0].ID)
		test.EqOp(t, "s_old", held[1].ID)
	})

	T.Run("does not cross scopes or principals", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		store(t, backend, c, heldHolder(), "mine")
		store(t, backend, c, sessions.Holder{Scope: tenancy.Of("acct_2"), Principal: "u_1"}, "other_tenant")
		store(t, backend, c, sessions.Holder{Scope: tenancy.Of("acct_1"), Principal: "u_2"}, "other_person")

		held, err := backend.ListHeld(t.Context(), heldHolder())
		must.NoError(t, err)
		must.SliceLen(t, 1, held)
		test.EqOp(t, "mine", held[0].ID)
	})

	// Whether a session is live is decided a layer up from the record's own
	// anchors, so this read hands back a row past its stored deadline rather
	// than hiding it — a row hidden here and answered by Load would be the two
	// clocks disagreeing.
	T.Run("applies no expiry of its own", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "s_1",
			testHeldRecord(c, heldHolder(), sessions.Metadata{}, "u_1"), time.Minute))

		c.advance(time.Hour)

		held, err := backend.ListHeld(t.Context(), heldHolder())
		must.NoError(t, err)
		must.SliceLen(t, 1, held)
	})

	T.Run("returns nothing for a holder with no rows", func(t *testing.T) {
		t.Parallel()

		backend, _ := newTestBackend(t)

		held, err := backend.ListHeld(t.Context(), heldHolder())
		must.NoError(t, err)
		test.SliceEmpty(t, held)
	})

	// A session established without a payload comes back as the nil it went in
	// as, exactly as Load reports it.
	T.Run("round-trips an absent payload as nil", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		record := testHeldRecord(c, heldHolder(), sessions.Metadata{}, "u_1")
		record.Data = nil

		must.NoError(t, backend.Create(t.Context(), "s_1", record, time.Hour))

		held, err := backend.ListHeld(t.Context(), heldHolder())
		must.NoError(t, err)
		must.SliceLen(t, 1, held)
		test.Nil(t, held[0].Record.Data)
	})
}

func TestBackend_DeleteHeld(T *testing.T) {
	T.Parallel()

	T.Run("removes the row and reports one", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)
		store(t, backend, c, heldHolder(), "s_1")

		affected, err := backend.DeleteHeld(t.Context(), heldHolder(), "s_1")
		must.NoError(t, err)
		test.EqOp(t, 1, affected)

		_, err = backend.Load(t.Context(), "s_1")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	// The holder is in the WHERE clause, so a caller naming somebody else's
	// session removes nothing — decided by the server at the instant the row
	// would have gone.
	T.Run("will not remove another holder's row", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)
		store(t, backend, c, sessions.Holder{Scope: tenancy.Of("acct_2"), Principal: "u_9"}, "theirs")

		affected, err := backend.DeleteHeld(t.Context(), heldHolder(), "theirs")
		must.NoError(t, err)
		test.EqOp(t, 0, affected)

		_, err = backend.Load(t.Context(), "theirs")
		must.NoError(t, err)
	})

	T.Run("reports none for a row that was already gone", func(t *testing.T) {
		t.Parallel()

		backend, _ := newTestBackend(t)

		affected, err := backend.DeleteHeld(t.Context(), heldHolder(), "nothing")
		must.NoError(t, err)
		test.EqOp(t, 0, affected)
	})
}

func TestBackend_DeleteAllHeld(T *testing.T) {
	T.Parallel()

	T.Run("removes every one of the holder's rows", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)
		store(t, backend, c, heldHolder(), "s_1")
		store(t, backend, c, heldHolder(), "s_2")
		store(t, backend, c, sessions.Holder{Scope: tenancy.Of("acct_1"), Principal: "u_2"}, "theirs")

		affected, err := backend.DeleteAllHeld(t.Context(), heldHolder(), "")
		must.NoError(t, err)
		test.EqOp(t, 2, affected)

		_, err = backend.Load(t.Context(), "theirs")
		must.NoError(t, err)
	})

	// The unset argument is what makes one statement serve both revocations,
	// and this is the half that spares a row.
	T.Run("spares the kept row", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)
		store(t, backend, c, heldHolder(), "kept")
		store(t, backend, c, heldHolder(), "gone")

		affected, err := backend.DeleteAllHeld(t.Context(), heldHolder(), "kept")
		must.NoError(t, err)
		test.EqOp(t, 1, affected)

		_, err = backend.Load(t.Context(), "kept")
		must.NoError(t, err)

		_, err = backend.Load(t.Context(), "gone")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	// A kept identifier the holder does not hold spares nothing of theirs:
	// sparing is by the same key the deletion is.
	T.Run("spares nothing for an identifier the holder does not hold", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)
		store(t, backend, c, heldHolder(), "s_1")

		affected, err := backend.DeleteAllHeld(t.Context(), heldHolder(), "not-theirs")
		must.NoError(t, err)
		test.EqOp(t, 1, affected)
	})
}

// TestAttribution_OverlongMetadata is the SQLite half of the width guarantee:
// the four device columns hold whatever a client said about itself, and nothing
// in the write path shortens it. The container suite makes the same assertion
// against a real Postgres and a real MySQL, which is where a narrowed column
// would show.
func TestAttribution_OverlongMetadata(T *testing.T) {
	T.Parallel()

	backend, c := newTestBackend(T)
	assertOverlongMetadataRoundTrips(T, backend, c, heldHolder(), "s_long")
}

// TestAttribution_SurvivesEveryWrite is the row-level half of "a session does
// not change hands": the two writes a live session takes must leave the holder
// and the metadata exactly as the create wrote them.
func TestAttribution_SurvivesEveryWrite(T *testing.T) {
	T.Parallel()

	metadata := sessions.Metadata{
		DeviceName:  "laptop",
		IPAddress:   "203.0.113.4",
		UserAgent:   "Mozilla/5.0",
		LoginMethod: "passkey",
	}

	T.Run("an update leaves them alone", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "s_1",
			testHeldRecord(c, heldHolder(), metadata, "u_1"), time.Hour))

		// A touch, written as the store writes one: the same record with a
		// later last_seen_at and no attribution at all.
		touched := testHeldRecord(c, sessions.Holder{}, sessions.Metadata{}, "u_1")
		must.NoError(t, backend.Update(t.Context(), "s_1", touched, time.Hour))

		read, err := backend.Load(t.Context(), "s_1")
		must.NoError(t, err)
		test.EqOp(t, heldHolder(), read.Holder)
		test.EqOp(t, metadata, read.Metadata)
	})

	T.Run("a rename carries them across", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		record := testHeldRecord(c, heldHolder(), metadata, "u_1")
		must.NoError(t, backend.Create(t.Context(), "old", record, time.Hour))

		must.NoError(t, backend.Rename(t.Context(), "old", "new", record, time.Hour))

		read, err := backend.Load(t.Context(), "new")
		must.NoError(t, err)
		test.EqOp(t, heldHolder(), read.Holder)
		test.EqOp(t, metadata, read.Metadata)

		held, err := backend.ListHeld(t.Context(), heldHolder())
		must.NoError(t, err)
		must.SliceLen(t, 1, held)
		test.EqOp(t, "new", held[0].ID)
	})
}

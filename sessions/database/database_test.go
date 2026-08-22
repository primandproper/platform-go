package database

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/sessions"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewBackend(T *testing.T) {
	T.Parallel()

	T.Run("requires a config", func(t *testing.T) {
		t.Parallel()

		_, err := NewBackend[principal](nil, newTestClient(t))
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("requires a client", func(t *testing.T) {
		t.Parallel()

		_, err := NewBackend[principal](&Config{}, nil)
		test.ErrorIs(t, err, ErrNilClient)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	// The prefix is interpolated into query text rather than bound, so it is
	// vetted against the schema before a single statement is rendered.
	T.Run("rejects a prefix the schema cannot render", func(t *testing.T) {
		t.Parallel()

		_, err := NewBackend[principal](&Config{TablePrefix: "trailing_"}, newTestClient(t))
		must.Error(t, err)

		_, err = NewBackend[principal](&Config{TablePrefix: "not an identifier"}, newTestClient(t))
		must.Error(t, err)
	})

	T.Run("takes its dialect from the client", func(t *testing.T) {
		t.Parallel()

		backend, err := NewBackend[principal](&Config{}, newTestClient(t))
		must.NoError(t, err)
		test.EqOp(t, dialect.SQLite, backend.dialect)
	})
}

func TestBackend_Load(T *testing.T) {
	T.Parallel()

	T.Run("round-trips a record", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)
		want := testRecord(c, "u_1")

		must.NoError(t, backend.Create(t.Context(), "id-1", want, time.Hour))

		got, err := backend.Load(t.Context(), "id-1")
		must.NoError(t, err)
		test.EqOp(t, want.CreatedAt, got.CreatedAt)
		test.EqOp(t, want.LastSeenAt, got.LastSeenAt)
		test.EqOp(t, want.Version, got.Version)
		test.EqOp(t, "u_1", got.Data.UserID)
	})

	T.Run("reports a missing row as a missing session", func(t *testing.T) {
		t.Parallel()

		backend, _ := newTestBackend(t)

		_, err := backend.Load(t.Context(), "never-written")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	// A session established without a payload comes back as the nil it went in
	// as, rather than as a zero value that looks like a real principal.
	T.Run("round-trips an absent payload as nil", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		record := testRecord(c, "")
		record.Data = nil

		must.NoError(t, backend.Create(t.Context(), "id-1", record, time.Hour))

		got, err := backend.Load(t.Context(), "id-1")
		must.NoError(t, err)
		test.Nil(t, got.Data)
	})

	// The row is returned whatever expires_at says. Expiry belongs to the
	// store, which evaluates it from the record's own anchors, so that both
	// backends answer the question identically.
	T.Run("returns a row the sweeper has not reached yet", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord(c, "u_1"), time.Minute))

		c.advance(time.Hour)

		_, err := backend.Load(t.Context(), "id-1")
		must.NoError(t, err)
	})

	// Undecodable is treated as absent rather than as a failure that would
	// repeat on every request carrying that identifier until it expired.
	T.Run("reports an undecodable payload as a missing session", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord(c, "u_1"), time.Hour))

		// Rewrite the blob as something the codec cannot read.
		_, err := backend.db.Writer().ExecContext(t.Context(),
			"UPDATE sessions SET data = ? WHERE id = ?", []byte{0xff, 0xff, 0xff, 0xff}, "id-1")
		must.NoError(t, err)

		_, err = backend.Load(t.Context(), "id-1")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})
}

func TestBackend_Create(T *testing.T) {
	T.Parallel()

	T.Run("stores a row", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord(c, "u_1"), time.Hour))

		_, err := backend.Load(t.Context(), "id-1")
		must.NoError(t, err)
	})

	// Reported without parsing a driver error, which is what the insert-ignore
	// clause is for.
	T.Run("reports a duplicate identifier", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord(c, "u_1"), time.Hour))

		test.ErrorIs(t,
			backend.Create(t.Context(), "id-1", testRecord(c, "u_2"), time.Hour),
			sessions.ErrIDConflict)

		// And the first record is untouched: a conflict is a refusal, not an
		// overwrite.
		got, err := backend.Load(t.Context(), "id-1")
		must.NoError(t, err)
		test.EqOp(t, "u_1", got.Data.UserID)
	})
}

func TestBackend_Update(T *testing.T) {
	T.Parallel()

	T.Run("overwrites an existing row", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord(c, "u_1"), time.Hour))

		c.advance(time.Minute)
		must.NoError(t, backend.Update(t.Context(), "id-1", testRecord(c, "u_2"), time.Hour))

		got, err := backend.Load(t.Context(), "id-1")
		must.NoError(t, err)
		test.EqOp(t, "u_2", got.Data.UserID)
	})

	// The guarantee the table buys over a cache: one statement, and a row that
	// is gone is not recreated.
	T.Run("refuses to resurrect a row that is gone", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord(c, "u_1"), time.Hour))
		must.NoError(t, backend.Delete(t.Context(), "id-1"))

		test.ErrorIs(t,
			backend.Update(t.Context(), "id-1", testRecord(c, "u_1"), time.Hour),
			sessions.ErrNotFound)

		_, err := backend.Load(t.Context(), "id-1")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	// created_at is not in the SET list, so no update can move the anchor the
	// absolute timeout is measured from.
	T.Run("leaves created_at alone", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		original := testRecord(c, "u_1")
		must.NoError(t, backend.Create(t.Context(), "id-1", original, time.Hour))

		c.advance(30 * time.Minute)

		// A record whose CreatedAt claims the session started later — which is
		// what a buggy caller, or a future refactor, would hand down.
		moved := testRecord(c, "u_1")
		must.NoError(t, backend.Update(t.Context(), "id-1", moved, time.Hour))

		got, err := backend.Load(t.Context(), "id-1")
		must.NoError(t, err)
		test.EqOp(t, original.CreatedAt, got.CreatedAt)
		test.EqOp(t, moved.LastSeenAt, got.LastSeenAt)
	})

	// MySQL reports zero rows affected for an update that changed nothing, so
	// an identical write must not be mistaken for a missing row.
	T.Run("a no-op rewrite is not a missing session", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		record := testRecord(c, "u_1")
		must.NoError(t, backend.Create(t.Context(), "id-1", record, time.Hour))
		must.NoError(t, backend.Update(t.Context(), "id-1", record, time.Hour))
	})

	T.Run("refuses an identifier that never existed", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		test.ErrorIs(t,
			backend.Update(t.Context(), "never-written", testRecord(c, "u_1"), time.Hour),
			sessions.ErrNotFound)
	})
}

func TestBackend_Rename(T *testing.T) {
	T.Parallel()

	T.Run("moves a row and retires the old identifier", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "old", testRecord(c, "u_1"), time.Hour))
		must.NoError(t, backend.Rename(t.Context(), "old", "new", testRecord(c, "u_1"), time.Hour))

		got, err := backend.Load(t.Context(), "new")
		must.NoError(t, err)
		test.EqOp(t, "u_1", got.Data.UserID)

		_, err = backend.Load(t.Context(), "old")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	T.Run("refuses an old identifier that holds nothing", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		test.ErrorIs(t,
			backend.Rename(t.Context(), "never-written", "new", testRecord(c, "u_1"), time.Hour),
			sessions.ErrNotFound)

		_, err := backend.Load(t.Context(), "new")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	// The transaction is the point: a rename that cannot complete leaves the
	// old identifier resolving rather than leaving the user with neither.
	T.Run("rolls back when the new identifier is taken", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "old", testRecord(c, "u_1"), time.Hour))
		must.NoError(t, backend.Create(t.Context(), "taken", testRecord(c, "u_2"), time.Hour))

		test.ErrorIs(t,
			backend.Rename(t.Context(), "old", "taken", testRecord(c, "u_1"), time.Hour),
			sessions.ErrIDConflict)

		got, err := backend.Load(t.Context(), "old")
		must.NoError(t, err)
		test.EqOp(t, "u_1", got.Data.UserID)

		got, err = backend.Load(t.Context(), "taken")
		must.NoError(t, err)
		test.EqOp(t, "u_2", got.Data.UserID)
	})
}

func TestBackend_Delete(T *testing.T) {
	T.Parallel()

	T.Run("removes a row", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord(c, "u_1"), time.Hour))
		must.NoError(t, backend.Delete(t.Context(), "id-1"))

		_, err := backend.Load(t.Context(), "id-1")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	T.Run("an absent row is not an error", func(t *testing.T) {
		t.Parallel()

		backend, _ := newTestBackend(t)
		must.NoError(t, backend.Delete(t.Context(), "never-written"))
	})
}

func TestBackend_Sweep(T *testing.T) {
	T.Parallel()

	T.Run("removes rows whose deadlines have passed and keeps the rest", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "short", testRecord(c, "u_1"), time.Minute))
		must.NoError(t, backend.Create(t.Context(), "long", testRecord(c, "u_2"), 24*time.Hour))

		c.advance(time.Hour)

		swept, err := backend.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), swept)

		_, err = backend.Load(t.Context(), "short")
		test.ErrorIs(t, err, sessions.ErrNotFound)

		_, err = backend.Load(t.Context(), "long")
		must.NoError(t, err)
	})

	T.Run("sweeping an empty table removes nothing", func(t *testing.T) {
		t.Parallel()

		backend, _ := newTestBackend(t)

		swept, err := backend.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), swept)
	})
}

func TestBackend_Codec(T *testing.T) {
	T.Parallel()

	T.Run("honors a supplied codec", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t, WithCodec(encoding.NewClientEncoder(encoding.ContentTypeJSON)))

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord(c, "u_1"), time.Hour))

		got, err := backend.Load(t.Context(), "id-1")
		must.NoError(t, err)
		test.EqOp(t, "u_1", got.Data.UserID)

		// The blob really is JSON, not the CBOR default — the property that
		// makes changing this on a live store a sign-out event.
		var raw []byte
		must.NoError(t, backend.db.Writer().
			QueryRowContext(t.Context(), "SELECT data FROM sessions WHERE id = ?", "id-1").Scan(&raw))
		test.StrContains(t, string(raw), `"UserID"`)
	})

	// Rows written with one encoding are unreadable through another, and there
	// is no version column that can rescue them — which is exactly why the
	// choice is documented as one to make once.
	T.Run("a record written with another codec reads as missing", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newFakeClock()

		writer, err := NewBackend[principal](&Config{}, client,
			WithClock(c), WithCodec(encoding.NewClientEncoder(encoding.ContentTypeJSON)))
		must.NoError(t, err)

		must.NoError(t, writer.Create(t.Context(), "id-1", testRecord(c, "u_1"), time.Hour))

		reader, err := NewBackend[principal](&Config{}, client, WithClock(c))
		must.NoError(t, err)

		_, err = reader.Load(t.Context(), "id-1")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})
}

func TestBackend_TablePrefix(T *testing.T) {
	T.Parallel()

	T.Run("reads and writes the namespaced table", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		createTable(t, client, dialect.SQLite, "ddb")

		backend, err := NewBackend[principal](&Config{TablePrefix: "ddb"}, client, WithClock(newFakeClock()))
		must.NoError(t, err)
		test.EqOp(t, "ddb_sessions", backend.table)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord(newFakeClock(), "u_1"), time.Hour))

		// Written to ddb_sessions and nowhere else, so an application sharing a
		// database cannot read another's sessions by accident.
		var count int
		must.NoError(t, client.Writer().
			QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sessions").Scan(&count))
		test.EqOp(t, 0, count)
	})
}

func TestBackend_UnderAStore(T *testing.T) {
	T.Parallel()

	T.Run("establishes, reads, renews, and ends a session", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		store, err := sessions.NewStore(backend, sessions.WithClock(c))
		must.NoError(t, err)

		session, err := store.New(t.Context(), &principal{UserID: "u_1", Admin: true})
		must.NoError(t, err)

		read, err := store.Get(t.Context(), session.ID)
		must.NoError(t, err)
		test.EqOp(t, "u_1", read.Data.UserID)
		test.True(t, read.Data.Admin)
		test.EqOp(t, session.CreatedAt, read.CreatedAt)

		renewed, err := store.Renew(t.Context(), session.ID)
		must.NoError(t, err)

		_, err = store.Get(t.Context(), session.ID)
		test.ErrorIs(t, err, sessions.ErrNotFound)

		must.NoError(t, store.Delete(t.Context(), renewed))
	})

	// The store's clock decides expiry, not the row's expires_at, so a
	// controlled clock produces a deterministic timeout.
	T.Run("expires on the store's clock", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		store, err := sessions.NewStore(backend,
			sessions.WithClock(c), sessions.WithIdleTimeout(10*time.Minute))
		must.NoError(t, err)

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		c.advance(10 * time.Minute)

		_, err = store.Get(t.Context(), session.ID)
		test.ErrorIs(t, err, sessions.ErrIdleTimeout)
	})
}

// exec is the one helper the tests reach into, so its contract is worth
// pinning: a statement that affects nothing reports zero rather than an error.
func TestBackend_exec(T *testing.T) {
	T.Parallel()

	T.Run("reports rows affected", func(t *testing.T) {
		t.Parallel()

		backend, c := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord(c, "u_1"), time.Hour))

		affected, err := backend.exec(t.Context(), backend.db.Writer(),
			"DELETE FROM sessions WHERE id = ?", []any{"id-1"})
		must.NoError(t, err)
		test.EqOp(t, int64(1), affected)

		affected, err = backend.exec(t.Context(), backend.db.Writer(),
			"DELETE FROM sessions WHERE id = ?", []any{"id-1"})
		must.NoError(t, err)
		test.EqOp(t, int64(0), affected)
	})

	T.Run("surfaces a statement failure", func(t *testing.T) {
		t.Parallel()

		backend, _ := newTestBackend(t)

		_, err := backend.exec(t.Context(), backend.db.Writer(), "SELEKT 1", nil)
		must.Error(t, err)
		test.False(t, stderrors.Is(err, context.Canceled))
	})
}

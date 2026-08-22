package database

import (
	stderrors "errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	"github.com/primandproper/platform-go/v13/authentication/webauthn/webauthntest"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestSessionStore_Conformance(T *testing.T) {
	T.Parallel()

	// The wall clock, not the fake one the rest of this file uses: the suite
	// asserts that state does not outlive its TTL by waiting, and a clock only
	// a test can move would make that case prove nothing.
	webauthntest.Run(T, func(tb testing.TB) webauthn.SessionStore {
		tb.Helper()

		store, err := NewSessionStore(&Config{}, newTestClient(tb))
		must.NoError(tb, err)

		return store
	})
}

func TestNewSessionStore(T *testing.T) {
	T.Parallel()

	T.Run("builds a store over a client", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(&Config{}, newTestClient(t))
		must.NoError(t, err)
		must.NotNil(t, store)
		test.EqOp(t, "webauthn_sessions", store.table)
	})

	T.Run("takes the table's namespace from the config", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(&Config{TablePrefix: "ddb"}, newTestClient(t))
		must.NoError(t, err)
		test.EqOp(t, "ddb_webauthn_sessions", store.table)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(nil, newTestClient(t))
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, store)
	})

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		// There is no default. A store with no database is a store that
		// silently loses every ceremony, and finding that out at the first
		// passkey login is finding it out from a user.
		store, err := NewSessionStore(&Config{}, nil)
		test.ErrorIs(t, err, ErrNilClient)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, store)
	})

	T.Run("refuses a prefix the schema cannot render", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(&Config{TablePrefix: "ddb_"}, newTestClient(t))
		test.Error(t, err)
		test.Nil(t, store)
	})
}

func TestSessionStore_Save(T *testing.T) {
	T.Parallel()

	T.Run("stamps the deadline from the store's clock", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)
		session := testSession("stamped")

		must.NoError(t, store.Save(t.Context(), session, time.Minute))

		// The row's deadline is the clock's now plus the TTL, not the deadline
		// the library stamped into the session: the two are computed by
		// different clocks, and only this one is the one Consume compares
		// against.
		test.EqOp(t, c.Now().UTC().Add(time.Minute), expiresAt(t, store, "stamped"))
	})

	T.Run("stores nothing for an unusable ceremony", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		ctx := t.Context()

		test.ErrorIs(t, store.Save(ctx, nil, time.Minute), webauthn.ErrNilSession)
		test.ErrorIs(t, store.Save(ctx, &webauthn.SessionData{}, time.Minute), webauthn.ErrChallengeRequired)
		test.ErrorIs(t, store.Save(ctx, testSession("unstored"), 0), webauthn.ErrNonPositiveTTL)

		test.EqOp(t, 0, rowCount(t, store))
	})

	T.Run("reports a write the server refuses", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		ctx := t.Context()

		_, err := store.db.Writer().ExecContext(ctx, "DROP TABLE webauthn_sessions")
		must.NoError(t, err)

		// A ceremony whose state was not stored cannot be finished, so the
		// caller has to hear about it before it hands the client its options.
		test.Error(t, store.Save(ctx, testSession("nowhere"), time.Minute))
	})

	T.Run("reports an encoding failure rather than storing nothing quietly", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t, WithCodec(encoding.NewClientEncoder("application/nonsense")))

		err := store.Save(t.Context(), testSession("unencodable"), time.Minute)
		test.Error(t, err)
		test.EqOp(t, 0, rowCount(t, store))
	})
}

func TestSessionStore_Consume(T *testing.T) {
	T.Parallel()

	T.Run("refuses state the clock has passed, and removes it", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)
		ctx := t.Context()

		must.NoError(t, store.Save(ctx, testSession("lapsed"), time.Minute))

		c.advance(time.Minute)

		// Exactly at the deadline is expired, matching the sweeper's
		// `expires_at <= now`: a row the sweeper would delete must not be one
		// Consume hands out.
		session, err := store.Consume(ctx, "lapsed")
		test.ErrorIs(t, err, webauthn.ErrSessionExpired)
		test.ErrorIs(t, err, webauthn.ErrSessionNotFound)
		test.Nil(t, session)

		// And it is gone rather than left for the sweeper, so a challenge that
		// is refused is also a challenge that is no longer there.
		test.EqOp(t, 0, rowCount(t, store))
	})

	T.Run("hands out state the clock has not passed", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)
		ctx := t.Context()

		must.NoError(t, store.Save(ctx, testSession("live"), time.Minute))

		c.advance(time.Minute - time.Microsecond)

		session, err := store.Consume(ctx, "live")
		must.NoError(t, err)
		test.EqOp(t, "live", session.Challenge)
	})

	T.Run("reports a row it cannot decode", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		ctx := t.Context()

		must.NoError(t, store.Save(ctx, testSession("corrupt"), time.Minute))

		_, err := store.db.Writer().ExecContext(ctx,
			"UPDATE webauthn_sessions SET session_data = ? WHERE challenge = ?", []byte("{not json"), "corrupt")
		must.NoError(t, err)

		// Not folded into ErrSessionNotFound. The caller's recourse is the same
		// either way — begin again — so nothing is bought by hiding a row that
		// went in one shape and came back another, and something is lost: this
		// is what a codec changed under a live deployment looks like.
		session, err := store.Consume(ctx, "corrupt")
		test.Error(t, err)
		test.False(t, stderrors.Is(err, webauthn.ErrSessionNotFound))
		test.Nil(t, session)
	})

	T.Run("reports a transaction that fails", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		ctx := t.Context()

		must.NoError(t, store.Save(ctx, testSession("gone-table"), time.Minute))
		_, err := store.db.Writer().ExecContext(ctx, "DROP TABLE webauthn_sessions")
		must.NoError(t, err)

		session, err := store.Consume(ctx, "gone-table")
		test.Error(t, err)
		test.False(t, stderrors.Is(err, webauthn.ErrSessionNotFound))
		test.Nil(t, session)
	})
}

func TestSessionStore_Sweep(T *testing.T) {
	T.Parallel()

	T.Run("removes what has expired and reports how much", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)
		ctx := t.Context()

		must.NoError(t, store.Save(ctx, testSession("short"), time.Minute))
		must.NoError(t, store.Save(ctx, testSession("long"), time.Hour))

		c.advance(time.Minute)

		swept, err := store.Sweep(ctx)
		must.NoError(t, err)
		test.EqOp(t, int64(1), swept)
		test.EqOp(t, 1, rowCount(t, store))

		// The live one is untouched and still consumable, which is the half of
		// a sweep that nothing else asserts.
		session, err := store.Consume(ctx, "long")
		must.NoError(t, err)
		test.EqOp(t, "long", session.Challenge)
	})

	T.Run("sweeps nothing when nothing has expired", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		must.NoError(t, store.Save(t.Context(), testSession("live"), time.Hour))

		swept, err := store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), swept)
	})

	T.Run("reports a failure rather than a count of zero", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		ctx := t.Context()

		_, err := store.db.Writer().ExecContext(ctx, "DROP TABLE webauthn_sessions")
		must.NoError(t, err)

		swept, err := store.Sweep(ctx)
		test.Error(t, err)
		test.EqOp(t, int64(0), swept)
	})
}

func TestSessionStore_dialect(T *testing.T) {
	T.Parallel()

	T.Run("refuses a client whose dialect it cannot speak", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(&Config{}, &unsupportedClient{newTestClient(t)})
		test.ErrorIs(t, err, dialect.ErrUnsupported)
		test.Nil(t, store)
	})
}

// unsupportedClient is a client that reports a dialect this package has no SQL
// for, which is the one thing a real client cannot be made to do.
type unsupportedClient struct {
	database.Client
}

func (c *unsupportedClient) Dialect() dialect.Dialect { return dialect.Dialect("cobol") }

// expiresAt reads the deadline column for a challenge, which is what Save
// stamps and nothing reads back.
func expiresAt(t *testing.T, store *SessionStore, challenge string) time.Time {
	t.Helper()

	var at time.Time
	must.NoError(t, store.db.Writer().
		QueryRowContext(t.Context(), "SELECT expires_at FROM webauthn_sessions WHERE challenge = ?", challenge).
		Scan(&at))

	return at.UTC()
}

// rowCount counts what is actually in the table, which is what a sweep changes
// and a Consume cannot see.
func rowCount(t *testing.T, store *SessionStore) int {
	t.Helper()

	var count int
	must.NoError(t, store.db.Writer().
		QueryRowContext(t.Context(), "SELECT COUNT(*) FROM webauthn_sessions").Scan(&count))

	return count
}

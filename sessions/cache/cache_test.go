package cache

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/cache/memory"
	cachemock "github.com/primandproper/platform-go/v13/cache/mock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/sessions"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// principal is the payload the tests store.
type principal struct {
	UserID string
}

// newTestBackend builds a backend over a fresh in-memory cache.
func newTestBackend(t *testing.T) sessions.Backend[principal] {
	t.Helper()

	c, err := memory.NewInMemoryCache[sessions.Record[principal]](time.Hour)
	must.NoError(t, err)

	backend, err := NewBackend(c)
	must.NoError(t, err)

	return backend
}

// testRecord is one live record.
func testRecord(userID string) *sessions.Record[principal] {
	now := time.Now().UTC()

	return &sessions.Record[principal]{
		CreatedAt:  now,
		LastSeenAt: now,
		Data:       &principal{UserID: userID},
		Version:    1,
	}
}

func TestNewBackend(T *testing.T) {
	T.Parallel()

	T.Run("requires a cache", func(t *testing.T) {
		t.Parallel()

		_, err := NewBackend[principal](nil)
		test.ErrorIs(t, err, ErrNilCache)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("builds over a cache", func(t *testing.T) {
		t.Parallel()

		must.NotNil(t, newTestBackend(t))
	})
}

func TestBackend_Load(T *testing.T) {
	T.Parallel()

	T.Run("round-trips a record", func(t *testing.T) {
		t.Parallel()

		backend := newTestBackend(t)
		want := testRecord("u_1")

		must.NoError(t, backend.Create(t.Context(), "id-1", want, time.Hour))

		got, err := backend.Load(t.Context(), "id-1")
		must.NoError(t, err)
		test.EqOp(t, want.CreatedAt, got.CreatedAt)
		test.EqOp(t, want.Version, got.Version)
		test.EqOp(t, "u_1", got.Data.UserID)
	})

	// The translation the whole Backend contract rests on: a Store must be able
	// to tell an absent session from a broken store without knowing which
	// backend it holds.
	T.Run("reports a cache miss as a missing session", func(t *testing.T) {
		t.Parallel()

		_, err := newTestBackend(t).Load(t.Context(), "never-written")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	// A read that answered "no session" during an outage would sign every user
	// out at once, which is the opposite of what a circuit breaker is for.
	T.Run("does not report an unavailable cache as a missing session", func(t *testing.T) {
		t.Parallel()

		c := &cachemock.CacheMock[sessions.Record[principal]]{
			GetFunc: func(context.Context, string) (*sessions.Record[principal], error) {
				return nil, cache.ErrUnavailable
			},
		}

		backend, err := NewBackend[principal](c)
		must.NoError(t, err)

		_, err = backend.Load(t.Context(), "id-1")
		test.ErrorIs(t, err, cache.ErrUnavailable)
		test.False(t, stderrors.Is(err, sessions.ErrNotFound))
	})

	// A cache that answers with no error and no value is not a session.
	T.Run("reports a nil value as a missing session", func(t *testing.T) {
		t.Parallel()

		c := &cachemock.CacheMock[sessions.Record[principal]]{
			GetFunc: func(context.Context, string) (*sessions.Record[principal], error) {
				return nil, nil
			},
		}

		backend, err := NewBackend[principal](c)
		must.NoError(t, err)

		_, err = backend.Load(t.Context(), "id-1")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})
}

func TestBackend_Create(T *testing.T) {
	T.Parallel()

	T.Run("stores a record for its ttl", func(t *testing.T) {
		t.Parallel()

		backend := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord("u_1"), time.Hour))

		_, err := backend.Load(t.Context(), "id-1")
		must.NoError(t, err)
	})

	// The cache provider's own expiry, exercised through the ttl the store
	// hands down rather than through the cache's configured default.
	T.Run("honors the ttl rather than the cache's default", func(t *testing.T) {
		t.Parallel()

		c, err := memory.NewInMemoryCache[sessions.Record[principal]](time.Hour)
		must.NoError(t, err)

		backend, err := NewBackend(c)
		must.NoError(t, err)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord("u_1"), time.Nanosecond))

		time.Sleep(time.Millisecond)

		_, err = backend.Load(t.Context(), "id-1")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})
}

func TestBackend_Update(T *testing.T) {
	T.Parallel()

	T.Run("overwrites an existing record", func(t *testing.T) {
		t.Parallel()

		backend := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord("u_1"), time.Hour))
		must.NoError(t, backend.Update(t.Context(), "id-1", testRecord("u_2"), time.Hour))

		got, err := backend.Load(t.Context(), "id-1")
		must.NoError(t, err)
		test.EqOp(t, "u_2", got.Data.UserID)
	})

	// The check that stops a signed-out session coming back. A request holding
	// a record it read before the sign-out must not be able to write it back.
	T.Run("refuses to resurrect a record that is gone", func(t *testing.T) {
		t.Parallel()

		backend := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord("u_1"), time.Hour))
		must.NoError(t, backend.Delete(t.Context(), "id-1"))

		test.ErrorIs(t,
			backend.Update(t.Context(), "id-1", testRecord("u_1"), time.Hour),
			sessions.ErrNotFound)

		_, err := backend.Load(t.Context(), "id-1")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	T.Run("refuses an identifier that never existed", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t,
			newTestBackend(t).Update(t.Context(), "never-written", testRecord("u_1"), time.Hour),
			sessions.ErrNotFound)
	})

	// The mechanism, not just the outcome. A read-then-write passes the two
	// tests above and still has the window this exists to close, so the thing
	// worth pinning is that the existence check and the write are one call.
	T.Run("is a single conditional write, with no separate read", func(t *testing.T) {
		t.Parallel()

		c := &cachemock.CacheMock[sessions.Record[principal]]{
			SetIfPresentFunc: func(context.Context, string, *sessions.Record[principal], ...cache.WriteOption) error {
				return nil
			},
		}

		backend, err := NewBackend[principal](c)
		must.NoError(t, err)

		must.NoError(t, backend.Update(t.Context(), "id-1", testRecord("u_1"), time.Hour))

		test.SliceLen(t, 1, c.SetIfPresentCalls())
		test.SliceLen(t, 0, c.GetCalls())
		test.SliceLen(t, 0, c.SetCalls())
	})

	// An outage must not read as a revoked session: one signs the user out, the
	// other is a retryable failure, and the store's caller branches on which.
	T.Run("keeps an unavailable cache distinct from a missing record", func(t *testing.T) {
		t.Parallel()

		c := &cachemock.CacheMock[sessions.Record[principal]]{
			SetIfPresentFunc: func(context.Context, string, *sessions.Record[principal], ...cache.WriteOption) error {
				return cache.ErrUnavailable
			},
		}

		backend, err := NewBackend[principal](c)
		must.NoError(t, err)

		err = backend.Update(t.Context(), "id-1", testRecord("u_1"), time.Hour)
		test.ErrorIs(t, err, cache.ErrUnavailable)
		test.False(t, stderrors.Is(err, sessions.ErrNotFound))
	})
}

func TestBackend_Rename(T *testing.T) {
	T.Parallel()

	T.Run("moves a record and retires the old identifier", func(t *testing.T) {
		t.Parallel()

		backend := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "old", testRecord("u_1"), time.Hour))
		must.NoError(t, backend.Rename(t.Context(), "old", "new", testRecord("u_1"), time.Hour))

		got, err := backend.Load(t.Context(), "new")
		must.NoError(t, err)
		test.EqOp(t, "u_1", got.Data.UserID)

		_, err = backend.Load(t.Context(), "old")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	T.Run("refuses an old identifier that holds nothing", func(t *testing.T) {
		t.Parallel()

		backend := newTestBackend(t)

		test.ErrorIs(t,
			backend.Rename(t.Context(), "never-written", "new", testRecord("u_1"), time.Hour),
			sessions.ErrNotFound)

		// And writes nothing under the new identifier, so a failed renewal
		// leaves no orphan behind.
		_, err := backend.Load(t.Context(), "new")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})
}

func TestBackend_Delete(T *testing.T) {
	T.Parallel()

	T.Run("removes a record", func(t *testing.T) {
		t.Parallel()

		backend := newTestBackend(t)

		must.NoError(t, backend.Create(t.Context(), "id-1", testRecord("u_1"), time.Hour))
		must.NoError(t, backend.Delete(t.Context(), "id-1"))

		_, err := backend.Load(t.Context(), "id-1")
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})

	// Sign-out is idempotent, so removing what is already gone is a success.
	T.Run("an absent record is not an error", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, newTestBackend(t).Delete(t.Context(), "never-written"))
	})
}

func TestBackend_Close(T *testing.T) {
	T.Parallel()

	T.Run("closes the cache", func(t *testing.T) {
		t.Parallel()

		closed := false
		c := &cachemock.CacheMock[sessions.Record[principal]]{
			CloseFunc: func() error {
				closed = true

				return nil
			},
		}

		backend, err := NewBackend[principal](c)
		must.NoError(t, err)

		must.NoError(t, backend.Close())
		test.True(t, closed)
	})
}

// The end-to-end property: a Store over this backend behaves like a session
// store, with the real expiry policy and real identifiers.
func TestBackend_UnderAStore(T *testing.T) {
	T.Parallel()

	T.Run("establishes, reads, renews, and ends a session", func(t *testing.T) {
		t.Parallel()

		store, err := sessions.NewStore(newTestBackend(t))
		must.NoError(t, err)

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		read, err := store.Get(t.Context(), session.ID)
		must.NoError(t, err)
		test.EqOp(t, "u_1", read.Data.UserID)

		renewed, err := store.Renew(t.Context(), session.ID)
		must.NoError(t, err)
		test.NotEqOp(t, session.ID, renewed)

		_, err = store.Get(t.Context(), session.ID)
		test.ErrorIs(t, err, sessions.ErrNotFound)

		must.NoError(t, store.Delete(t.Context(), renewed))

		_, err = store.Get(t.Context(), renewed)
		test.ErrorIs(t, err, sessions.ErrNotFound)
	})
}

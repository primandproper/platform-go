package cache

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	"github.com/primandproper/platform-go/v13/authentication/webauthn/webauthntest"
	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/cache/memory"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestSessionStore_Conformance(T *testing.T) {
	T.Parallel()

	// The deviation is declared rather than worked around: Consume here is a
	// read followed by a removal, so the case that proves one ceremony goes to
	// one consumer is skipped by name instead of passing by luck.
	webauthntest.Run(T, func(tb testing.TB) webauthn.SessionStore {
		tb.Helper()

		store, err := NewSessionStore(newTestCache(tb))
		must.NoError(tb, err)

		return store
	}, webauthntest.WithRacyConsume())
}

func TestNewSessionStore(T *testing.T) {
	T.Parallel()

	T.Run("builds a store over a cache", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(newTestCache(t),
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()))
		must.NoError(t, err)
		must.NotNil(t, store)
	})

	T.Run("refuses a nil cache", func(t *testing.T) {
		t.Parallel()

		// There is no default. Which cache this is decides whether the store
		// works across replicas at all, so it cannot be inferred.
		store, err := NewSessionStore(nil)
		test.ErrorIs(t, err, ErrNilCache)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, store)
	})

	T.Run("ignores a nil option", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(newTestCache(t), nil)
		must.NoError(t, err)
		must.NotNil(t, store)
	})
}

func TestSessionStore_Save(T *testing.T) {
	T.Parallel()

	T.Run("writes under the ceremony's own challenge", func(t *testing.T) {
		t.Parallel()

		c := newTestCache(t)
		store, err := NewSessionStore(c)
		must.NoError(t, err)

		must.NoError(t, store.Save(t.Context(), testSession("keyed"), time.Minute))

		// Keyed by the challenge and nothing else, which is what lets a Finish
		// on another replica find it from what the client echoes back.
		entry, getErr := c.Get(t.Context(), "keyed")
		must.NoError(t, getErr)
		must.NotNil(t, entry)
		test.EqOp(t, "keyed", entry.Challenge)
	})

	T.Run("reports a cache that refuses the write", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(&failingCache{Cache: newTestCache(t), setErr: errBoom})
		must.NoError(t, err)

		test.ErrorIs(t, store.Save(t.Context(), testSession("unwritable"), time.Minute), errBoom)
	})

	T.Run("stores nothing for an unusable ceremony", func(t *testing.T) {
		t.Parallel()

		c := newTestCache(t)
		store, err := NewSessionStore(c)
		must.NoError(t, err)

		ctx := t.Context()

		test.ErrorIs(t, store.Save(ctx, nil, time.Minute), webauthn.ErrNilSession)
		test.ErrorIs(t, store.Save(ctx, &webauthn.SessionData{}, time.Minute), webauthn.ErrChallengeRequired)
		test.ErrorIs(t, store.Save(ctx, testSession("unstored"), 0), webauthn.ErrNonPositiveTTL)

		_, getErr := c.Get(ctx, "unstored")
		test.ErrorIs(t, getErr, cache.ErrNotFound)
	})
}

func TestSessionStore_Consume(T *testing.T) {
	T.Parallel()

	T.Run("removes what it hands back", func(t *testing.T) {
		t.Parallel()

		c := newTestCache(t)
		store, err := NewSessionStore(c)
		must.NoError(t, err)

		ctx := t.Context()
		must.NoError(t, store.Save(ctx, testSession("once"), time.Minute))

		session, consumeErr := store.Consume(ctx, "once")
		must.NoError(t, consumeErr)
		test.EqOp(t, "once", session.Challenge)

		// The entry is gone from the cache itself, not merely from this
		// store's answer: a replay a moment later has nothing to find.
		_, getErr := c.Get(ctx, "once")
		test.ErrorIs(t, getErr, cache.ErrNotFound)
	})

	T.Run("reports a cache outage as an outage", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(&failingCache{Cache: newTestCache(t), getErr: errBoom})
		must.NoError(t, err)

		// Not folded into ErrSessionNotFound. A cache that is down would
		// otherwise report every ceremony as never begun, which reads as a
		// client bug rather than as the outage it is.
		session, consumeErr := store.Consume(t.Context(), "anything")
		test.ErrorIs(t, consumeErr, errBoom)
		test.False(t, stderrors.Is(consumeErr, webauthn.ErrSessionNotFound))
		test.Nil(t, session)
	})

	T.Run("refuses the ceremony when the removal fails", func(t *testing.T) {
		t.Parallel()

		c := newTestCache(t)
		store, err := NewSessionStore(&failingCache{Cache: c, deleteErr: errBoom})
		must.NoError(t, err)

		ctx := t.Context()
		must.NoError(t, store.Save(ctx, testSession("undeletable"), time.Minute))

		// A ceremony this store cannot remove is a challenge that could be
		// answered again, and the only thing that prevents that is refusing the
		// ceremony. The user retries; the replay does not.
		session, consumeErr := store.Consume(ctx, "undeletable")
		test.ErrorIs(t, consumeErr, errBoom)
		test.Nil(t, session)
	})

	// A provider that answers a miss with a nil value and a nil error would
	// otherwise hand the caller a nil session to dereference.
	T.Run("answers a nil entry as an absent one", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(&nilCache{Cache: newTestCache(t)})
		must.NoError(t, err)

		session, consumeErr := store.Consume(t.Context(), "nil-entry")
		test.ErrorIs(t, consumeErr, webauthn.ErrSessionNotFound)
		test.Nil(t, session)
	})

	T.Run("refuses an empty challenge rather than looking one up", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(newTestCache(t))
		must.NoError(t, err)

		_, consumeErr := store.Consume(t.Context(), "")
		test.ErrorIs(t, consumeErr, webauthn.ErrChallengeRequired)
	})
}

// errBoom is the failure the cache doubles below inject.
var errBoom = platformerrors.New("cache is having a day")

// failingCache fails whichever operation it was given an error for, and defers
// to the real cache for the rest.
type failingCache struct {
	cache.Cache[webauthn.SessionData]

	setErr    error
	getErr    error
	deleteErr error
}

func (c *failingCache) Set(
	ctx context.Context,
	key string,
	value *webauthn.SessionData,
	opts ...cache.WriteOption,
) error {
	if c.setErr != nil {
		return c.setErr
	}

	return c.Cache.Set(ctx, key, value, opts...)
}

func (c *failingCache) Get(ctx context.Context, key string) (*webauthn.SessionData, error) {
	if c.getErr != nil {
		return nil, c.getErr
	}

	return c.Cache.Get(ctx, key)
}

func (c *failingCache) Delete(ctx context.Context, key string) error {
	if c.deleteErr != nil {
		return c.deleteErr
	}

	return c.Cache.Delete(ctx, key)
}

// nilCache reports a miss the way a provider that returns no error might.
type nilCache struct {
	cache.Cache[webauthn.SessionData]
}

func (c *nilCache) Get(context.Context, string) (*webauthn.SessionData, error) {
	return nil, nil
}

// newTestCache is a memory cache, which is what this store is for in tests and
// only in tests: a memory cache is per-process, so two replicas do not share a
// ceremony.
func newTestCache(tb testing.TB) cache.Cache[webauthn.SessionData] {
	tb.Helper()

	c, err := memory.NewInMemoryCache[webauthn.SessionData](time.Minute)
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = c.Close() })

	return c
}

// testSession is one ceremony's worth of state.
func testSession(challenge string) *webauthn.SessionData {
	return &webauthn.SessionData{
		Challenge:            challenge,
		RelyingPartyID:       "example.com",
		UserID:               []byte("user-handle"),
		AllowedCredentialIDs: [][]byte{[]byte("credential-one")},
		UserVerification:     protocol.VerificationPreferred,
		Expires:              time.Now().UTC().Add(time.Minute),
	}
}

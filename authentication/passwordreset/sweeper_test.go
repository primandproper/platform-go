package passwordreset

import (
	"context"
	"testing"
	"time"

	loggingnoop "github.com/primandproper/primitives-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/primitives-go/observability/tracing/noop"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestSQLStore_Sweep(T *testing.T) {
	T.Parallel()

	T.Run("removes what is past its deadline and nothing else", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		short := issue(t, store, time.Minute)
		long := issue(t, store, 48*time.Hour)

		c.advance(time.Hour)

		swept, err := store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), swept)

		_, err = verify(t, store, testScope(), short.Secret)
		test.ErrorIs(t, err, ErrTokenNotFound)

		_, err = verify(t, store, testScope(), long.Secret)
		test.NoError(t, err)
	})

	// A redeemed row goes at its own expiry rather than at its redemption, which
	// is what keeps "this link has already been used" answerable for the life of
	// the link.
	T.Run("keeps a redeemed row until it expires", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		issuance := issue(t, store, time.Hour)

		_, err := consume(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)

		swept, err := store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), swept)

		_, err = verify(t, store, testScope(), issuance.Secret)
		test.ErrorIs(t, err, ErrTokenRedeemed)

		c.advance(2 * time.Hour)

		swept, err = store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), swept)

		_, err = verify(t, store, testScope(), issuance.Secret)
		test.ErrorIs(t, err, ErrTokenNotFound)
	})

	// The sweep is the component's own machinery, and the narrow exception to
	// the doctrine: it reclaims one table for the whole deployment.
	T.Run("spans every scope", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		_, err := issueFor(t, store, testScope(), testUserID, time.Minute)
		must.NoError(t, err)

		_, err = issueFor(t, store, tenancy.Of("tenant_b"), testUserID, time.Minute)
		must.NoError(t, err)

		_, err = issueFor(t, store, tenancy.Global(), testUserID, time.Minute)
		must.NoError(t, err)

		c.advance(time.Hour)

		swept, err := store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(3), swept)
	})

	T.Run("with nothing to sweep", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		swept, err := store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), swept)
	})

	T.Run("with a closed database", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		must.NoError(t, store.db.Close())

		swept, err := store.Sweep(t.Context())
		test.EqOp(t, int64(0), swept)
		test.Error(t, err)
	})
}

func TestSQLStore_sweepEvery(T *testing.T) {
	T.Parallel()

	T.Run("sweeps on every tick until its context is done", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		store, c := newTestStore(t, WithSweeper(ctx, time.Minute))

		issuance := issue(t, store, time.Minute)
		c.advance(time.Hour)

		c.tick()

		// The tick is delivered synchronously, but the sweep it triggers runs on
		// the loop's goroutine; a second tick blocks until the first is drained,
		// which is what makes the first one's work observable.
		c.tick()

		_, err := verify(t, store, testScope(), issuance.Secret)
		test.ErrorIs(t, err, ErrTokenNotFound)
	})

	// The loop's only effect on a failure is the line it logs, so a loop that
	// stopped logging would fail silently.
	T.Run("logs a failing sweep and keeps going", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		logger := newRecordingLogger()

		store, c := newTestStore(t, WithSweeper(ctx, time.Minute), WithLogger(logger))
		must.NoError(t, store.db.Close())

		c.tick()
		c.tick()

		test.Positive(t, logger.count(backgroundSweepFailure))
	})

	T.Run("starts nothing when it was not asked to", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(&Config{}, newTestClient(t), WithSweeper(nil, time.Minute), //nolint:staticcheck // the nil context is the case under test
			WithLogger(loggingnoop.NewLogger()), WithTracerProvider(tracingnoop.NewTracerProvider()))
		must.NoError(t, err)
		must.NotNil(t, store)
	})
}

package webauthncredentials

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestWithSweeper(T *testing.T) {
	T.Parallel()

	// Two clocks, and each drives one half of the loop. The bubble's is what a
	// ticker fires on, so time.Sleep is what makes a sweep happen; the fake
	// clock is what stamps the deadline, and the sweep compares that against
	// the server's own clock rather than either of them. So a row is made
	// expired by writing it from a clock behind the server, not by sleeping —
	// which is the difference the port introduced, and the reason a bubble that
	// sleeps for an hour still finds a live row live.
	T.Run("removes expired rows with no Consume to discover them", func(t *testing.T) {
		t.Parallel()

		// Built outside the bubble, deliberately: inside one, time.Now reads the
		// bubble's clock rather than the wall's, and a deadline stamped from
		// that is decades behind the server the sweep compares against.
		c := newFakeClock()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t)

			store, err := NewSessionStore(&Config{}, client,
				WithClock(c), WithSweeper(t.Context(), 10*time.Second))
			must.NoError(t, err)

			// Stamped by a clock two minutes behind the server's, so the row is
			// past its deadline the moment it is written. This is the ceremony
			// nobody finished, which is the row nothing else deletes.
			c.advance(-2 * time.Minute)
			must.NoError(t, store.Save(t.Context(), testSession("abandoned"), time.Minute))
			c.advance(2 * time.Minute)

			test.EqOp(t, 1, rowCount(t, store))

			// Across at least one tick.
			time.Sleep(20 * time.Second)
			synctest.Wait()

			test.EqOp(t, 0, rowCount(t, store))
		})
	})

	T.Run("leaves live rows alone", func(t *testing.T) {
		t.Parallel()

		// Built outside the bubble, for the reason the test above gives.
		c := newFakeClock()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t)

			store, err := NewSessionStore(&Config{}, client,
				WithClock(c), WithSweeper(t.Context(), time.Second))
			must.NoError(t, err)

			must.NoError(t, store.Save(t.Context(), testSession("live"), time.Hour))

			time.Sleep(time.Minute)
			synctest.Wait()

			test.EqOp(t, 1, rowCount(t, store))
		})
	})

	// The goroutine's life is the caller's to bound. A sweeper that outlived
	// the scope that started it would keep a client alive after Close.
	T.Run("stops when its context is done", func(t *testing.T) {
		t.Parallel()

		// Built outside the bubble, for the reason the first test above gives.
		c := newFakeClock()

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())

			client := newTestClient(t)

			store, err := NewSessionStore(&Config{}, client, WithClock(c), WithSweeper(ctx, time.Second))
			must.NoError(t, err)

			cancel()
			synctest.Wait()

			// Already past its deadline, so a sweeper still running would take
			// it — which is what makes the row surviving an assertion about the
			// goroutine rather than about the row.
			c.advance(-time.Hour)
			must.NoError(t, store.Save(t.Context(), testSession("orphan"), time.Second))
			c.advance(time.Hour)

			time.Sleep(time.Minute)
			synctest.Wait()

			// Still there, because nothing is sweeping any more.
			test.EqOp(t, 1, rowCount(t, store))
		})
	})

	// A sweep is the one thing here with nobody waiting on it, so a failure has
	// two ways to disappear: unlogged, and by taking the goroutine with it.
	T.Run("logs a failed sweep and keeps sweeping", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t)
			logger := newRecordingLogger()

			_, err := NewSessionStore(&Config{}, client,
				WithLogger(logger), WithSweeper(t.Context(), time.Second))
			must.NoError(t, err)

			_, err = client.Writer().ExecContext(t.Context(), "DROP TABLE webauthn_sessions")
			must.NoError(t, err)

			time.Sleep(3 * time.Second)
			synctest.Wait()

			// More than one, because one would also be what a sweeper that died
			// on its first failure produced.
			logged := logger.count(backgroundSweepFailure)
			test.True(t, logged > 1, test.Sprintf("logged %d failures", logged))
		})
	})

	T.Run("starts nothing without a context or an interval", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithSweeper(nil, time.Second)}) //nolint:staticcheck // deliberate nil context
		must.Nil(t, o.sweepCtx)

		o = newOptions([]Option{WithSweeper(context.Background(), 0)})
		must.Nil(t, o.sweepCtx)
	})
}

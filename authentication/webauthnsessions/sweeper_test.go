package webauthnsessions

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
	// clock is what stamps the deadline and what the sweep compares against, so
	// advancing it is what makes a row expired. Sleeping the bubble alone never
	// does — which is why a row here is stamped live, the fake clock is moved
	// past its deadline, and only then does the bubble sleep across a tick.
	T.Run("removes expired rows with no Consume to discover them", func(t *testing.T) {
		t.Parallel()

		// Built outside the bubble, deliberately: inside one, time.Now reads the
		// bubble's clock rather than the wall's, and every row would be stamped
		// decades before any database this test runs against.
		c := newFakeClock()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t)

			store, err := NewSessionStore(&Config{}, client,
				WithClock(c), WithSweeper(t.Context(), 10*time.Second))
			must.NoError(t, err)

			// The ceremony nobody finished, which is the row nothing else
			// deletes: written live, and then left behind by a clock that has
			// moved past its deadline.
			must.NoError(t, store.Save(t.Context(), testSession("abandoned"), time.Minute))
			c.advance(2 * time.Minute)

			test.EqOp(t, 1, rowCount(t, store))

			// Across at least one tick.
			time.Sleep(20 * time.Second)
			synctest.Wait()

			test.EqOp(t, 0, rowCount(t, store))
		})
	})

	// The bubble sleeps for a minute while the fake clock stays put, which is the
	// skewed case as the loop sees it: the deadline and the horizon are one clock's
	// readings, so a wall clock running ahead of that one reclaims nothing.
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

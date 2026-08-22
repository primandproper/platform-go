package database

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/platform-go/v13/sessions"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestWithSweeper(T *testing.T) {
	T.Parallel()

	// The wall clock is deliberate rather than the fake one the other tests
	// use: inside a synctest bubble clock.NewClock reads the bubble's time, so
	// the sweeper's ticker advances with time.Sleep and needs no test double.
	T.Run("removes expired rows with no read to discover them", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t)

			backend, err := NewBackend[principal](&Config{}, client, WithSweeper(t.Context(), 10*time.Second))
			must.NoError(t, err)

			must.NoError(t, backend.Create(t.Context(), "id-1", wallRecord(), time.Minute))
			test.EqOp(t, 1, rowCount(t, backend))

			// Past the row's deadline and across at least one tick.
			time.Sleep(time.Minute + 10*time.Second)
			synctest.Wait()

			test.EqOp(t, 0, rowCount(t, backend))
		})
	})

	T.Run("leaves live rows alone", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t)

			backend, err := NewBackend[principal](&Config{}, client, WithSweeper(t.Context(), time.Second))
			must.NoError(t, err)

			must.NoError(t, backend.Create(t.Context(), "id-1", wallRecord(), time.Hour))

			time.Sleep(time.Minute)
			synctest.Wait()

			test.EqOp(t, 1, rowCount(t, backend))
		})
	})

	// The goroutine's life is the caller's to bound. A sweeper that outlived
	// the scope that started it would keep a client alive after Close.
	T.Run("stops when its context is done", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())

			client := newTestClient(t)

			backend, err := NewBackend[principal](&Config{}, client, WithSweeper(ctx, time.Second))
			must.NoError(t, err)

			cancel()
			synctest.Wait()

			must.NoError(t, backend.Create(t.Context(), "id-1", wallRecord(), time.Second))

			time.Sleep(time.Minute)
			synctest.Wait()

			// Still there, because nothing is sweeping any more.
			test.EqOp(t, 1, rowCount(t, backend))
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

// wallRecord is a record stamped from the wall clock, for the synctest bubbles
// above where the fake clock is not in play.
func wallRecord() *sessions.Record[principal] {
	now := time.Now().UTC().Truncate(time.Microsecond)

	return &sessions.Record[principal]{
		CreatedAt:  now,
		LastSeenAt: now,
		Data:       &principal{UserID: "u_1"},
		Version:    1,
	}
}

// rowCount counts what is actually in the table, which is what a sweeper
// changes and a Load cannot see.
func rowCount(t *testing.T, backend *Backend[principal]) int {
	t.Helper()

	var count int
	must.NoError(t, backend.db.Writer().
		QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sessions").Scan(&count))

	return count
}

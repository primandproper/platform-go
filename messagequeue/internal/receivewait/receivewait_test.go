package receivewait

import (
	"context"
	"testing"
	"time"

	clockmock "github.com/primandproper/platform-go/v13/clock/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// sleepRecorder is a clock whose Sleep returns instantly and remembers what it
// was asked to wait.
func sleepRecorder() (*clockmock.ClockMock, *[]time.Duration) {
	var slept []time.Duration

	return &clockmock.ClockMock{
		SleepFunc: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)

			return nil
		},
	}, &slept
}

// noJitter draws the top of the equal-jitter window, so a wait is exactly its
// scheduled delay and the schedule can be asserted rather than bounded.
func noJitter() float64 { return 1 }

func TestBackoff_Wait(T *testing.T) {
	T.Parallel()

	T.Run("grows from the floor and stops at the ceiling", func(t *testing.T) {
		t.Parallel()

		c, slept := sleepRecorder()
		b := New(c, noJitter)

		for range 12 {
			must.NoError(t, b.Wait(t.Context()))
		}

		test.EqOp(t, 100*time.Millisecond, (*slept)[0])
		test.EqOp(t, 200*time.Millisecond, (*slept)[1])
		test.EqOp(t, 400*time.Millisecond, (*slept)[2])

		// Clamped rather than doubling without bound: polling a deleted queue
		// four times a second for an hour helps nobody, and neither does one
		// scheduled a week out.
		test.EqOp(t, 30*time.Second, (*slept)[11])
	})

	// Equal jitter, not full: a loop that has backed off to thirty seconds must
	// not be able to draw a near-zero wait and become hot again.
	T.Run("every wait keeps at least half its schedule", func(t *testing.T) {
		t.Parallel()

		c, slept := sleepRecorder()
		b := New(c, func() float64 { return 0 })

		for range 12 {
			must.NoError(t, b.Wait(t.Context()))
		}

		test.EqOp(t, 50*time.Millisecond, (*slept)[0])
		test.EqOp(t, 15*time.Second, (*slept)[11])
	})

	// A queue that fails once an hour must not accumulate its way to the
	// ceiling: the wait describes the current failure, not the lifetime.
	T.Run("Reset returns the wait to its floor", func(t *testing.T) {
		t.Parallel()

		c, slept := sleepRecorder()
		b := New(c, noJitter)

		must.NoError(t, b.Wait(t.Context()))
		must.NoError(t, b.Wait(t.Context()))

		b.Reset()

		must.NoError(t, b.Wait(t.Context()))

		test.EqOp(t, 100*time.Millisecond, (*slept)[2])
	})

	T.Run("reports a context that went away underneath it", func(t *testing.T) {
		t.Parallel()

		sentinel := context.Canceled

		b := New(&clockmock.ClockMock{
			SleepFunc: func(context.Context, time.Duration) error { return sentinel },
		}, noJitter)

		test.ErrorIs(t, b.Wait(t.Context()), sentinel)
	})

	// A nil clock and a nil source both have to work: the consumers build one
	// with neither.
	T.Run("resolves an absent clock and source", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		must.Error(t, New(nil, nil).Wait(ctx))
	})
}

package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestAwaitDeadline(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

	t.Run("no budget named is the default", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, now.Add(DefaultFulfillmentBudget), awaitDeadline(now, 0, time.Time{}, false))
	})

	t.Run("a named budget is the subject's", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, now.Add(90*time.Second), awaitDeadline(now, 90*time.Second, time.Time{}, false))
	})

	t.Run("a test deadline sooner than the budget wins, short by the grace", func(t *testing.T) {
		t.Parallel()

		testDeadline := now.Add(30 * time.Second)

		test.EqOp(t, testDeadline.Add(-awaitGrace), awaitDeadline(now, time.Minute, testDeadline, true))
	})

	t.Run("a test deadline later than the budget leaves it alone", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, now.Add(time.Minute), awaitDeadline(now, time.Minute, now.Add(time.Hour), true))
	})
}

func TestPoll(t *testing.T) {
	t.Parallel()

	t.Run("it returns once the probe is done", func(t *testing.T) {
		t.Parallel()

		asked := 0
		err := poll(t.Context(), time.Now().Add(time.Minute), time.Millisecond, func() (bool, error) {
			asked++

			return asked == 3, nil
		})

		must.NoError(t, err)
		test.EqOp(t, 3, asked)
	})

	t.Run("a probe's error ends the wait with it", func(t *testing.T) {
		t.Parallel()

		broken := errors.New("broken probe")
		err := poll(t.Context(), time.Now().Add(time.Minute), time.Millisecond, func() (bool, error) {
			return false, broken
		})

		test.ErrorIs(t, err, broken)
	})

	t.Run("a spent budget is reported as one", func(t *testing.T) {
		t.Parallel()

		asked := 0
		err := poll(t.Context(), time.Now().Add(20*time.Millisecond), time.Millisecond, func() (bool, error) {
			asked++

			return false, nil
		})

		test.True(t, platformerrors.Is(err, errBudgetSpent))
		test.Greater(t, 1, asked, test.Sprint("the probe was not asked again before the budget ran out"))
	})

	t.Run("a deadline already behind it still asks once", func(t *testing.T) {
		t.Parallel()

		asked := 0
		err := poll(t.Context(), time.Now().Add(-time.Second), time.Millisecond, func() (bool, error) {
			asked++

			return true, nil
		})

		must.NoError(t, err)
		test.EqOp(t, 1, asked)
	})

	t.Run("a cancelled context ends the wait", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := poll(ctx, time.Now().Add(time.Minute), time.Hour, func() (bool, error) { return false, nil })

		test.ErrorIs(t, err, context.Canceled)
	})
}

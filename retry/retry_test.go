package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestExponentialBackoffPolicy_Execute(T *testing.T) {
	T.Parallel()

	T.Run("success on first attempt", func(t *testing.T) {
		t.Parallel()

		policy := NewExponentialBackoffPolicy(Config{MaxAttempts: 3})
		ctx := context.Background()
		attempts := 0

		err := policy.Execute(ctx, func(ctx context.Context) error {
			attempts++
			return nil
		})

		must.NoError(t, err)
		test.EqOp(t, 1, attempts)
	})

	T.Run("success after retries", func(t *testing.T) {
		t.Parallel()

		policy := NewExponentialBackoffPolicy(Config{
			MaxAttempts:  5,
			InitialDelay: 1,
			MaxDelay:     10,
			UseJitter:    false,
		})
		ctx := context.Background()
		attempts := 0

		err := policy.Execute(ctx, func(ctx context.Context) error {
			attempts++
			if attempts < 3 {
				return errors.New("transient")
			}
			return nil
		})

		must.NoError(t, err)
		test.EqOp(t, 3, attempts)
	})

	T.Run("returns last error after max attempts", func(t *testing.T) {
		t.Parallel()

		policy := NewExponentialBackoffPolicy(Config{
			MaxAttempts:  3,
			InitialDelay: 1,
			MaxDelay:     10,
			UseJitter:    false,
		})
		ctx := context.Background()
		attempts := 0
		expectedErr := errors.New("final failure")

		err := policy.Execute(ctx, func(ctx context.Context) error {
			attempts++
			if attempts < 3 {
				return errors.New("transient")
			}
			return expectedErr
		})

		must.Error(t, err)
		test.ErrorIs(t, err, expectedErr)
		test.EqOp(t, 3, attempts)
	})

	T.Run("stops immediately on a canceled context error", func(t *testing.T) {
		t.Parallel()

		policy := NewExponentialBackoffPolicy(Config{
			MaxAttempts:  5,
			InitialDelay: time.Millisecond,
			MaxDelay:     10 * time.Millisecond,
		})
		attempts := 0

		err := policy.Execute(context.Background(), func(ctx context.Context) error {
			attempts++
			return context.Canceled
		})

		test.ErrorIs(t, err, context.Canceled)
		// Retrying a canceled context is pointless; it must not burn all 5 attempts.
		test.EqOp(t, 1, attempts)
	})

	T.Run("stops immediately on an Unretryable error", func(t *testing.T) {
		t.Parallel()

		policy := NewExponentialBackoffPolicy(Config{
			MaxAttempts:  5,
			InitialDelay: time.Millisecond,
			MaxDelay:     10 * time.Millisecond,
		})
		attempts := 0
		underlying := errors.New("fatal")

		err := policy.Execute(context.Background(), func(ctx context.Context) error {
			attempts++
			return Unretryable(underlying)
		})

		test.ErrorIs(t, err, ErrUnretryable)
		test.ErrorIs(t, err, underlying)
		test.EqOp(t, 1, attempts)
	})

	// Regression: a sub-2ns InitialDelay makes int64(delay)/2 truncate to 0, and
	// rand.Int64N(0) panics. With jitter enabled this would crash on the first
	// backoff instead of retrying.
	T.Run("does not panic when jitter delay is too small to halve", func(t *testing.T) {
		t.Parallel()

		policy := NewExponentialBackoffPolicy(Config{
			MaxAttempts:  3,
			InitialDelay: 1,
			MaxDelay:     10,
			UseJitter:    true,
		})
		ctx := context.Background()
		attempts := 0

		err := policy.Execute(ctx, func(ctx context.Context) error {
			attempts++
			return errors.New("transient")
		})

		must.Error(t, err)
		test.EqOp(t, 3, attempts)
	})

	T.Run("respects context cancellation", func(t *testing.T) {
		t.Parallel()

		policy := NewExponentialBackoffPolicy(Config{
			MaxAttempts:  10,
			InitialDelay: time.Hour,
			UseJitter:    false,
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := policy.Execute(ctx, func(ctx context.Context) error {
			return errors.New("fail")
		})

		must.Error(t, err)
		test.ErrorIs(t, err, context.Canceled)
	})
}

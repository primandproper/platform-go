package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// ErrUnretryable marks an error as one that Execute must not retry. Wrap a
// returned error with Unretryable (or return anything that wraps ErrUnretryable)
// to stop the retry loop immediately instead of exhausting the remaining attempts.
var ErrUnretryable = errors.New("unretryable")

// Unretryable wraps err so Execute stops retrying on it. The original error is
// preserved in the chain, so errors.Is/As against it still work.
func Unretryable(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%w: %w", ErrUnretryable, err)
}

// isTerminal reports whether an operation error should abort the retry loop
// rather than trigger another attempt: a canceled/expired context (retrying can
// never succeed) or an explicitly non-retryable error.
func isTerminal(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrUnretryable)
}

// Policy executes operations with retry logic.
type Policy interface {
	Execute(ctx context.Context, operation func(ctx context.Context) error) error
}

// exponentialBackoff implements Policy with configurable exponential backoff.
type exponentialBackoff struct {
	config Config
}

// NewExponentialBackoffPolicy returns a Policy that retries with exponential backoff.
func NewExponentialBackoffPolicy(cfg Config) Policy {
	cfg.EnsureDefaults()
	return &exponentialBackoff{config: cfg}
}

// Execute runs the operation, retrying on failure up to MaxAttempts times.
func (e *exponentialBackoff) Execute(ctx context.Context, operation func(ctx context.Context) error) error {
	var lastErr error
	delay := e.config.InitialDelay

	for attempt := uint(0); attempt < e.config.MaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return lastErr
			}
			return ctx.Err()
		default:
		}

		lastErr = operation(ctx)
		if lastErr == nil {
			return nil
		}

		// A canceled/expired context or an explicitly non-retryable error can never
		// be resolved by another attempt — return immediately instead of sleeping and
		// burning the remaining attempts.
		if isTerminal(lastErr) {
			return lastErr
		}

		if attempt == e.config.MaxAttempts-1 {
			return lastErr
		}

		sleepDuration := delay
		// half > 0 guards rand.Int64N, which panics on a non-positive argument
		// (e.g. a sub-2ns delay where int64(delay)/2 truncates to 0). When the
		// delay is too small to halve, jitter is simply skipped.
		if half := delay / 2; e.config.UseJitter && half > 0 {
			jitter := time.Duration(rand.Int64N(int64(half))) //nolint:gosec // G404: jitter does not require cryptographic randomness
			sleepDuration = delay - half + jitter
		}

		select {
		case <-ctx.Done():
			return lastErr
		case <-time.After(sleepDuration):
		}

		delay = min(time.Duration(float64(delay)*e.config.Multiplier), e.config.MaxDelay)
	}

	return lastErr
}

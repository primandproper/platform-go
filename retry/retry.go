package retry

import (
	"context"
	"math/rand/v2"
	"time"
)

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

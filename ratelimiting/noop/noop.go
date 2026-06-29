package noop

import (
	"context"

	"github.com/primandproper/platform-go/v2/ratelimiting"
)

var _ ratelimiting.RateLimiter = (*rateLimiter)(nil)

// rateLimiter always allows requests.
type rateLimiter struct{}

// Allow always returns true.
func (n *rateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	return true, nil
}

// Close is a no-op.
func (n *rateLimiter) Close() error {
	return nil
}

// NewRateLimiter returns a RateLimiter that never limits.
func NewRateLimiter() ratelimiting.RateLimiter {
	return &rateLimiter{}
}

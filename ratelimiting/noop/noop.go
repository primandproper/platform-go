// Package noop is the ratelimiting.RateLimiter that never limits: Allow returns
// true for every key, and there is no counter, window, or store behind it to
// consult.
//
// Choosing it moves the protection elsewhere rather than deciding it is not
// needed — to an ingress, an API gateway, or the upstream's own limits —
// because the endpoints wired through it now accept whatever load arrives. It
// is also what a single-process local run wants, where a shared limiter would
// mean standing up Redis in order to throttle one developer.
//
// ratelimiting/config builds it for the "noop" provider name, which has to be
// chosen: an unrecognized name is errors.ErrUnknownProvider, not an open door.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/ratelimiting"
)

var _ ratelimiting.RateLimiter = (*RateLimiter)(nil)

// RateLimiter always allows requests.
type RateLimiter struct{}

// Allow always returns true.
func (n *RateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	return true, nil
}

// Close is a no-op.
func (n *RateLimiter) Close() error {
	return nil
}

// NewRateLimiter returns a RateLimiter that never limits.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{}
}

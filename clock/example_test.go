package clock_test

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
)

// expiringToken is the shape components take: it stamps and checks against an
// injected Clock rather than calling time.Now directly, which is what lets a
// test drive it under testing/synctest without waiting on real time.
type expiringToken struct {
	clock     clock.Clock
	expiresAt time.Time
}

func newExpiringToken(c clock.Clock, ttl time.Duration) *expiringToken {
	return &expiringToken{clock: c, expiresAt: c.Now().Add(ttl)}
}

func (t *expiringToken) expired() bool {
	return !t.clock.Now().Before(t.expiresAt)
}

// Example wires a component to the production clock. Tests need no double:
// inside a testing/synctest bubble this same Clock reads the bubble's fake
// time, so time.Sleep moves the token to expiry in nanoseconds of wall time.
func Example() {
	c := clock.NewClock()

	tok := newExpiringToken(c, time.Hour)
	fmt.Println("expired at issue:", tok.expired())

	// Sleep is context-aware: a canceled context ends the pause immediately
	// rather than stranding the goroutine for the full duration.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fmt.Println("sleep:", c.Sleep(ctx, time.Hour))

	// Output:
	// expired at issue: false
	// sleep: context canceled
}

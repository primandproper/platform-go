// Package receivewait paces a consumer's receive loop after a failed receive.
//
// A broker's own long-polling paces the loop while it is working; a receive that
// fails returns immediately, so a persistent failure — expired credentials, a
// deleted queue, a partitioned broker — spins the loop as fast as the CPU and
// the broker's API allow, burning a core and the request quota for as long as it
// lasts. Every consumer therefore needs a wait here, and the two that had one
// disagreed about what it should be: SQS grew from 100ms to 30s, Kafka paused a
// flat 250ms forever.
//
// Neither jittered, which is the part that matters at fleet scale. A broker that
// goes away takes every consumer with it at the same instant, and un-jittered
// backoff has them all come back at the same instant too — turning a recovering
// broker's first moment back into the same thundering herd that is still
// arriving when it falls over again.
package receivewait

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/retry"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"
)

// The schedule every consumer in this module backs off on: a tenth of a second
// after the first failure, doubling to thirty seconds.
//
// The floor is short because most receive failures are one bad response and the
// next call succeeds; the ceiling is long because the ones that are not are
// usually an operator's problem, and polling a deleted queue four times a second
// for an hour helps nobody.
var schedule = retrycfg.Config{
	InitialDelay: 100 * time.Millisecond,
	MaxDelay:     30 * time.Second,
	Multiplier:   2,
	MaxAttempts:  1, // unused: this grows a wait rather than bounding a loop
	UseJitter:    true,
}

// Backoff is one consumer's receive-loop pacing. It is not safe for concurrent
// use; a consumer's loop is a single goroutine.
//
// The zero value is not usable — build one with New, which resolves the clock.
type Backoff struct {
	clock    clock.Clock
	jitter   retry.Jitter
	failures uint
}

// New builds a Backoff against c, which may be nil for the wall clock.
//
// rand may be nil for the default source; it is a parameter so a test can pin
// the schedule rather than assert on a range.
func New(c clock.Clock, rand retry.Rand) *Backoff {
	if c == nil {
		c = clock.NewClock()
	}

	return &Backoff{clock: c, jitter: retry.Equal(rand)}
}

// Wait sleeps out the backoff for the failure that just happened, growing it,
// and reports a context that went away underneath it.
//
// The jitter is retry.Equal rather than retry.Full because this caller sleeps in
// place: half the schedule stays under every wait, so a loop that has backed off
// to thirty seconds cannot draw a near-zero one and become hot again.
func (b *Backoff) Wait(ctx context.Context) error {
	b.failures++

	return b.clock.Sleep(ctx, b.jitter(retrycfg.DelayFor(schedule, b.failures)))
}

// Reset returns the wait to its floor, and is called after every receive that
// succeeds — so a queue that fails once an hour never accumulates its way to the
// ceiling.
func (b *Backoff) Reset() { b.failures = 0 }

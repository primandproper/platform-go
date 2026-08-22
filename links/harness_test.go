package links

import (
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	cachememory "github.com/primandproper/platform-go/v13/cache/memory"
	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/distributedlock"
	dlmemory "github.com/primandproper/platform-go/v13/distributedlock/memory"

	"github.com/shoenig/test/must"
)

const (
	testAction    Action  = "magic_login"
	testSubject   Subject = "user_123"
	testActionTTL         = 15 * time.Minute
)

// testPolicy is the action every test mints under unless it needs otherwise.
func testPolicy() ActionPolicy {
	return ActionPolicy{
		URL: "https://app.example.com/auth/magic/{token}",
		TTL: testActionTTL,
	}
}

// newStore builds an in-memory record store. The expiry is per-write, so the
// cache default is irrelevant.
func newStore(tb testing.TB) cache.Cache[Record] {
	tb.Helper()

	c, err := cachememory.NewInMemoryCache[Record](0)
	must.NoError(tb, err)

	return c
}

// newLocker builds a real in-process scoped locker, so the concurrency tests
// exercise actual mutual exclusion rather than a mock that always grants.
func newLocker(tb testing.TB) distributedlock.ScopedLocker {
	tb.Helper()

	locker, err := dlmemory.NewLocker()
	must.NoError(tb, err)

	scoped, err := distributedlock.NewScopedLocker(locker)
	must.NoError(tb, err)

	return scoped
}

// newTestMinter builds a Minter over a memory store and a memory locker, with
// the default action registered.
func newTestMinter(tb testing.TB, opts ...Option) *Minter {
	tb.Helper()

	m, err := NewMinter(newStore(tb), newLocker(tb), append([]Option{
		WithAction(testAction, testPolicy()),
	}, opts...)...)
	must.NoError(tb, err)

	return m
}

// testClock is a clock whose time only moves when a test moves it, so expiry
// can be reached without waiting for it.
type testClock struct {
	now time.Time
	mu  sync.Mutex
}

// newTestClock starts a clock at a fixed instant.
func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)}
}

// Advance moves the clock forward.
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// Clock adapts the test clock to the interface the Minter takes. Only Now and
// Since are reachable from this package, so the rest is left to panic rather
// than given a plausible-looking implementation nothing exercises.
func (c *testClock) Clock() clock.Clock {
	return &clockmock.ClockMock{
		NowFunc: func() time.Time {
			c.mu.Lock()
			defer c.mu.Unlock()

			return c.now
		},
		SinceFunc: func(t time.Time) time.Duration {
			c.mu.Lock()
			defer c.mu.Unlock()

			return c.now.Sub(t)
		},
	}
}

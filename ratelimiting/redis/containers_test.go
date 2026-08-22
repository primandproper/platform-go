package redis

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/ratelimiting"
	"github.com/primandproper/platform-go/v13/testutils/containers/redistest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newContainerBackedLimiter stands up a Redis and returns a limiter over it.
func newContainerBackedLimiter(t *testing.T, requestsPerSec float64, burstSize int) ratelimiting.RateLimiter {
	t.Helper()

	container := redistest.Start(t)

	limiter, err := NewRedisRateLimiter(
		t.Context(),
		Config{Addresses: []string{redistest.Address(t, container)}},
		requestsPerSec,
		burstSize,
	)
	must.NoError(t, err)
	t.Cleanup(func() { _ = limiter.Close() })

	return limiter
}

// TestRateLimiter_Container exercises both Lua scripts against a real Redis.
// The unit tests stub Eval, so this is the only thing that catches a script
// that a stub happily accepts and Redis rejects.
func TestRateLimiter_Container(T *testing.T) {
	T.Parallel()

	T.Run("allows up to the burst and then refuses", func(t *testing.T) {
		t.Parallel()

		limiter := newContainerBackedLimiter(t, 1, 3)

		for range 3 {
			allowed, err := limiter.Allow(t.Context(), "burst-key")
			must.NoError(t, err)
			test.True(t, allowed)
		}

		allowed, err := limiter.Allow(t.Context(), "burst-key")
		must.NoError(t, err)
		test.False(t, allowed)
	})

	T.Run("keeps keys independent", func(t *testing.T) {
		t.Parallel()

		limiter := newContainerBackedLimiter(t, 1, 1)

		allowed, err := limiter.Allow(t.Context(), "key-a")
		must.NoError(t, err)
		must.True(t, allowed)

		allowed, err = limiter.Allow(t.Context(), "key-b")
		must.NoError(t, err)
		test.True(t, allowed)

		allowed, err = limiter.Allow(t.Context(), "key-a")
		must.NoError(t, err)
		test.False(t, allowed)
	})

	T.Run("estimates the wait for a saturated window", func(t *testing.T) {
		t.Parallel()

		// One per second with a burst of one: the entry just written falls out
		// of the window roughly a second from now.
		limiter := newContainerBackedLimiter(t, 1, 1)

		allowed, err := limiter.Allow(t.Context(), "hint-key")
		must.NoError(t, err)
		must.True(t, allowed)

		allowed, err = limiter.Allow(t.Context(), "hint-key")
		must.NoError(t, err)
		must.False(t, allowed)

		delay, ok := ratelimiting.RetryAfterFor(t.Context(), limiter, "hint-key")
		must.True(t, ok)
		test.Greater(t, time.Duration(0), delay)
		test.LessEq(t, time.Second, delay)
	})

	T.Run("reports no hint for a window with room", func(t *testing.T) {
		t.Parallel()

		limiter := newContainerBackedLimiter(t, 10, 5)

		allowed, err := limiter.Allow(t.Context(), "roomy-key")
		must.NoError(t, err)
		must.True(t, allowed)

		_, ok := ratelimiting.RetryAfterFor(t.Context(), limiter, "roomy-key")
		test.False(t, ok)
	})

	T.Run("reports no hint for a key it has never seen", func(t *testing.T) {
		t.Parallel()

		limiter := newContainerBackedLimiter(t, 10, 5)

		_, ok := ratelimiting.RetryAfterFor(t.Context(), limiter, "unseen-key")
		test.False(t, ok)
	})

	T.Run("asking for a hint does not change the next decision", func(t *testing.T) {
		t.Parallel()

		// The hint script is read-only, so consulting it cannot consume or
		// evict anything the allow script would otherwise have counted.
		limiter := newContainerBackedLimiter(t, 1, 2)

		allowed, err := limiter.Allow(t.Context(), "readonly-key")
		must.NoError(t, err)
		must.True(t, allowed)

		for range 5 {
			_, _ = ratelimiting.RetryAfterFor(t.Context(), limiter, "readonly-key")
		}

		// One token of the burst is left, and only one.
		allowed, err = limiter.Allow(t.Context(), "readonly-key")
		must.NoError(t, err)
		test.True(t, allowed)

		allowed, err = limiter.Allow(t.Context(), "readonly-key")
		must.NoError(t, err)
		test.False(t, allowed)
	})

	T.Run("lets a refused key back in once its window slides", func(t *testing.T) {
		t.Parallel()

		limiter := newContainerBackedLimiter(t, 20, 1)

		allowed, err := limiter.Allow(t.Context(), "sliding-key")
		must.NoError(t, err)
		must.True(t, allowed)

		allowed, err = limiter.Allow(t.Context(), "sliding-key")
		must.NoError(t, err)
		must.False(t, allowed)

		delay, ok := ratelimiting.RetryAfterFor(t.Context(), limiter, "sliding-key")
		must.True(t, ok)

		// The hint is documented as a floor, so waiting it out plus a margin is
		// what a client honoring it would do.
		time.Sleep(delay + 50*time.Millisecond)

		allowed, err = limiter.Allow(t.Context(), "sliding-key")
		must.NoError(t, err)
		test.True(t, allowed)
	})
}

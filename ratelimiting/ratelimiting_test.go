package ratelimiting

import (
	"context"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestInMemoryRateLimiter_Allow(T *testing.T) {
	T.Parallel()

	T.Run("allows within burst", func(t *testing.T) {
		t.Parallel()

		limiter, err := NewInMemoryRateLimiter(nil, 10, 3)
		must.NoError(t, err)
		defer limiter.Close()

		ctx := context.Background()

		allowed, err := limiter.Allow(ctx, "key1")
		must.NoError(t, err)
		test.True(t, allowed)

		allowed, err = limiter.Allow(ctx, "key1")
		must.NoError(t, err)
		test.True(t, allowed)

		allowed, err = limiter.Allow(ctx, "key1")
		must.NoError(t, err)
		test.True(t, allowed)

		allowed, err = limiter.Allow(ctx, "key1")
		must.NoError(t, err)
		test.False(t, allowed)
	})

	T.Run("different keys have independent limits", func(t *testing.T) {
		t.Parallel()

		limiter, err := NewInMemoryRateLimiter(nil, 10, 1)
		must.NoError(t, err)
		defer limiter.Close()

		ctx := context.Background()

		allowed, err := limiter.Allow(ctx, "key1")
		must.NoError(t, err)
		test.True(t, allowed)

		allowed, err = limiter.Allow(ctx, "key2")
		must.NoError(t, err)
		test.True(t, allowed)

		allowed, err = limiter.Allow(ctx, "key1")
		must.NoError(t, err)
		test.False(t, allowed)

		allowed, err = limiter.Allow(ctx, "key2")
		must.NoError(t, err)
		test.False(t, allowed)
	})

	T.Run("Close is safe", func(t *testing.T) {
		t.Parallel()

		limiter, err := NewInMemoryRateLimiter(nil, 10, 1)
		must.NoError(t, err)
		err = limiter.Close()
		must.NoError(t, err)
	})

	T.Run("Close releases per-key limiters", func(t *testing.T) {
		t.Parallel()

		limiter, err := NewInMemoryRateLimiter(nil, 10, 1)
		must.NoError(t, err)

		ctx := context.Background()
		_, err = limiter.Allow(ctx, "key1")
		must.NoError(t, err)
		_, err = limiter.Allow(ctx, "key2")
		must.NoError(t, err)

		impl, ok := limiter.(*inMemoryRateLimiter)
		must.True(t, ok)

		countBefore := 0
		impl.limiters.Range(func(_, _ any) bool { countBefore++; return true })
		test.EqOp(t, 2, countBefore)

		must.NoError(t, limiter.Close())

		countAfter := 0
		impl.limiters.Range(func(_, _ any) bool { countAfter++; return true })
		test.EqOp(t, 0, countAfter)
	})
}

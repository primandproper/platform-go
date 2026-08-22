package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/ratelimiting"

	"github.com/redis/go-redis/v9"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRateLimiter_RetryAfter(T *testing.T) {
	T.Parallel()

	T.Run("renders the script's milliseconds as a duration", func(t *testing.T) {
		t.Parallel()

		limiter, client := buildTestRateLimiter(t)
		client.evalFunc = func(ctx context.Context, _ string, _ []string, _ ...any) *redis.Cmd {
			cmd := redis.NewCmd(ctx)
			cmd.SetVal(int64(1500))

			return cmd
		}

		delay, ok := limiter.RetryAfter(t.Context(), "key")
		must.True(t, ok)
		test.EqOp(t, 1500*time.Millisecond, delay)
	})

	T.Run("reports no hint when the window already has room", func(t *testing.T) {
		t.Parallel()

		// Zero means "nothing to wait for". Passing it on as a hint would send
		// a Retry-After of 0 and invite the client straight back.
		limiter, client := buildTestRateLimiter(t)
		client.evalFunc = func(ctx context.Context, _ string, _ []string, _ ...any) *redis.Cmd {
			cmd := redis.NewCmd(ctx)
			cmd.SetVal(int64(0))

			return cmd
		}

		delay, ok := limiter.RetryAfter(t.Context(), "key")
		test.False(t, ok)
		test.EqOp(t, time.Duration(0), delay)
	})

	T.Run("swallows a script failure rather than failing the refusal", func(t *testing.T) {
		t.Parallel()

		// The caller is already refusing this request; a missing hint costs it
		// a header, and surfacing the error would cost it the refusal.
		limiter, client := buildTestRateLimiter(t)
		client.evalFunc = func(ctx context.Context, _ string, _ []string, _ ...any) *redis.Cmd {
			cmd := redis.NewCmd(ctx)
			cmd.SetErr(errors.New("redis is having a moment"))

			return cmd
		}

		_, ok := limiter.RetryAfter(t.Context(), "key")
		test.False(t, ok)
	})

	T.Run("measures against the same window Allow decided in", func(t *testing.T) {
		t.Parallel()

		limiter, client := buildTestRateLimiter(t)
		client.evalFunc = func(ctx context.Context, _ string, _ []string, _ ...any) *redis.Cmd {
			cmd := redis.NewCmd(ctx)
			cmd.SetVal(int64(1))

			return cmd
		}

		_, err := limiter.Allow(t.Context(), "key")
		must.NoError(t, err)

		_, _ = limiter.RetryAfter(t.Context(), "key")

		must.SliceLen(t, 2, client.evalCalls)

		allow, hint := client.evalCalls[0], client.evalCalls[1]
		test.Eq(t, allow.keys, hint.keys)

		// Both scripts take (now, windowMS, limit); the hint adds no member.
		// A drifting window would make the estimate measure a different limit
		// than the one that refused the request.
		test.EqOp(t, allow.args[1], hint.args[1])
		test.EqOp(t, allow.args[2], hint.args[2])
	})

	T.Run("reads without mutating the window", func(t *testing.T) {
		t.Parallel()

		// The allow script prunes with ZREMRANGEBYSCORE; the hint must not, or
		// consulting it would change what the next Allow decides.
		limiter, client := buildTestRateLimiter(t)
		client.evalFunc = func(ctx context.Context, _ string, _ []string, _ ...any) *redis.Cmd {
			cmd := redis.NewCmd(ctx)
			cmd.SetVal(int64(0))

			return cmd
		}

		_, _ = limiter.RetryAfter(t.Context(), "key")

		must.SliceLen(t, 1, client.evalCalls)
		test.StrNotContains(t, client.evalCalls[0].script, "ZREMRANGEBYSCORE")
		test.StrNotContains(t, client.evalCalls[0].script, "ZADD")
	})
}

func TestRateLimiter_ImplementsRetryHinter(T *testing.T) {
	T.Parallel()

	// The middleware and interceptor find the hint by type assertion, so this
	// is the assertion that keeps the Redis limiter's hints reachable.
	limiter, _ := buildTestRateLimiter(T)

	var asLimiter ratelimiting.RateLimiter = limiter
	_, ok := asLimiter.(ratelimiting.RetryHinter)
	test.True(T, ok)
}

func TestRateLimiter_window(T *testing.T) {
	T.Parallel()

	T.Run("derives the window from the burst and the rate", func(t *testing.T) {
		t.Parallel()

		limiter := &RateLimiter{requestsPerSec: 10, burstSize: 20}

		limit, windowMS := limiter.window()
		test.EqOp(t, int64(20), limit)
		test.EqOp(t, int64(2000), windowMS)
	})

	T.Run("keeps a sub-1 rate from flooring the limit to zero", func(t *testing.T) {
		t.Parallel()

		limiter := &RateLimiter{requestsPerSec: 0.5, burstSize: 1}

		limit, windowMS := limiter.window()
		test.EqOp(t, int64(1), limit)
		test.EqOp(t, int64(2000), windowMS)
	})

	T.Run("substitutes a usable rate for a non-positive one", func(t *testing.T) {
		t.Parallel()

		limiter := &RateLimiter{requestsPerSec: 0, burstSize: 3}

		limit, windowMS := limiter.window()
		test.EqOp(t, int64(3), limit)
		test.EqOp(t, int64(3000), windowMS)
	})
}

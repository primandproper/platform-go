package ratelimitingcfg

import (
	"context"
	"testing"

	redisrl "github.com/primandproper/platform-go/v4/ratelimiting/redis"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("sets defaults for zero values", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, 10.0, cfg.RequestsPerSec)
		test.EqOp(t, 20, cfg.BurstSize)
	})

	T.Run("preserves non-zero values", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			RequestsPerSec: 5.0,
			BurstSize:      10,
		}
		cfg.EnsureDefaults()

		test.EqOp(t, 5.0, cfg.RequestsPerSec)
		test.EqOp(t, 10, cfg.BurstSize)
	})
}

func TestConfig_ProvideRateLimiter(T *testing.T) {
	T.Parallel()

	T.Run("nil config returns noop", func(t *testing.T) {
		t.Parallel()

		var cfg *Config
		limiter, err := cfg.ProvideRateLimiter(nil)
		must.NoError(t, err)
		must.NotNil(t, limiter)

		allowed, err := limiter.Allow(context.Background(), "x")
		must.NoError(t, err)
		test.True(t, allowed)
	})

	T.Run("empty provider returns noop", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ""}
		limiter, err := cfg.ProvideRateLimiter(nil)
		must.NoError(t, err)
		must.NotNil(t, limiter)

		allowed, err := limiter.Allow(context.Background(), "x")
		must.NoError(t, err)
		test.True(t, allowed)
	})

	T.Run("noop provider returns noop", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderNoop}
		limiter, err := cfg.ProvideRateLimiter(nil)
		must.NoError(t, err)
		must.NotNil(t, limiter)

		allowed, err := limiter.Allow(context.Background(), "x")
		must.NoError(t, err)
		test.True(t, allowed)
	})

	T.Run("memory provider returns in-memory limiter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:       ProviderMemory,
			RequestsPerSec: 1,
			BurstSize:      1,
		}
		limiter, err := cfg.ProvideRateLimiter(nil)
		must.NoError(t, err)
		must.NotNil(t, limiter)

		allowed, err := limiter.Allow(context.Background(), "x")
		must.NoError(t, err)
		test.True(t, allowed)

		allowed, err = limiter.Allow(context.Background(), "x")
		must.NoError(t, err)
		test.False(t, allowed)
	})

	T.Run("redis provider returns redis limiter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:       ProviderRedis,
			Redis:          redisrl.Config{Addresses: []string{"127.0.0.1:6379"}},
			RequestsPerSec: 1,
			BurstSize:      1,
		}
		limiter, err := cfg.ProvideRateLimiter(nil)
		must.NoError(t, err)
		test.NotNil(t, limiter)
	})

	T.Run("unknown provider returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "unknown"}
		limiter, err := cfg.ProvideRateLimiter(nil)
		must.Error(t, err)
		test.Nil(t, limiter)
		test.StrContains(t, err.Error(), "unknown")
	})
}

func TestProvideRateLimiterFromConfig(T *testing.T) {
	T.Parallel()

	T.Run("nil config returns noop", func(t *testing.T) {
		t.Parallel()

		limiter, err := ProvideRateLimiterFromConfig(nil, nil)
		must.NoError(t, err)
		must.NotNil(t, limiter)
	})

	T.Run("noop provider returns noop", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderNoop}
		limiter, err := ProvideRateLimiterFromConfig(cfg, nil)
		must.NoError(t, err)
		must.NotNil(t, limiter)
	})

	T.Run("unknown provider wraps error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "unknown"}
		limiter, err := ProvideRateLimiterFromConfig(cfg, nil)
		must.Error(t, err)
		test.Nil(t, limiter)
		test.StrContains(t, err.Error(), "provide rate limiter")
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("valid config", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		cfg := &Config{
			RequestsPerSec: 1.0,
			BurstSize:      1,
		}

		err := cfg.ValidateWithContext(ctx)
		must.NoError(t, err)
	})

	T.Run("invalid RequestsPerSec", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		cfg := &Config{
			RequestsPerSec: -1,
			BurstSize:      1,
		}

		err := cfg.ValidateWithContext(ctx)
		must.Error(t, err)
	})

	T.Run("invalid BurstSize", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		cfg := &Config{
			RequestsPerSec: 1.0,
			BurstSize:      -1,
		}

		err := cfg.ValidateWithContext(ctx)
		must.Error(t, err)
	})
}

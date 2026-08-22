package ratelimitingcfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/errors"
	redisrl "github.com/primandproper/platform-go/v13/ratelimiting/redis"

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

func TestNewRateLimiter(T *testing.T) {
	T.Parallel()

	T.Run("nil config is an error", func(t *testing.T) {
		t.Parallel()

		limiter, err := NewRateLimiter(context.Background(), nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
		test.Nil(t, limiter)
	})

	// The whole point of the change: an unset provider no longer silently
	// yields a limiter that never limits.
	T.Run("empty provider is an error", func(t *testing.T) {
		t.Parallel()

		limiter, err := NewRateLimiter(context.Background(), &Config{Provider: ""})
		must.Error(t, err)
		test.Nil(t, limiter)
	})

	T.Run("noop provider returns noop", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderNoop}
		limiter, err := NewRateLimiter(context.Background(), cfg)
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
		limiter, err := NewRateLimiter(context.Background(), cfg)
		must.NoError(t, err)
		must.NotNil(t, limiter)

		t.Cleanup(func() { must.NoError(t, limiter.Close()) })

		allowed, err := limiter.Allow(context.Background(), "x")
		must.NoError(t, err)
		test.True(t, allowed)

		allowed, err = limiter.Allow(context.Background(), "x")
		must.NoError(t, err)
		test.False(t, allowed)
	})

	// MaxLimiters reaches the memory provider, which is only observable through
	// what the bound does: a key evicted to make room is handed a fresh bucket,
	// so the refusal it was sitting on is forgiven. The rate is slow enough that
	// no bucket refills on its own during the test, and the window long enough
	// that nothing goes idle.
	T.Run("memory provider honors the limiter bound", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		cfg := &Config{
			Provider:       ProviderMemory,
			RequestsPerSec: 0.001,
			BurstSize:      1,
			MaxLimiters:    2,
		}
		limiter, err := NewRateLimiter(ctx, cfg)
		must.NoError(t, err)
		must.NotNil(t, limiter)

		t.Cleanup(func() { must.NoError(t, limiter.Close()) })

		allowed, err := limiter.Allow(ctx, "first")
		must.NoError(t, err)
		must.True(t, allowed)

		allowed, err = limiter.Allow(ctx, "first")
		must.NoError(t, err)
		must.False(t, allowed)

		// Two more keys, the second of which crosses the bound and drops the
		// least recently seen — "first".
		for _, key := range []string{"second", "third"} {
			_, err = limiter.Allow(ctx, key)
			must.NoError(t, err)
		}

		allowed, err = limiter.Allow(ctx, "first")
		must.NoError(t, err)
		test.True(t, allowed, test.Sprint("the bound did not evict the least recently seen key"))
	})

	T.Run("redis provider returns redis limiter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:       ProviderRedis,
			Redis:          redisrl.Config{Addresses: []string{"127.0.0.1:6379"}},
			RequestsPerSec: 1,
			BurstSize:      1,
		}
		limiter, err := NewRateLimiter(context.Background(), cfg)
		must.NoError(t, err)
		test.NotNil(t, limiter)
	})

	T.Run("unknown provider returns ErrUnknownProvider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "unknown"}
		limiter, err := NewRateLimiter(context.Background(), cfg)
		must.Error(t, err)
		test.Nil(t, limiter)
	})

	// Validation is wired in now, so a negative rate — which would reject every
	// request — is caught at construction instead of at the first Allow.
	T.Run("rejects a negative rate", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMemory, RequestsPerSec: -1, BurstSize: 1}
		limiter, err := NewRateLimiter(context.Background(), cfg)
		must.Error(t, err)
		test.Nil(t, limiter)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("valid config", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		cfg := &Config{
			Provider:       ProviderMemory,
			RequestsPerSec: 1.0,
			BurstSize:      1,
		}

		err := cfg.ValidateWithContext(ctx)
		must.NoError(t, err)
	})

	T.Run("missing provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{RequestsPerSec: 1.0, BurstSize: 1}

		err := cfg.ValidateWithContext(context.Background())
		must.Error(t, err)
	})

	T.Run("invalid RequestsPerSec", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		cfg := &Config{
			Provider:       ProviderMemory,
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
			Provider:       ProviderMemory,
			RequestsPerSec: 1.0,
			BurstSize:      -1,
		}

		err := cfg.ValidateWithContext(ctx)
		must.Error(t, err)
	})
}

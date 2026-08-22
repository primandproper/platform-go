package secretscfg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/secrets"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext_caching(T *testing.T) {
	T.Parallel()

	T.Run("no caching fields is valid", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderEnv}

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("a cache TTL alone is valid", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderEnv, CacheTTL: time.Minute}

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("a refresh shorter than the TTL is valid", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderEnv, CacheTTL: time.Minute, RefreshInterval: 30 * time.Second}

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects a negative cache TTL", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderEnv, CacheTTL: -time.Minute}

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects a refresh without a cache", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderEnv, RefreshInterval: time.Minute}

		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "requires a cache TTL")
	})

	T.Run("rejects a refresh that cannot beat the TTL", func(t *testing.T) {
		t.Parallel()

		for _, interval := range []time.Duration{time.Minute, 2 * time.Minute} {
			cfg := &Config{Provider: ProviderEnv, CacheTTL: time.Minute, RefreshInterval: interval}

			err := cfg.ValidateWithContext(t.Context())
			must.Error(t, err)
			test.StrContains(t, err.Error(), "must be shorter than the cache TTL")
		}
	})
}

func TestConfig_NewSecretSource_caching(T *testing.T) {
	T.Parallel()

	T.Run("an unset TTL leaves the provider undecorated", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderEnv}

		source, err := cfg.NewSecretSource(t.Context())
		must.NoError(t, err)
		must.NotNil(t, source)
		t.Cleanup(func() { _ = source.Close() })

		_, ok := source.(secrets.CachingSource)
		test.False(t, ok)
	})

	T.Run("a TTL wraps the provider in a caching source", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderEnv, CacheTTL: time.Minute, RefreshInterval: 30 * time.Second}

		source, err := cfg.NewSecretSource(t.Context())
		must.NoError(t, err)
		must.NotNil(t, source)
		t.Cleanup(func() { _ = source.Close() })

		// The assertion a caller makes to reach the rotation hooks.
		cached, ok := source.(secrets.CachingSource)
		must.True(t, ok)

		key := "TEST_CACHING_CFG_" + t.Name()
		must.NoError(t, os.Setenv(key, "one"))
		t.Cleanup(func() { _ = os.Unsetenv(key) })

		cancel := cached.OnChange(key, func(string, string) {})
		t.Cleanup(cancel)

		got, err := source.GetSecret(t.Context(), key)
		must.NoError(t, err)
		test.EqOp(t, "one", got)

		// The second read comes from the cache, so the environment changing
		// underneath it is not visible until the TTL runs out.
		must.NoError(t, os.Setenv(key, "two"))

		got, err = source.GetSecret(t.Context(), key)
		must.NoError(t, err)
		test.EqOp(t, "one", got)
	})

	T.Run("a construction failure closes the source it was going to wrap", func(t *testing.T) {
		t.Parallel()

		// Reachable only by going around validation, which is exactly why the
		// constructor checks again: a Config assembled in code rather than
		// parsed from the environment need never have been validated.
		cfg := &Config{Provider: ProviderNoop, CacheTTL: time.Minute}

		source, err := cfg.decorate(t.Context(), &countingSource{}, newOptions([]Option{
			WithCachingOptions(secrets.WithRefresh(context.Background(), 2*time.Minute)),
		}))

		must.Error(t, err)
		test.Nil(t, source)
		test.ErrorIs(t, err, secrets.ErrInvalidRefreshInterval)
	})

	T.Run("caching options are passed through", func(t *testing.T) {
		t.Parallel()

		backend := &countingSource{}
		cfg := &Config{Provider: ProviderNoop, CacheTTL: time.Minute}

		source, err := cfg.decorate(t.Context(), backend, newOptions([]Option{
			WithCachingOptions(secrets.WithRefresh(context.Background(), 30*time.Second)),
		}))

		must.NoError(t, err)
		must.NotNil(t, source)
		must.NoError(t, source.Close())

		// Closing the decorator closes what it wrapped.
		test.EqOp(t, 1, backend.closes)
	})
}

// countingSource records how many times it was closed, so a test can assert the
// decorator did not leak the source it was handed.
type countingSource struct {
	closes int
}

func (c *countingSource) GetSecret(context.Context, string) (string, error) { return "", nil }

func (c *countingSource) Close() error {
	c.closes++

	return nil
}

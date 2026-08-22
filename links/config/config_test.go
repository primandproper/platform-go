package linkscfg

import (
	"testing"
	"time"

	cachecfg "github.com/primandproper/platform-go/v13/cache/config"
	distributedlockcfg "github.com/primandproper/platform-go/v13/distributedlock/config"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/links"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// memoryConfig is a valid in-process configuration, for tests that care about
// everything except the providers.
func memoryConfig() *Config {
	return &Config{
		Actions: map[links.Action]links.ActionPolicy{
			"magic_login": {URL: "https://app.example.com/auth/magic/{token}", TTL: 15 * time.Minute},
		},
		Cache: cachecfg.Config{Provider: cachecfg.ProviderMemory},
		Lock:  distributedlockcfg.Config{Provider: distributedlockcfg.MemoryProvider},
	}
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills in zero fields", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, links.DefaultRetention, cfg.Retention)
		test.EqOp(t, links.DefaultTokenBytes, cfg.TokenBytes)
		test.EqOp(t, links.DefaultMaxTokenLength, cfg.MaxTokenLength)
		test.EqOp(t, links.DefaultKeyPrefix, cfg.KeyPrefix)
	})

	T.Run("leaves an action's TTL alone", func(t *testing.T) {
		t.Parallel()

		// The absence of a default lifetime is the point: filling one in here
		// would put back exactly what links.ActionPolicy refuses to guess.
		cfg := &Config{Actions: map[links.Action]links.ActionPolicy{"magic_login": {}}}
		cfg.EnsureDefaults()

		test.EqOp(t, time.Duration(0), cfg.Actions["magic_login"].TTL)
	})

	T.Run("leaves set fields alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Retention:      time.Hour,
			TokenBytes:     64,
			MaxTokenLength: 128,
			KeyPrefix:      "custom:",
		}
		cfg.EnsureDefaults()

		test.EqOp(t, time.Hour, cfg.Retention)
		test.EqOp(t, 64, cfg.TokenBytes)
		test.EqOp(t, 128, cfg.MaxTokenLength)
		test.EqOp(t, "custom:", cfg.KeyPrefix)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a valid config", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, memoryConfig().ValidateWithContext(t.Context()))
	})

	T.Run("rejects a negative retention", func(t *testing.T) {
		t.Parallel()

		cfg := memoryConfig()
		cfg.Retention = -time.Second

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects an invalid cache config", func(t *testing.T) {
		t.Parallel()

		cfg := memoryConfig()
		cfg.Cache.Provider = "nonsense"

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects an invalid lock config", func(t *testing.T) {
		t.Parallel()

		cfg := memoryConfig()
		cfg.Lock.Provider = "nonsense"

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewMinter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		minter, err := NewMinter(t.Context(), memoryConfig(), nil)
		must.NoError(t, err)

		link, err := minter.Mint(t.Context(), "magic_login", "user_123")
		must.NoError(t, err)
		test.StrHasPrefix(t, "https://app.example.com/auth/magic/", link.URL)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewMinter(t.Context(), nil, nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("reports a config with no actions", func(t *testing.T) {
		t.Parallel()

		cfg := memoryConfig()
		cfg.Actions = nil

		_, err := NewMinter(t.Context(), cfg, nil)
		test.ErrorIs(t, err, links.ErrNoActions)
	})

	T.Run("reports an action policy the file got wrong", func(t *testing.T) {
		t.Parallel()

		cfg := memoryConfig()
		cfg.Actions["verify_email"] = links.ActionPolicy{URL: "https://app.example.com/verify/{token}"}

		_, err := NewMinter(t.Context(), cfg, nil)
		test.ErrorIs(t, err, links.ErrInvalidTTL)
	})

	T.Run("refuses a cleartext action URL unless told otherwise", func(t *testing.T) {
		t.Parallel()

		cfg := memoryConfig()
		cfg.Actions["magic_login"] = links.ActionPolicy{
			URL: "http://staging.example.com/auth/magic/{token}",
			TTL: time.Minute,
		}

		_, err := NewMinter(t.Context(), cfg, nil)
		test.ErrorIs(t, err, links.ErrInsecureActionURL)

		cfg.AllowInsecureURLs = true

		_, err = NewMinter(t.Context(), cfg, nil)
		test.NoError(t, err)
	})

	T.Run("caller options win over the file", func(t *testing.T) {
		t.Parallel()

		minter, err := NewMinter(t.Context(), memoryConfig(), nil,
			WithMinterOptions(links.WithAction("magic_login", links.ActionPolicy{
				URL: "https://tenant.example.com/auth/magic/{token}",
				TTL: time.Minute,
			})))
		must.NoError(t, err)

		link, err := minter.Mint(t.Context(), "magic_login", "user_123")
		must.NoError(t, err)
		test.StrHasPrefix(t, "https://tenant.example.com/auth/magic/", link.URL)
	})

	T.Run("caller options can register an action the file does not", func(t *testing.T) {
		t.Parallel()

		minter, err := NewMinter(t.Context(), memoryConfig(), nil,
			WithMinterOptions(links.WithAction("unsubscribe", links.ActionPolicy{
				URL: "https://app.example.com/unsubscribe?t={token}",
				TTL: 24 * time.Hour,
			})))
		must.NoError(t, err)

		test.SliceLen(t, 2, minter.Actions())
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		_, err := NewMinter(t.Context(), memoryConfig(), nil, nil)
		test.NoError(t, err)
	})
}

package linkscfg

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/links"

	"github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testConfig is a valid configuration with one action registered, and no
// sweeper: these tests build several Minters apiece and none of them wants a
// goroutine ticking behind it.
func testConfig() *Config {
	return &Config{
		Actions: map[links.Action]links.ActionPolicy{
			"magic_login": {URL: "https://app.example.com/auth/magic/{token}", TTL: 15 * time.Minute},
		},
		SweepInterval: pointer.To(time.Duration(0)),
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

		// The table grows by a row per link until something sweeps it, so an
		// unconfigured deployment gets a sweeper rather than an ever-growing
		// table.
		must.NotNil(t, cfg.SweepInterval)
		test.EqOp(t, DefaultSweepInterval, *cfg.SweepInterval)
	})

	T.Run("a zero sweep interval is a deployment asking for no sweeper", func(t *testing.T) {
		t.Parallel()

		// Unset and zero are different answers, which is the whole reason the
		// field is a pointer: a scheduler calling Sweep for the fleet wants no
		// per-replica sweeper at all.
		cfg := &Config{SweepInterval: pointer.To(time.Duration(0))}
		cfg.EnsureDefaults()

		must.NotNil(t, cfg.SweepInterval)
		test.EqOp(t, time.Duration(0), *cfg.SweepInterval)
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
			SweepInterval:  pointer.To(time.Minute),
		}
		cfg.EnsureDefaults()

		test.EqOp(t, time.Hour, cfg.Retention)
		test.EqOp(t, 64, cfg.TokenBytes)
		test.EqOp(t, 128, cfg.MaxTokenLength)
		test.EqOp(t, time.Minute, *cfg.SweepInterval)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a valid config", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, testConfig().ValidateWithContext(t.Context()))
	})

	T.Run("rejects a negative retention", func(t *testing.T) {
		t.Parallel()

		cfg := testConfig()
		cfg.Retention = -time.Second

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects a negative sweep interval", func(t *testing.T) {
		t.Parallel()

		cfg := testConfig()
		cfg.SweepInterval = pointer.To(-time.Second)

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	// There is no provider to skip it under any more: the store's rules always
	// apply, so a table prefix the schema cannot render is a startup failure
	// rather than a field nothing reads.
	T.Run("rejects an invalid database config", func(t *testing.T) {
		t.Parallel()

		cfg := testConfig()
		cfg.Database.TablePrefix = "trailing_"

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewMinter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		minter, err := NewMinter(t.Context(), testConfig(), newLinksDB(t))
		must.NoError(t, err)

		link, err := minter.Mint(t.Context(), "magic_login", "user_123")
		must.NoError(t, err)
		test.StrHasPrefix(t, "https://app.example.com/auth/magic/", link.URL)

		claims, err := minter.Redeem(t.Context(), link.Token)
		must.NoError(t, err)
		test.EqOp(t, links.Subject("user_123"), claims.Subject)

		_, err = minter.Redeem(t.Context(), link.Token)
		test.ErrorIs(t, err, links.ErrLinkAlreadyRedeemed)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewMinter(t.Context(), nil, newLinksDB(t))
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	// The client is required now that records live in a table, where it used to
	// be optional for a deployment storing links in a cache. A missing one is
	// refused here rather than at the first mint.
	T.Run("rejects a nil database client", func(t *testing.T) {
		t.Parallel()

		_, err := NewMinter(t.Context(), testConfig(), nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("reports a config with no actions", func(t *testing.T) {
		t.Parallel()

		cfg := testConfig()
		cfg.Actions = nil

		_, err := NewMinter(t.Context(), cfg, newLinksDB(t))
		test.ErrorIs(t, err, links.ErrNoActions)
	})

	T.Run("reports an action policy the file got wrong", func(t *testing.T) {
		t.Parallel()

		cfg := testConfig()
		cfg.Actions["verify_email"] = links.ActionPolicy{URL: "https://app.example.com/verify/{token}"}

		_, err := NewMinter(t.Context(), cfg, newLinksDB(t))
		test.ErrorIs(t, err, links.ErrInvalidTTL)
	})

	T.Run("refuses a cleartext action URL unless told otherwise", func(t *testing.T) {
		t.Parallel()

		db := newLinksDB(t)

		cfg := testConfig()
		cfg.Actions["magic_login"] = links.ActionPolicy{
			URL: "http://staging.example.com/auth/magic/{token}",
			TTL: time.Minute,
		}

		_, err := NewMinter(t.Context(), cfg, db)
		test.ErrorIs(t, err, links.ErrInsecureActionURL)

		cfg.AllowInsecureURLs = true

		_, err = NewMinter(t.Context(), cfg, db)
		test.NoError(t, err)
	})

	T.Run("reports a store the config cannot build", func(t *testing.T) {
		t.Parallel()

		// A prefix the schema will not render reaches the store's own
		// constructor, which refuses rather than naming a table nothing
		// created.
		cfg := testConfig()
		cfg.Database.TablePrefix = "trailing_"

		_, err := NewMinter(t.Context(), cfg, newLinksDB(t))
		test.Error(t, err)
	})

	T.Run("caller options win over the file", func(t *testing.T) {
		t.Parallel()

		minter, err := NewMinter(t.Context(), testConfig(), newLinksDB(t),
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

		minter, err := NewMinter(t.Context(), testConfig(), newLinksDB(t),
			WithMinterOptions(links.WithAction("unsubscribe", links.ActionPolicy{
				URL: "https://app.example.com/unsubscribe?t={token}",
				TTL: 24 * time.Hour,
			})))
		must.NoError(t, err)

		test.SliceLen(t, 2, minter.Actions())
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		_, err := NewMinter(t.Context(), testConfig(), newLinksDB(t), nil)
		test.NoError(t, err)
	})

	// The write the store exists to make reachable from configuration: no
	// capability assertion, no provider that answers it and one that does not.
	T.Run("revokes every link a subject holds", func(t *testing.T) {
		t.Parallel()

		minter, err := NewMinter(t.Context(), testConfig(), newLinksDB(t))
		must.NoError(t, err)

		first, err := minter.Mint(t.Context(), "magic_login", "user_123")
		must.NoError(t, err)

		second, err := minter.Mint(t.Context(), "magic_login", "user_123")
		must.NoError(t, err)

		revoked, err := minter.RevokeForSubject(t.Context(), "user_123")
		must.NoError(t, err)
		test.EqOp(t, int64(2), revoked)

		_, err = minter.Redeem(t.Context(), first.Token)
		test.ErrorIs(t, err, links.ErrLinkRevoked)

		_, err = minter.Redeem(t.Context(), second.Token)
		test.ErrorIs(t, err, links.ErrLinkRevoked)
	})
}

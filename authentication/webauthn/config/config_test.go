package webauthncfg

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	webauthncache "github.com/primandproper/platform-go/v13/authentication/webauthn/cache"
	webauthndatabase "github.com/primandproper/platform-go/v13/authentication/webauthn/database"
	cachecfg "github.com/primandproper/platform-go/v13/cache/config"
	"github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	// The database provider is the default, and the reason is the alternative:
	// an unconfigured cache is a memory cache, which holds a challenge on the
	// replica that issued it.
	T.Run("selects the database provider and its sweep", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, ProviderDatabase, cfg.Provider)
		test.EqOp(t, DefaultSweepInterval, cfg.SweepInterval)
		test.EqOp(t, webauthn.DefaultCeremonyTimeout, cfg.RelyingParty.CeremonyTimeout)
	})

	// The cache reclaims its own entries, so a sweep interval under it would be
	// a setting that does nothing — which is worse than an empty one, because
	// it reads as configured.
	T.Run("leaves the sweep unset under the cache provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderCache}
		cfg.EnsureDefaults()

		test.EqOp(t, time.Duration(0), cfg.SweepInterval)
	})

	T.Run("leaves configured values alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderDatabase, SweepInterval: time.Hour}
		cfg.RelyingParty.CeremonyTimeout = 30 * time.Second
		cfg.EnsureDefaults()

		test.EqOp(t, time.Hour, cfg.SweepInterval)
		test.EqOp(t, 30*time.Second, cfg.RelyingParty.CeremonyTimeout)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts either provider", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, databaseConfig().ValidateWithContext(t.Context()))
		must.NoError(t, cacheConfig().ValidateWithContext(t.Context()))
	})

	// Normalized before it is checked, matching dispatch: validating the raw
	// string would reject a provider NewSessionStore would happily build.
	T.Run("accepts a provider however it is spelled", func(t *testing.T) {
		t.Parallel()

		for _, spelling := range []string{"database", " Database ", "DATABASE"} {
			cfg := databaseConfig()
			cfg.Provider = spelling

			test.NoError(t, cfg.ValidateWithContext(t.Context()), test.Sprintf("provider %q", spelling))
		}
	})

	T.Run("rejects a provider it does not implement", func(t *testing.T) {
		t.Parallel()

		cfg := databaseConfig()
		cfg.Provider = "a-filing-cabinet"

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	// The relying party is validated under every provider, because it is what
	// runs the ceremonies whichever one holds them.
	T.Run("rejects a relying party no ceremony could run under", func(t *testing.T) {
		t.Parallel()

		cfg := databaseConfig()
		cfg.RelyingParty.RPOrigins = nil

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	// The unselected provider's sub-config is skipped rather than merely
	// unguarded: `env:",init"` populates every sub-config, so validating the
	// cache's rules under the database provider would make a perfectly good
	// database configuration unloadable.
	T.Run("skips the sub-config of the provider it did not select", func(t *testing.T) {
		t.Parallel()

		cfg := databaseConfig()
		cfg.Cache = cachecfg.Config{Provider: "a-filing-cabinet"}

		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		cfg = cacheConfig()
		cfg.Database = webauthndatabase.Config{TablePrefix: "ddb_"}

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("validates the sub-config of the provider it did select", func(t *testing.T) {
		t.Parallel()

		cfg := databaseConfig()
		cfg.Database = webauthndatabase.Config{TablePrefix: "ddb_"}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewSessionStore(T *testing.T) {
	T.Parallel()

	T.Run("builds the database store", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(t.Context(), databaseConfig(), newTestClient(t))
		must.NoError(t, err)
		must.NotNil(t, store)

		_, ok := store.(*webauthndatabase.SessionStore)
		test.True(t, ok)

		// It is a store that works, not merely one that was constructed.
		must.NoError(t, store.Save(t.Context(), testSession("built"), time.Minute))

		session, err := store.Consume(t.Context(), "built")
		must.NoError(t, err)
		test.EqOp(t, "built", session.Challenge)
	})

	T.Run("builds the cache store", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(t.Context(), cacheConfig(), nil)
		must.NoError(t, err)

		_, ok := store.(*webauthncache.SessionStore)
		test.True(t, ok)

		must.NoError(t, store.Save(t.Context(), testSession("built"), time.Minute))

		session, err := store.Consume(t.Context(), "built")
		must.NoError(t, err)
		test.EqOp(t, "built", session.Challenge)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(t.Context(), nil, nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
		test.Nil(t, store)
	})

	// The database provider with no client is the loud failure the default is
	// chosen for. A store that cannot be built is a service that does not
	// start, rather than one that starts and loses ceremonies.
	T.Run("refuses the database provider with no client", func(t *testing.T) {
		t.Parallel()

		store, err := NewSessionStore(t.Context(), databaseConfig(), nil)
		test.ErrorIs(t, err, webauthndatabase.ErrNilClient)

		// Nil as an interface, not a non-nil interface holding a nil pointer:
		// the concrete constructors return their own types, and returning one
		// straight through on the error path would hand back a value that
		// passes a nil check and panics on first use.
		test.Nil(t, store)
	})

	T.Run("refuses a provider it does not implement", func(t *testing.T) {
		t.Parallel()

		cfg := databaseConfig()
		cfg.Provider = "a-filing-cabinet"

		store, err := NewSessionStore(t.Context(), cfg, newTestClient(t))
		test.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("reports a cache it cannot build", func(t *testing.T) {
		t.Parallel()

		cfg := cacheConfig()
		cfg.Cache = cachecfg.Config{Provider: cachecfg.ProviderRedis}

		store, err := NewSessionStore(t.Context(), cfg, nil)
		test.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("passes provider options through", func(t *testing.T) {
		t.Parallel()

		// The pass-through slots exist because Go allows one variadic per
		// function, and this package's Option owns it.
		store, err := NewSessionStore(t.Context(), databaseConfig(), newTestClient(t),
			WithDatabaseStoreOptions(webauthndatabase.WithCodec(nil)),
			WithCacheStoreOptions(webauthncache.WithLogger(nil)))
		must.NoError(t, err)
		test.NotNil(t, store)
	})
}

func TestNewRelyingParty(T *testing.T) {
	T.Parallel()

	T.Run("builds a relying party over the configured store", func(t *testing.T) {
		t.Parallel()

		rp, err := NewRelyingParty(t.Context(), databaseConfig(), newTestClient(t))
		must.NoError(t, err)
		must.NotNil(t, rp)

		// A ceremony begins, which means the relying party and the store under
		// it were wired to each other rather than merely built.
		creation, err := rp.BeginRegistration(t.Context(), &testUser{})
		must.NoError(t, err)
		test.EqOp(t, "example.com", creation.Response.RelyingParty.ID)
	})

	T.Run("refuses a config whose store cannot be built", func(t *testing.T) {
		t.Parallel()

		rp, err := NewRelyingParty(t.Context(), databaseConfig(), nil)
		test.ErrorIs(t, err, webauthndatabase.ErrNilClient)
		test.Nil(t, rp)
	})

	T.Run("refuses a relying party no ceremony could run under", func(t *testing.T) {
		t.Parallel()

		cfg := cacheConfig()
		cfg.RelyingParty.RPID = ""

		rp, err := NewRelyingParty(t.Context(), cfg, nil)
		test.Error(t, err)
		test.Nil(t, rp)
	})
}

// testUser is the adapter an application writes, minimal enough to begin a
// ceremony.
type testUser struct{}

var _ webauthn.User = (*testUser)(nil)

func (*testUser) WebAuthnID() []byte                         { return []byte("user-handle") }
func (*testUser) WebAuthnName() string                       { return "user" }
func (*testUser) WebAuthnDisplayName() string                { return "User" }
func (*testUser) WebAuthnCredentials() []webauthn.Credential { return nil }

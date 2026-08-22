package sessionscfg

import (
	"encoding/base64"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	cachecfg "github.com/primandproper/platform-go/v13/cache/config"
	"github.com/primandproper/platform-go/v13/cookies"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/sessions"
	sessionsdatabase "github.com/primandproper/platform-go/v13/sessions/database"
	"github.com/primandproper/platform-go/v13/sessions/database/migrations"
	sessionshttp "github.com/primandproper/platform-go/v13/sessions/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// principal is the payload the tests store.
type principal struct {
	UserID string
}

// memoryConfig is the smallest working cache-backed configuration.
func memoryConfig() *Config {
	return &Config{
		Provider: ProviderCache,
		Cache:    cachecfg.Config{Provider: cachecfg.ProviderMemory, Expiry: time.Hour},
	}
}

// newTestClient builds a SQLite client with the session table created.
func newTestClient(t *testing.T, prefix string) database.Client {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "sessions.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr)
	}

	return client
}

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 3 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills in the timeouts, provider, and cookie name", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, ProviderCache, cfg.Provider)
		test.EqOp(t, sessions.DefaultAbsoluteTimeout, cfg.AbsoluteTimeout)
		test.EqOp(t, sessions.DefaultIdleTimeout, cfg.IdleTimeout)
		test.EqOp(t, sessionshttp.DefaultCookieName, cfg.CookieName)
	})

	T.Run("leaves configured values alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:        ProviderDatabase,
			AbsoluteTimeout: time.Hour,
			IdleTimeout:     time.Minute,
			CookieName:      "sid",
			SweepInterval:   time.Second,
		}
		cfg.EnsureDefaults()

		test.EqOp(t, ProviderDatabase, cfg.Provider)
		test.EqOp(t, time.Hour, cfg.AbsoluteTimeout)
		test.EqOp(t, time.Minute, cfg.IdleTimeout)
		test.EqOp(t, "sid", cfg.CookieName)
		test.EqOp(t, time.Second, cfg.SweepInterval)
	})

	// A cache reclaims its own entries, so defaulting a sweep interval for it
	// would configure a component that does not exist.
	T.Run("defaults a sweep interval only for the database provider", func(t *testing.T) {
		t.Parallel()

		cacheCfg := &Config{Provider: ProviderCache}
		cacheCfg.EnsureDefaults()
		test.EqOp(t, time.Duration(0), cacheCfg.SweepInterval)

		dbCfg := &Config{Provider: ProviderDatabase}
		dbCfg.EnsureDefaults()
		test.EqOp(t, DefaultSweepInterval, dbCfg.SweepInterval)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a well-formed cache configuration", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, memoryConfig().ValidateWithContext(t.Context()))
	})

	T.Run("rejects a provider it does not implement", func(t *testing.T) {
		t.Parallel()

		cfg := memoryConfig()
		cfg.Provider = "etcd"

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	// `env:",init"` leaves every sub-config populated, so validating the one
	// that was not selected would make a perfectly good configuration
	// unloadable.
	T.Run("skips the sub-config of the provider that was not selected", func(t *testing.T) {
		t.Parallel()

		// A cache config that could not build, under the database provider.
		cfg := &Config{
			Provider: ProviderDatabase,
			Cache:    cachecfg.Config{Provider: "nonsense"},
		}

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects a table prefix the schema cannot render", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderDatabase,
			Database: sessionsdatabase.Config{TablePrefix: "trailing_"},
		}

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects negative timeouts", func(t *testing.T) {
		t.Parallel()

		cfg := memoryConfig()
		cfg.IdleTimeout = -time.Minute

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("requires a config", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore[principal](t.Context(), nil, nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("builds a working cache-backed store with no database client", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore[principal](t.Context(), memoryConfig(), nil)
		must.NoError(t, err)

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		read, err := store.Get(t.Context(), session.ID)
		must.NoError(t, err)
		test.EqOp(t, "u_1", read.Data.UserID)
	})

	T.Run("builds a working database-backed store", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderDatabase}

		store, err := NewStore[principal](t.Context(), cfg, newTestClient(t, ""))
		must.NoError(t, err)

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		read, err := store.Get(t.Context(), session.ID)
		must.NoError(t, err)
		test.EqOp(t, "u_1", read.Data.UserID)
	})

	// newBackend is the narrowing seam: sessions/database.NewBackend returns its
	// own *Backend[T], and returning that straight through made a nil pointer a
	// non-nil sessions.Backend[T] on every one of its six failure paths.
	// Exercised through newBackend rather than NewStore because NewStore checks
	// the error and discards the value, so the trap is invisible from outside.
	T.Run("a failed database backend is a nil interface, not a typed nil", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderDatabase}
		cfg.EnsureDefaults()

		backend, err := newBackend[principal](t.Context(), cfg, nil, newOptions(nil))
		test.ErrorIs(t, err, sessionsdatabase.ErrNilClient)

		// Compared against nil directly rather than with test.Nil, which is
		// satisfied by a nil pointer inside a non-nil interface — the exact
		// value this asserts is absent.
		test.True(t, backend == nil)
	})

	// An unrecognized provider must not fall back to something that works in
	// development and signs everybody out in production.
	T.Run("refuses a provider it does not implement", func(t *testing.T) {
		t.Parallel()

		cfg := memoryConfig()
		cfg.Provider = "etcd"

		_, err := NewStore[principal](t.Context(), cfg, nil)
		must.Error(t, err)
	})

	T.Run("applies the configured timeouts to the store", func(t *testing.T) {
		t.Parallel()

		cfg := memoryConfig()
		cfg.AbsoluteTimeout = 2 * time.Hour
		cfg.IdleTimeout = 20 * time.Minute
		cfg.TouchInterval = 30 * time.Second

		store, err := NewStore[principal](t.Context(), cfg, nil)
		must.NoError(t, err)

		policy := store.Policy()
		test.EqOp(t, 2*time.Hour, policy.Absolute)
		test.EqOp(t, 20*time.Minute, policy.Idle)
		test.EqOp(t, 30*time.Second, policy.Touch)
	})

	// Zero is a meaningful touch interval — refresh on every read — but it is
	// also what an unset field looks like, so the option is only applied when
	// the field says something.
	T.Run("leaves the store's default touch interval alone when unconfigured", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore[principal](t.Context(), memoryConfig(), nil)
		must.NoError(t, err)

		test.EqOp(t, sessions.DefaultTouchInterval, store.Policy().Touch)
	})

	// The pass-through slot: caller options are applied last, so they win over
	// anything the Config derived.
	T.Run("caller options win over the config", func(t *testing.T) {
		t.Parallel()

		cfg := memoryConfig()
		cfg.IdleTimeout = 20 * time.Minute

		store, err := NewStore[principal](t.Context(), cfg, nil,
			WithStoreOptions(sessions.WithIdleTimeout(5*time.Minute)))
		must.NoError(t, err)

		test.EqOp(t, 5*time.Minute, store.Policy().Idle)
	})

	T.Run("validates before it builds", func(t *testing.T) {
		t.Parallel()

		cfg := memoryConfig()
		cfg.TouchInterval = -time.Second

		_, err := NewStore[principal](t.Context(), cfg, nil)
		must.Error(t, err)
	})

	T.Run("defaults an empty config to a working cache store", func(t *testing.T) {
		t.Parallel()

		// What a service that sets no SESSIONS_ variables at all gets: the
		// memory provider, which is wrong for a fleet and right for a test, and
		// the documented timeouts.
		cfg := &Config{Cache: cachecfg.Config{Provider: cachecfg.ProviderMemory}}

		store, err := NewStore[principal](t.Context(), cfg, nil)
		must.NoError(t, err)
		test.EqOp(t, sessions.DefaultIdleTimeout, store.Policy().Idle)
	})
}

func TestNewManager(T *testing.T) {
	T.Parallel()

	T.Run("requires a config", func(t *testing.T) {
		t.Parallel()

		_, err := NewManager[principal](t.Context(), nil, nil, nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("requires a cookie manager", func(t *testing.T) {
		t.Parallel()

		_, err := NewManager[principal](t.Context(), memoryConfig(), nil, nil)
		test.ErrorIs(t, err, sessionshttp.ErrNilCookieManager)
	})

	T.Run("builds a manager over the configured store", func(t *testing.T) {
		t.Parallel()

		manager, err := NewManager[principal](t.Context(), memoryConfig(), nil, newTestCookieManager(t))
		must.NoError(t, err)
		must.NotNil(t, manager)
	})

	T.Run("names the cookie from the config", func(t *testing.T) {
		t.Parallel()

		cfg := memoryConfig()
		cfg.CookieName = "sid"

		manager, err := NewManager[principal](t.Context(), cfg, nil, newTestCookieManager(t))
		must.NoError(t, err)

		res := httptest.NewRecorder()

		_, err = manager.Issue(t.Context(), res, &principal{UserID: "u_1"})
		must.NoError(t, err)

		set := res.Result().Cookies()
		must.SliceLen(t, 1, set)
		test.EqOp(t, "sid", set[0].Name)
	})
}

// newTestCookieManager builds a cookie manager with throwaway keys.
func newTestCookieManager(t *testing.T) cookies.Manager {
	t.Helper()

	manager, err := cookies.NewCookieManager(&cookies.Config{
		Base64EncodedHashKey:  base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
		Base64EncodedBlockKey: base64.StdEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210")),
		Lifetime:              24 * time.Hour,
	})
	must.NoError(t, err)

	return manager
}

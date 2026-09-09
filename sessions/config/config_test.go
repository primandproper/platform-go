package sessionscfg

import (
	"encoding/base64"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/platform-go/v14/sessions"
	sessionsdatabase "github.com/primandproper/platform-go/v14/sessions/database"
	"github.com/primandproper/platform-go/v14/sessions/database/migrations"
	sessionshttp "github.com/primandproper/platform-go/v14/sessions/http"

	cachecfg "github.com/primandproper/primitives-go/cache/config"
	"github.com/primandproper/primitives-go/cookies"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	"github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/pointer"

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
			SweepInterval:   pointer.To(time.Second),
		}
		cfg.EnsureDefaults()

		test.EqOp(t, ProviderDatabase, cfg.Provider)
		test.EqOp(t, time.Hour, cfg.AbsoluteTimeout)
		test.EqOp(t, time.Minute, cfg.IdleTimeout)
		test.EqOp(t, "sid", cfg.CookieName)
		test.EqOp(t, time.Second, pointer.Dereference(cfg.SweepInterval))
	})

	// A cache reclaims its own entries, so defaulting a sweep interval for it
	// would configure a component that does not exist.
	T.Run("defaults a sweep interval only for the database provider", func(t *testing.T) {
		t.Parallel()

		cacheCfg := &Config{Provider: ProviderCache}
		cacheCfg.EnsureDefaults()
		test.Nil(t, cacheCfg.SweepInterval)

		dbCfg := &Config{Provider: ProviderDatabase}
		dbCfg.EnsureDefaults()
		test.EqOp(t, DefaultSweepInterval, pointer.Dereference(dbCfg.SweepInterval))
	})

	// Nil is the default and a spelled zero is the off-switch, which is only
	// true while this method can tell them apart. Defaulting the zero would put
	// the off-switch back out of reach.
	T.Run("leaves a spelled zero alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderDatabase, SweepInterval: pointer.To(time.Duration(0))}
		cfg.EnsureDefaults()

		test.EqOp(t, time.Duration(0), pointer.Dereference(cfg.SweepInterval))
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

	T.Run("accepts the off-switch by rule rather than by omission", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderDatabase, SweepInterval: pointer.To(time.Duration(0))}
		cfg.EnsureDefaults()

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	// Below zero there is no cadence to configure — every negative duration
	// reaches the backend as "start nothing" — so a magnitude is somebody
	// describing a sweep they will not get.
	T.Run("rejects a negative interval", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderDatabase, SweepInterval: pointer.To(-30 * time.Minute)}
		cfg.EnsureDefaults()

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

// TestConfig_SweepInterval exercises the field's documented contract where it
// is actually decided. The store's own WithSweeper has always honored a
// non-positive interval; what could not reach it was a configured one, because
// EnsureDefaults mapped every zero to the default before the option was built.
func TestConfig_SweepInterval(T *testing.T) {
	T.Parallel()

	// The wall clock is deliberate: inside a synctest bubble clock.NewClock
	// reads the bubble's time, so the sweeper's ticker advances with
	// time.Sleep and the store needs no test double.
	T.Run("a zero interval leaves dead rows for somebody else to remove", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t, "")

			store, err := NewStore[principal](t.Context(), &Config{
				Provider:        ProviderDatabase,
				AbsoluteTimeout: time.Minute,
				IdleTimeout:     time.Minute,
				SweepInterval:   pointer.To(time.Duration(0)),
			}, client)
			must.NoError(t, err)

			_, err = store.New(t.Context(), &principal{UserID: "u_1"})
			must.NoError(t, err)

			// Well past the row's deadline — which is the session's plus
			// Policy.Grace, since the store keeps an expired record around
			// long enough to tell a signed-out user why — and past every tick
			// a defaulted interval would have taken.
			time.Sleep(3 * time.Hour)
			synctest.Wait()

			// Still there, waiting for the scheduled Sweep this deployment runs
			// instead. While EnsureDefaults could not tell a zero from an
			// unset field the row was gone by now, and the configuration
			// said it would not be.
			test.EqOp(t, 1, sessionRowCount(t, client))
		})
	})

	T.Run("an unset interval sweeps, because a table nobody sweeps only grows", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t, "")

			store, err := NewStore[principal](t.Context(), &Config{
				Provider:        ProviderDatabase,
				AbsoluteTimeout: time.Minute,
				IdleTimeout:     time.Minute,
			}, client)
			must.NoError(t, err)

			_, err = store.New(t.Context(), &principal{UserID: "u_1"})
			must.NoError(t, err)

			time.Sleep(3 * time.Hour)
			synctest.Wait()

			test.EqOp(t, 0, sessionRowCount(t, client))
		})
	})
}

// sessionRowCount reads the session table directly, because a Store read
// refuses an expired session whether or not the row is still there — and
// whether the row is still there is the whole question.
func sessionRowCount(t *testing.T, client database.Client) int {
	t.Helper()

	var count int
	must.NoError(t, client.Reader().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sessions").Scan(&count))

	return count
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

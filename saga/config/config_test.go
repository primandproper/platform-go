package sagacfg

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/distributedlock"
	lockmemory "github.com/primandproper/platform-go/v13/distributedlock/memory"
	"github.com/primandproper/platform-go/v13/saga"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

func newClient(t *testing.T) database.Client {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "saga.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return client
}

func newLocker(t *testing.T) distributedlock.ScopedLocker {
	t.Helper()

	raw, err := lockmemory.NewLocker()
	must.NoError(t, err)

	scoped, err := distributedlock.NewScopedLocker(raw)
	must.NoError(t, err)

	return scoped
}

func validConfig() *Config {
	cfg := &Config{}
	cfg.EnsureDefaults()

	return cfg
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills the prefix, the topic, and the worker", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, saga.DefaultTablePrefix, cfg.TablePrefix)
		test.EqOp(t, saga.DefaultEventTopic, cfg.EventTopic)
		test.EqOp(t, saga.DefaultPollInterval, cfg.Worker.PollInterval)
	})

	T.Run("leaves set values alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			TablePrefix: "app_saga",
			EventTopic:  "sagas",
		}
		cfg.EnsureDefaults()

		test.EqOp(t, "app_saga", cfg.TablePrefix)
		test.EqOp(t, "sagas", cfg.EventTopic)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a defaulted config", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, validConfig().ValidateWithContext(t.Context()))
	})

	T.Run("rejects a worker whose advance timeout cannot fit a step", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Worker.StepTimeout = time.Hour
		cfg.Worker.AdvanceTimeout = time.Second

		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)

		// ozzo collects field errors into a map that does not unwrap, so the
		// assertion is on the rendering rather than on errors.Is.
		test.StrContains(t, err.Error(), "must be at least the step timeout")
	})

	T.Run("rejects a worker config that cannot be satisfied", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Worker.LeaseDuration = time.Second

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("builds a store", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), validConfig(), newClient(t))
		must.NoError(t, err)
		must.NotNil(t, store)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore(t.Context(), nil, newClient(t))
		test.Error(t, err)
	})

	T.Run("rejects an invalid config", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Worker.StepTimeout = time.Hour
		cfg.Worker.AdvanceTimeout = time.Second

		_, err := NewStore(t.Context(), cfg, newClient(t))
		test.Error(t, err)
	})

	T.Run("propagates a store construction failure", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore(t.Context(), validConfig(), nil)
		test.ErrorIs(t, err, saga.ErrNilDatabaseClient)
	})
}

func TestNewWorker(T *testing.T) {
	T.Parallel()

	registry := func(t *testing.T) *saga.Registry {
		t.Helper()

		r := saga.NewRegistry()
		must.NoError(t, saga.Register(r, saga.Definition[struct{}]{
			Name: "orders",
			Steps: []saga.Step[struct{}]{{
				Name: "one",
				Do:   func(_ context.Context, _ *struct{}) error { return nil },
			}},
		}))

		return r
	}

	T.Run("builds a worker", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), validConfig(), newClient(t))
		must.NoError(t, err)

		worker, err := NewWorker(
			t.Context(),
			validConfig(),
			store,
			registry(t),
			newLocker(t),
			nil,
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, worker)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), validConfig(), newClient(t))
		must.NoError(t, err)

		_, err = NewWorker(t.Context(), nil, store, registry(t), newLocker(t), nil, nil)
		test.Error(t, err)
	})

	T.Run("rejects an invalid config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), validConfig(), newClient(t))
		must.NoError(t, err)

		cfg := validConfig()
		cfg.Worker.StepTimeout = time.Hour
		cfg.Worker.AdvanceTimeout = time.Second

		_, err = NewWorker(t.Context(), cfg, store, registry(t), newLocker(t), nil, nil)
		test.Error(t, err)
	})

	T.Run("propagates a missing locker", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), validConfig(), newClient(t))
		must.NoError(t, err)

		_, err = NewWorker(
			t.Context(),
			validConfig(),
			store,
			registry(t),
			nil,
			nil,
			nil,
		)
		test.ErrorIs(t, err, saga.ErrNilLocker)
	})
}

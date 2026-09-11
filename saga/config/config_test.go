package sagacfg

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/saga"
	"github.com/primandproper/platform-go/v14/saga/migrations"

	cachememory "github.com/primandproper/primitives-go/v2/cache/memory"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/distributedlock"
	lockmemory "github.com/primandproper/primitives-go/v2/distributedlock/memory"
	"github.com/primandproper/primitives-go/v2/idempotency"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/shoenig/test/wait"
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
		)
		must.NoError(t, err)
		must.NotNil(t, worker)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), validConfig(), newClient(t))
		must.NoError(t, err)

		_, err = NewWorker(t.Context(), nil, store, registry(t), newLocker(t))
		test.Error(t, err)
	})

	T.Run("rejects an invalid config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), validConfig(), newClient(t))
		must.NoError(t, err)

		cfg := validConfig()
		cfg.Worker.StepTimeout = time.Hour
		cfg.Worker.AdvanceTimeout = time.Second

		_, err = NewWorker(t.Context(), cfg, store, registry(t), newLocker(t))
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
		)
		test.ErrorIs(t, err, saga.ErrNilLocker)
	})

	T.Run("the event publisher supplied by option reaches the worker", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)
		migrate(t, client, saga.DefaultTablePrefix)

		cfg := validConfig()
		cfg.Worker.PollInterval = time.Millisecond

		store, err := NewStore(t.Context(), cfg, client)
		must.NoError(t, err)

		reg := registry(t)

		runner, err := saga.NewRunner[struct{}](store, reg)
		must.NoError(t, err)

		_, err = runner.Start(t.Context(), "orders", struct{}{})
		must.NoError(t, err)

		var (
			mu        sync.Mutex
			published []saga.Event
		)

		publisher := saga.EventPublisherFunc(
			func(_ context.Context, _ database.Tx, events ...saga.Event) error {
				mu.Lock()
				defer mu.Unlock()

				published = append(published, events...)

				return nil
			})

		// The manager is supplied too, so that a worker carrying both options is
		// the one actually driven.
		records, err := cachememory.NewInMemoryCache[idempotency.Record[saga.StepResult]](time.Hour)
		must.NoError(t, err)
		t.Cleanup(func() { _ = records.Close() })

		manager, err := idempotency.NewManager(records, newLocker(t),
			idempotency.WithInFlightTTL(time.Minute))
		must.NoError(t, err)

		worker, err := NewWorker(t.Context(), cfg, store, reg, newLocker(t),
			WithWorkerEventPublisher(publisher),
			WithWorkerIdempotency(manager),
		)
		must.NoError(t, err)

		go worker.Run()
		t.Cleanup(func() { _ = worker.Close(context.Background()) })

		// A worker that never received the publisher advances the instance and
		// announces nothing, so the events are the evidence the option arrived.
		must.Wait(t, wait.InitialSuccess(
			wait.BoolFunc(func() bool {
				mu.Lock()
				defer mu.Unlock()

				return len(published) > 0
			}),
			wait.Timeout(10*time.Second),
			wait.Gap(5*time.Millisecond),
		))
	})
}

// migrate renders and applies the saga schema to a SQLite client.
func migrate(t *testing.T, client database.Client, prefix string) {
	t.Helper()

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr)
	}
}

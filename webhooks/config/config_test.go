package webhookscfg

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/tenancy"
	"github.com/primandproper/platform-go/v13/webhooks"
	"github.com/primandproper/platform-go/v13/webhooks/migrations"

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

// newTestClient builds a SQLite-backed client with the webhook tables created.
func newTestClient(t *testing.T) database.Client {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "webhooks.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	stmts, err := migrations.Statements(dialect.SQLite, webhooks.DefaultTablePrefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr)
	}

	return client
}

func validConfig() *Config {
	return &Config{}
}

// invalidConfig is the smallest config that fails validation. A retention
// shorter than a minute is non-zero, so EnsureDefaults leaves it alone rather
// than repairing the config on the way in.
func invalidConfig() *Config {
	cfg := &Config{}
	cfg.EnsureDefaults()
	cfg.Worker.Retention = time.Second

	return cfg
}

var testCatalog = webhooks.Catalog{
	"order.created": {Description: "an order was created"},
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, webhooks.DefaultTablePrefix, cfg.TablePrefix)
		test.EqOp(t, webhooks.DefaultBatchSize, cfg.Worker.BatchSize)
		test.True(t, cfg.HTTPClient.Timeout > 0)
		test.NotEqOp(t, "", cfg.CircuitBreaker.Name)
	})

	T.Run("leaves an explicit prefix alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{TablePrefix: "acme_hook"}
		cfg.EnsureDefaults()

		test.EqOp(t, "acme_hook", cfg.TablePrefix)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects a retention shorter than the floor", func(t *testing.T) {
		t.Parallel()

		// Asserted on the rendering rather than with errors.Is: ozzo's
		// validation.Errors is a map with no Unwrap, so the sentinel does not
		// survive into the chain.
		err := invalidConfig().ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "retention")
	})

	// The nested configs are validated through validation.By closures because
	// ozzo dereferences a struct-value field before checking
	// ValidatableWithContext — without those they would be silently skipped.
	T.Run("surfaces a nested worker failure", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()
		cfg.Worker.RequestTimeout = cfg.Worker.LeaseDuration

		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), webhooks.ErrLeaseTooShort.Error())
	})
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), validConfig(), newTestClient(t))
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore(t.Context(), nil, newTestClient(t))
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore(t.Context(), validConfig(), nil)
		test.ErrorIs(t, err, webhooks.ErrNilDatabaseClient)
	})

	T.Run("invalid config", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore(t.Context(), invalidConfig(), newTestClient(t))
		test.Error(t, err)
	})
}

func TestNewDispatcher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		client := newTestClient(t)

		store, err := NewStore(t.Context(), cfg, client)
		must.NoError(t, err)

		dispatcher, err := NewDispatcher(t.Context(), cfg, store, testCatalog)
		must.NoError(t, err)
		must.NotNil(t, dispatcher)

		// The catalog reached the dispatcher: an event outside it is refused.
		must.NoError(t, client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			test.NoError(t, dispatcher.Dispatch(t.Context(), q, &webhooks.Delivery{
				Scope: tenancy.Global(), EventType: "order.created", Payload: []byte(`{}`),
			}))
			test.ErrorIs(t, dispatcher.Dispatch(t.Context(), q, &webhooks.Delivery{
				Scope: tenancy.Global(), EventType: "order.exploded", Payload: []byte(`{}`),
			}), webhooks.ErrUnknownEventType)

			return nil
		}))
	})

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewDispatcher(t.Context(), nil, nil, testCatalog)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("nil store", func(t *testing.T) {
		t.Parallel()

		_, err := NewDispatcher(t.Context(), validConfig(), nil, testCatalog)
		test.ErrorIs(t, err, webhooks.ErrNilStore)
	})
}

func TestNewWorker(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()

		store, err := NewStore(t.Context(), cfg, newTestClient(t))
		must.NoError(t, err)

		worker, err := NewWorker(t.Context(), cfg, store)
		must.NoError(t, err)
		must.NotNil(t, worker)

		t.Cleanup(func() { _ = worker.Close(t.Context()) })
	})

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewWorker(t.Context(), nil, nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("nil store", func(t *testing.T) {
		t.Parallel()

		_, err := NewWorker(t.Context(), validConfig(), nil)
		test.ErrorIs(t, err, webhooks.ErrNilStore)
	})

	T.Run("an invalid worker section surfaces", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()
		cfg.Worker.RequestTimeout = cfg.Worker.LeaseDuration

		store, err := NewStore(t.Context(), validConfig(), newTestClient(t))
		must.NoError(t, err)

		_, err = NewWorker(t.Context(), cfg, store)
		must.Error(t, err)
		test.StrContains(t, err.Error(), webhooks.ErrLeaseTooShort.Error())
	})
}

func TestEnsureHTTPClient(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()
		cfg.Worker.RequestTimeout = 3 * time.Second

		client, err := EnsureHTTPClient(cfg)
		must.NoError(t, err)
		must.NotNil(t, client)
		test.EqOp(t, 3*time.Second, client.Timeout)
	})

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		client, err := EnsureHTTPClient(nil)
		must.NoError(t, err)
		test.NotNil(t, client)
	})
}

func TestBreakerFactory(T *testing.T) {
	T.Parallel()

	// The factory is what gives each endpoint its own breaker. Building one is
	// the part that can fail, and every breaker carries its endpoint as a metric
	// attribute so a tripped circuit names the subscriber that tripped it.
	T.Run("builds a usable breaker per endpoint", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()

		factory := breakerFactory(t.Context(), cfg, nil, nil)
		must.NotNil(t, factory)

		first, err := factory("endpoint-1")
		must.NoError(t, err)
		must.NotNil(t, first)
		test.True(t, first.CanProceed())

		second, err := factory("endpoint-2")
		must.NoError(t, err)
		must.NotNil(t, second)

		// Distinct instances, so one endpoint tripping does not open another's.
		test.True(t, first != second)
	})

	// The factory reaches the worker, which caches one breaker per endpoint.
	T.Run("reaches the worker", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()

		store, err := NewStore(t.Context(), cfg, newTestClient(t))
		must.NoError(t, err)

		worker, err := NewWorker(t.Context(), cfg, store)
		must.NoError(t, err)
		t.Cleanup(func() { _ = worker.Close(t.Context()) })

		test.NotNil(t, worker)
	})
}

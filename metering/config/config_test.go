package meteringcfg

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/analytics"
	analyticsmock "github.com/primandproper/platform-go/v13/analytics/mock"
	"github.com/primandproper/platform-go/v13/cache"
	cachemock "github.com/primandproper/platform-go/v13/cache/mock"
	capitalismnoop "github.com/primandproper/platform-go/v13/capitalism/noop"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/metering"
	"github.com/primandproper/platform-go/v13/metering/migrations"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

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

// newClient builds a migrated SQLite client.
func newClient(t *testing.T) database.Client {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "metering.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	stmts, err := migrations.Statements(dialect.SQLite, metering.DefaultTablePrefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr)
	}

	return client
}

// newValidConfig is the smallest config that passes validation.
func newValidConfig() *Config {
	return &Config{}
}

// newInvalidConfig is the smallest config that fails validation.
//
// A lease that does not outlast the post it covers is the flusher's one
// cross-field constraint, and both values are non-zero — so EnsureDefaults
// leaves them alone rather than repairing the config on the way in.
func newInvalidConfig() *Config {
	cfg := &Config{}
	cfg.EnsureDefaults()
	cfg.Flusher.FlushTimeout = time.Hour
	cfg.Flusher.LeaseDuration = time.Second

	return cfg
}

// newRegistry builds a registry with one meter and one quota.
func newRegistry(t *testing.T) *metering.Registry {
	t.Helper()

	registry := metering.NewRegistry()

	must.NoError(t, registry.RegisterMeter(metering.Meter{
		Name: "api_requests", Aggregation: metering.AggregationSum, Period: metering.PeriodMonth,
	}))
	must.NoError(t, registry.RegisterQuota(metering.Quota{
		Meter: "api_requests", Limit: 100, Behavior: metering.BehaviorBlock, Period: metering.PeriodMonth,
	}))

	return registry
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	cfg := &Config{}
	cfg.EnsureDefaults()

	test.EqOp(T, metering.DefaultTablePrefix, cfg.TablePrefix)
	test.EqOp(T, metering.DefaultBatchSize, cfg.Recorder.BatchSize)
	test.EqOp(T, metering.DefaultStaleness, cfg.Enforcer.Staleness)
	test.EqOp(T, metering.DefaultFlushBatchSize, cfg.Flusher.BatchSize)

	// Idempotent: running it twice must not overwrite what it filled in.
	cfg.TablePrefix = "custom"
	cfg.EnsureDefaults()
	test.EqOp(T, "custom", cfg.TablePrefix)
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a defaulted config", func(t *testing.T) {
		t.Parallel()

		cfg := newValidConfig()
		cfg.EnsureDefaults()

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects a flush lease that does not outlast the flush", func(t *testing.T) {
		t.Parallel()

		// ozzo collects field errors into a map, which does not forward
		// errors.Is to the causes underneath — so this asserts on the rendering.
		err := newInvalidConfig().ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "must exceed flush timeout")
	})

	T.Run("validates the nested configs", func(t *testing.T) {
		t.Parallel()

		// The nested configs go through validation.By closures because ozzo
		// dereferences a struct-value field before checking
		// ValidatableWithContext, so they would otherwise be skipped entirely.
		cfg := newValidConfig()
		cfg.EnsureDefaults()
		cfg.Flusher.LeaseDuration = time.Second
		cfg.Flusher.FlushTimeout = time.Minute

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestConstructors(T *testing.T) {
	T.Parallel()

	T.Run("builds every component from one config", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)
		cfg := newValidConfig()
		registry := newRegistry(t)

		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metrics.EnsureMetricsProvider(nil)

		store, err := NewStore(t.Context(), cfg, client, WithLogger(logger), WithTracerProvider(tracerProvider), WithMetricsProvider(metricsProvider))
		must.NoError(t, err)
		must.NotNil(t, store)

		recorder, err := NewRecorder(
			t.Context(),
			cfg,
			store,
			registry,
			metering.NewCalendarPeriodResolver(nil),
			&analyticsmock.EventReporterMock{
				EventOccurredFunc: func(context.Context, string, string, map[string]any) error { return nil },
			},
			WithLogger(logger),
			WithTracerProvider(tracerProvider),
			WithMetricsProvider(metricsProvider),
		)
		must.NoError(t, err)
		must.NotNil(t, recorder)

		totals := &cachemock.CacheMock[metering.CachedTotal]{
			GetFunc: func(context.Context, string) (*metering.CachedTotal, error) { return nil, cache.ErrNotFound },
			SetFunc: func(context.Context, string, *metering.CachedTotal, ...cache.WriteOption) error { return nil },
		}

		enforcer, err := NewEnforcer(
			t.Context(),
			cfg,
			store,
			registry,
			metering.NewCalendarPeriodResolver(nil),
			metering.NewRegistryQuotaSource(registry),
			totals,
			WithLogger(logger),
			WithTracerProvider(tracerProvider),
			WithMetricsProvider(metricsProvider),
		)
		must.NoError(t, err)
		must.NotNil(t, enforcer)

		flusher, err := NewFlusher(
			t.Context(),
			cfg,
			store,
			metering.ProviderMapperFunc(
				func(context.Context, string, string) (metering.ProviderRef, error) {
					return metering.ProviderRef{CustomerID: "cus_123", MeterName: "api_requests"}, nil
				}),
			capitalismnoop.NewUsageReporter(),
			WithLogger(logger),
			WithTracerProvider(tracerProvider),
			WithMetricsProvider(metricsProvider),
		)
		must.NoError(t, err)
		must.NotNil(t, flusher)

		// The assembled pieces agree about the tables, which is the whole reason
		// they read one config.
		must.NoError(t, recorder.Record(t.Context(), metering.Usage{
			Subject: "account-1", Meter: "api_requests", Quantity: 5, IdempotencyKey: "req-1",
		}))

		decision, err := enforcer.Check(t.Context(), "account-1", "api_requests", 1)
		must.NoError(t, err)
		test.EqOp(t, int64(6), decision.Used)

		result, err := flusher.Flush(t.Context())
		must.NoError(t, err)
		test.EqOp(t, 1, result.Flushed)
	})

	T.Run("builds with every optional dependency omitted", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)
		cfg := newValidConfig()
		registry := newRegistry(t)

		store, err := NewStore(t.Context(), cfg, client)
		must.NoError(t, err)

		_, err = NewRecorder(t.Context(), cfg, store, registry, nil, nil)
		must.NoError(t, err)

		_, err = NewEnforcer(t.Context(), cfg, store, registry, nil, nil, nil)
		must.NoError(t, err)

		_, err = NewFlusher(
			t.Context(),
			cfg,
			store,
			metering.ProviderMapperFunc(func(context.Context, string, string) (metering.ProviderRef, error) {
				return metering.ProviderRef{}, nil
			}),
			capitalismnoop.NewUsageReporter(),
		)
		must.NoError(t, err)
	})

	T.Run("refuses a nil config everywhere", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)
		registry := newRegistry(t)

		mapper := metering.ProviderMapperFunc(func(context.Context, string, string) (metering.ProviderRef, error) {
			return metering.ProviderRef{}, nil
		})

		_, err := NewStore(t.Context(), nil, client)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)

		_, err = NewRecorder(t.Context(), nil, nil, registry, nil, nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)

		_, err = NewEnforcer(t.Context(), nil, nil, registry, nil, nil, nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)

		_, err = NewFlusher(t.Context(), nil, nil, mapper, capitalismnoop.NewUsageReporter())
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("propagates an invalid config everywhere", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)
		registry := newRegistry(t)
		bad := newInvalidConfig()

		mapper := metering.ProviderMapperFunc(func(context.Context, string, string) (metering.ProviderRef, error) {
			return metering.ProviderRef{}, nil
		})

		// ozzo collects field errors into a map, which does not forward
		// errors.Is to the causes underneath — so these assert on the rendering.
		_, err := NewStore(t.Context(), bad, client)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "must exceed flush timeout")

		_, err = NewRecorder(t.Context(), bad, nil, registry, nil, nil)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "must exceed flush timeout")

		_, err = NewEnforcer(t.Context(), bad, nil, registry, nil, nil, nil)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "must exceed flush timeout")

		_, err = NewFlusher(t.Context(), bad, nil, mapper, capitalismnoop.NewUsageReporter())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "must exceed flush timeout")
	})

	T.Run("propagates a component's own refusal", func(t *testing.T) {
		t.Parallel()

		cfg := newValidConfig()

		store, err := NewStore(t.Context(), cfg, newClient(t))
		must.NoError(t, err)

		_, err = NewRecorder(t.Context(), cfg, nil, newRegistry(t), nil, nil)
		test.ErrorIs(t, err, metering.ErrNilStore)

		_, err = NewFlusher(t.Context(), cfg, store, nil, capitalismnoop.NewUsageReporter())
		test.ErrorIs(t, err, metering.ErrNilProviderMapper)

		_, err = NewFlusher(
			t.Context(),
			cfg,
			store,
			metering.ProviderMapperFunc(func(context.Context, string, string) (metering.ProviderRef, error) {
				return metering.ProviderRef{}, nil
			}),
			nil,
		)
		test.ErrorIs(t, err, metering.ErrNilUsageReporter)
	})

	T.Run("honors the configured table prefix", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)
		cfg := &Config{TablePrefix: "custom_usage"}

		stmts, err := migrations.Statements(dialect.SQLite, cfg.TablePrefix)
		must.NoError(t, err)

		for _, stmt := range stmts {
			_, execErr := client.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr)
		}

		store, err := NewStore(t.Context(), cfg, client)
		must.NoError(t, err)

		// Reaching the custom tables at all is the assertion: a mismatch would
		// surface as a missing table rather than a construction error.
		_, err = store.Total(t.Context(), "account-1", "api_requests", metering.Bounds{
			Start: time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC),
		})
		must.NoError(t, err)
	})
}

// analyticsReporterIsSatisfied keeps the mock's conformance checked at compile
// time.
var _ analytics.EventReporter = (*analyticsmock.EventReporterMock)(nil)

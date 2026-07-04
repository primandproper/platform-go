package distributedlockcfg

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	circuitbreakingcfg "github.com/primandproper/platform-go/v3/circuitbreaking/config"
	"github.com/primandproper/platform-go/v3/database"
	"github.com/primandproper/platform-go/v3/distributedlock"
	pglock "github.com/primandproper/platform-go/v3/distributedlock/postgres"
	redislock "github.com/primandproper/platform-go/v3/distributedlock/redis"
	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v3/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v3/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v3/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// stubDBClient is a minimal database.Client for constructing a postgres locker
// without requiring a real database connection. The locker constructor stores
// the client but does not use it until a lock is acquired.
type stubDBClient struct{}

func (c *stubDBClient) WriteDB() *sql.DB       { return nil }
func (c *stubDBClient) ReadDB() *sql.DB        { return nil }
func (c *stubDBClient) Close() error           { return nil }
func (c *stubDBClient) CurrentTime() time.Time { return time.Now() }
func (c *stubDBClient) RollbackTransaction(_ context.Context, _ database.SQLQueryExecutorAndTransactionManager) {
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("redis provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Provider: RedisProvider,
			Redis: &redislock.Config{
				Addresses: []string{"localhost:6379"},
				KeyPrefix: "lock:",
			},
		}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("postgres provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Provider: PostgresProvider,
			Postgres: &pglock.Config{},
		}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("memory provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: MemoryProvider}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("noop provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: NoopProvider}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("redis without config", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: RedisProvider}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("postgres without config", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: PostgresProvider}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: "made-up"}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("empty provider is valid (noop)", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestProvideLocker(T *testing.T) {
	T.Parallel()

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()
		_, err := ProvideLocker(
			t.Context(),
			nil,
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			metricsnoop.NewMetricsProvider(),
			nil,
		)
		test.ErrorIs(t, err, distributedlock.ErrNilConfig)
	})

	T.Run("memory provider returns a working locker", func(t *testing.T) {
		t.Parallel()
		l, err := ProvideLocker(
			t.Context(),
			&Config{Provider: MemoryProvider},
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			metricsnoop.NewMetricsProvider(),
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, l)
		lock, err := l.Acquire(t.Context(), "k", time.Second)
		must.NoError(t, err)
		must.NoError(t, lock.Release(t.Context()))
	})

	T.Run("noop provider", func(t *testing.T) {
		t.Parallel()
		l, err := ProvideLocker(
			t.Context(),
			&Config{Provider: NoopProvider},
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			metricsnoop.NewMetricsProvider(),
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, l)
	})

	T.Run("unknown provider returns noop", func(t *testing.T) {
		t.Parallel()
		l, err := ProvideLocker(
			t.Context(),
			&Config{Provider: "unknown"},
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			metricsnoop.NewMetricsProvider(),
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, l)
	})

	T.Run("empty provider returns noop", func(t *testing.T) {
		t.Parallel()
		l, err := ProvideLocker(
			t.Context(),
			&Config{},
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			metricsnoop.NewMetricsProvider(),
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, l)
	})

	T.Run("provider with whitespace returns noop", func(t *testing.T) {
		t.Parallel()
		l, err := ProvideLocker(
			t.Context(),
			&Config{Provider: "   "},
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			metricsnoop.NewMetricsProvider(),
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, l)
	})

	T.Run("redis provider", func(t *testing.T) {
		t.Parallel()
		l, err := ProvideLocker(
			t.Context(),
			&Config{
				Provider: RedisProvider,
				Redis: &redislock.Config{
					Addresses: []string{"localhost:6379"},
					KeyPrefix: "lock:",
				},
			},
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			metricsnoop.NewMetricsProvider(),
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, l)
	})

	T.Run("postgres provider", func(t *testing.T) {
		t.Parallel()
		l, err := ProvideLocker(
			t.Context(),
			&Config{
				Provider: PostgresProvider,
				Postgres: &pglock.Config{},
			},
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			metricsnoop.NewMetricsProvider(),
			&stubDBClient{},
		)
		must.NoError(t, err)
		must.NotNil(t, l)
	})

	T.Run("circuit breaker init failure", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			CircuitBreaker: circuitbreakingcfg.Config{
				Name:                   "dlock-breaker",
				ErrorRate:              50,
				MinimumSampleThreshold: 10,
			},
		}

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "dlock-breaker_circuit_breaker_tripped", counterName)
				return &mockmetrics.Int64CounterMock{}, fmt.Errorf("counter init failure")
			},
		}

		l, err := ProvideLocker(
			t.Context(),
			cfg,
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			mp,
			nil,
		)
		must.Error(t, err)
		test.Nil(t, l)
		test.StrContains(t, err.Error(), "distributedlock circuit breaker")

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

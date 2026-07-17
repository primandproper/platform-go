package config

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v5/cache/redis"
	circuitbreakingcfg "github.com/primandproper/platform-go/v5/circuitbreaking/config"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v5/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v5/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

type example struct {
	Name string `json:"name"`
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("memory provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderMemory,
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("redis provider with config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderRedis,
			Redis:    &redis.Config{QueueAddresses: []string{"localhost:6379"}},
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("redis provider missing config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderRedis}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("invalid provider name", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "vault"}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewCache(T *testing.T) {
	T.Parallel()

	T.Run("memory provider", func(t *testing.T) {
		t.Parallel()

		c, err := NewCache[example](t.Context(), &Config{
			Provider: ProviderMemory,
		}, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())

		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("redis provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderRedis,
			Redis:    &redis.Config{QueueAddresses: []string{"localhost:6379"}},
		}
		cfg.CircuitBreaker.Name = "cache-breaker"

		c, err := NewCache[example](
			t.Context(),
			cfg,
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			metricsnoop.NewMetricsProvider(),
		)

		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("redis provider with cluster addresses", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderRedis,
			Redis:    &redis.Config{QueueAddresses: []string{"localhost:6379", "localhost:6380"}},
		}
		cfg.CircuitBreaker.Name = "cache-breaker-cluster"

		c, err := NewCache[example](
			t.Context(),
			cfg,
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			metricsnoop.NewMetricsProvider(),
		)

		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("redis provider with circuit breaker error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderRedis,
			Redis:    &redis.Config{QueueAddresses: []string{"localhost:6379"}},
			CircuitBreaker: circuitbreakingcfg.Config{
				Name:                   "redis-cache-breaker",
				ErrorRate:              50,
				MinimumSampleThreshold: 10,
			},
		}

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "redis-cache-breaker_circuit_breaker_tripped", name)
				return nil, errors.New("counter init failure")
			},
		}

		c, err := NewCache[example](
			t.Context(),
			cfg,
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			mp,
		)

		must.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()

		_, err := NewCache[example](t.Context(), &Config{}, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())

		test.Error(t, err)
	})
}

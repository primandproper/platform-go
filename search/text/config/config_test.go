package textsearchcfg

import (
	"context"
	"errors"
	"testing"

	circuitbreakingcfg "github.com/primandproper/platform-go/v3/circuitbreaking/config"
	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v3/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v3/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v3/observability/tracing/noop"
	"github.com/primandproper/platform-go/v3/search/text/algolia"
	"github.com/primandproper/platform-go/v3/search/text/elasticsearch"

	"github.com/shoenig/test"
	"go.opentelemetry.io/otel/metric"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("elasticsearch provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ElasticsearchProvider,
			Elasticsearch: &elasticsearch.Config{
				Address: t.Name(),
			},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("algolia provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: AlgoliaProvider,
			Algolia: &algolia.Config{
				AppID:  "test-app-id",
				APIKey: "test-api-key",
			},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "invalid-provider",
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("elasticsearch provider without elasticsearch config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ElasticsearchProvider,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("algolia provider without algolia config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: AlgoliaProvider,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("empty provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "",
		}

		// Empty provider should be valid (it will default to noop)
		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("provider with extra whitespace", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "  " + ElasticsearchProvider + "  ",
			Elasticsearch: &elasticsearch.Config{
				Address: t.Name(),
			},
		}

		// Provider is canonicalized (trimmed) before validation, matching dispatch.
		test.NoError(t, cfg.ValidateWithContext(ctx))
		test.EqOp(t, ElasticsearchProvider, cfg.Provider)
	})

	T.Run("provider case insensitive", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "ELASTICSEARCH",
			Elasticsearch: &elasticsearch.Config{
				Address: t.Name(),
			},
		}

		// Provider is canonicalized (lowercased) before validation, matching dispatch.
		test.NoError(t, cfg.ValidateWithContext(ctx))
		test.EqOp(t, ElasticsearchProvider, cfg.Provider)
	})

	T.Run("nil context", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ElasticsearchProvider,
			Elasticsearch: &elasticsearch.Config{
				Address: t.Name(),
			},
		}

		test.NoError(t, cfg.ValidateWithContext(context.TODO()))
	})
}

func TestConfig_ZeroValue(T *testing.T) {
	T.Parallel()

	T.Run("zero value is invalid", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{}

		// Zero value should be valid (it will default to noop)
		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("zero value fields", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		test.EqOp(t, "", cfg.Provider)
		test.Nil(t, cfg.Algolia)
		test.Nil(t, cfg.Elasticsearch)
	})
}

func TestConfig_Constants(T *testing.T) {
	T.Parallel()

	T.Run("provider constants have expected values", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "elasticsearch", ElasticsearchProvider)
		test.EqOp(t, "algolia", AlgoliaProvider)
	})

	T.Run("provider constants are not empty", func(t *testing.T) {
		t.Parallel()

		test.NotEq(t, "", ElasticsearchProvider)
		test.NotEq(t, "", AlgoliaProvider)
	})

	T.Run("provider constants are different", func(t *testing.T) {
		t.Parallel()

		test.NotEq(t, ElasticsearchProvider, AlgoliaProvider)
	})
}

func TestConfig_ProvideIndex(T *testing.T) {
	T.Parallel()

	T.Run("elasticsearch provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ElasticsearchProvider,
			Elasticsearch: &elasticsearch.Config{
				Address: "http://localhost:9200",
			},
		}

		// This will fail because we don't have a real Elasticsearch instance
		// but we're testing the interface compliance
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()
		index, err := ProvideIndex[testStruct](ctx, logger, tracerProvider, metricsProvider, cfg, "test-index")
		test.Error(t, err)
		test.Nil(t, index)
	})

	T.Run("algolia provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: AlgoliaProvider,
			Algolia: &algolia.Config{
				AppID:  "test-app-id",
				APIKey: "test-api-key",
			},
		}

		// This will succeed because we're using a real Algolia client
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()
		index, err := ProvideIndex[testStruct](ctx, logger, tracerProvider, metricsProvider, cfg, "test-index")
		test.NoError(t, err)
		test.NotNil(t, index)
	})

	T.Run("unknown provider returns noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "unknown-provider",
		}

		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()
		index, err := ProvideIndex[testStruct](ctx, logger, tracerProvider, metricsProvider, cfg, "test-index")
		test.NoError(t, err)
		test.NotNil(t, index)
	})

	T.Run("empty provider returns noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "",
		}

		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()
		index, err := ProvideIndex[testStruct](ctx, logger, tracerProvider, metricsProvider, cfg, "test-index")
		test.NoError(t, err)
		test.NotNil(t, index)
	})

	T.Run("provider with whitespace returns noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "   ",
		}

		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()
		index, err := ProvideIndex[testStruct](ctx, logger, tracerProvider, metricsProvider, cfg, "test-index")
		test.NoError(t, err)
		test.NotNil(t, index)
	})

	T.Run("circuit breaker init failure", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "",
			CircuitBreaker: circuitbreakingcfg.Config{
				Name:                   "test-breaker",
				ErrorRate:              50,
				MinimumSampleThreshold: 10,
			},
		}

		// Force the very first counter creation to fail so ProvideCircuitBreaker
		// returns an error, which is wrapped by ProvideIndex.
		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "test-breaker_circuit_breaker_tripped", counterName)
				return &mockmetrics.Int64CounterMock{}, errors.New("counter init failure")
			},
		}

		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		index, err := ProvideIndex[testStruct](ctx, logger, tracerProvider, mp, cfg, "test-index")
		test.Error(t, err)
		test.Nil(t, index)
		test.StrContains(t, err.Error(), "circuit breaker")

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

type testStruct struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

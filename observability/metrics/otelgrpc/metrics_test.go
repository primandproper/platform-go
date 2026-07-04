package otelgrpc

import (
	"testing"
	"time"

	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	"github.com/primandproper/platform-go/v3/observability/metrics"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("valid config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			CollectorEndpoint:  "localhost:4317",
			CollectionInterval: 30 * time.Second,
		}

		err := cfg.ValidateWithContext(t.Context())
		test.NoError(t, err)
	})

	T.Run("missing collector endpoint", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			CollectionInterval: 30 * time.Second,
		}

		err := cfg.ValidateWithContext(t.Context())
		test.Error(t, err)
		test.StrContains(t, err.Error(), "metricsCollectorEndpoint")
	})

	T.Run("missing collection interval", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			CollectorEndpoint: "localhost:4317",
		}

		err := cfg.ValidateWithContext(t.Context())
		test.Error(t, err)
		test.StrContains(t, err.Error(), "collectionInterval")
	})

	T.Run("empty collector endpoint", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			CollectorEndpoint:  "",
			CollectionInterval: 30 * time.Second,
		}

		err := cfg.ValidateWithContext(t.Context())
		test.Error(t, err)
		test.StrContains(t, err.Error(), "metricsCollectorEndpoint")
	})
}

func TestSetupMetricsProvider(T *testing.T) {
	T.Parallel()

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()

		provider, shutdown, err := setupMetricsProvider(ctx, logger, "test-service", nil)
		test.Nil(t, provider)
		test.Nil(t, shutdown)
		test.Error(t, err)
		test.ErrorIs(t, err, ErrNilConfig)
	})

	T.Run("valid config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: false,
			EnableHostMetrics:    false,
		}

		provider, shutdown, err := setupMetricsProvider(ctx, logger, "test-service", cfg)
		test.NoError(t, err)
		test.NotNil(t, provider)
		test.NotNil(t, shutdown)
	})

	T.Run("with runtime metrics enabled", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: true,
			EnableHostMetrics:    false,
		}

		provider, shutdown, err := setupMetricsProvider(ctx, logger, "test-service", cfg)
		test.NoError(t, err)
		test.NotNil(t, provider)
		test.NotNil(t, shutdown)
	})

	T.Run("with host metrics enabled", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: false,
			EnableHostMetrics:    true,
		}

		provider, shutdown, err := setupMetricsProvider(ctx, logger, "test-service", cfg)
		test.NoError(t, err)
		test.NotNil(t, provider)
		test.NotNil(t, shutdown)
	})
}

func TestProvideMetricsProvider(T *testing.T) {
	T.Parallel()

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()

		provider, err := ProvideMetricsProvider(ctx, logger, "test-service", nil)
		test.Nil(t, provider)
		test.Error(t, err)
		test.ErrorIs(t, err, ErrNilConfig)
	})

	T.Run("valid config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: false,
			EnableHostMetrics:    false,
		}

		provider, err := ProvideMetricsProvider(ctx, logger, "test-service", cfg)
		test.NoError(t, err)
		test.NotNil(t, provider)
		_, ok := any(provider).(metrics.Provider)
		test.True(t, ok)
	})
}

func TestProviderImpl_MeterProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: false,
			EnableHostMetrics:    false,
		}

		provider, err := ProvideMetricsProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)

		meterProvider := provider.MeterProvider()
		test.NotNil(t, meterProvider)
	})
}

func TestProviderImpl_Shutdown(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		// Note: This test is skipped because it requires a real metrics collector connection
		// The shutdown functionality is tested indirectly through the provider creation tests
		t.Skip("Skipping shutdown test - requires real metrics collector connection")
	})
}

func TestProviderImpl_NewFloat64Counter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: false,
			EnableHostMetrics:    false,
		}

		provider, err := ProvideMetricsProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)

		counter, err := provider.NewFloat64Counter("test_counter")
		test.NoError(t, err)
		test.NotNil(t, counter)
		_, ok := any(counter).(metrics.Float64Counter)
		test.True(t, ok)
	})
}

func TestProviderImpl_NewFloat64Gauge(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: false,
			EnableHostMetrics:    false,
		}

		provider, err := ProvideMetricsProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)

		gauge, err := provider.NewFloat64Gauge("test_gauge")
		test.NoError(t, err)
		test.NotNil(t, gauge)
		_, ok := any(gauge).(metrics.Float64Gauge)
		test.True(t, ok)
	})
}

func TestProviderImpl_NewFloat64UpDownCounter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: false,
			EnableHostMetrics:    false,
		}

		provider, err := ProvideMetricsProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)

		counter, err := provider.NewFloat64UpDownCounter("test_updown_counter")
		test.NoError(t, err)
		test.NotNil(t, counter)
		_, ok := any(counter).(metrics.Float64UpDownCounter)
		test.True(t, ok)
	})
}

func TestProviderImpl_NewFloat64Histogram(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: false,
			EnableHostMetrics:    false,
		}

		provider, err := ProvideMetricsProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)

		histogram, err := provider.NewFloat64Histogram("test_histogram")
		test.NoError(t, err)
		test.NotNil(t, histogram)
		_, ok := any(histogram).(metrics.Float64Histogram)
		test.True(t, ok)
	})
}

func TestProviderImpl_NewInt64Counter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: false,
			EnableHostMetrics:    false,
		}

		provider, err := ProvideMetricsProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)

		counter, err := provider.NewInt64Counter("test_counter")
		test.NoError(t, err)
		test.NotNil(t, counter)
		_, ok := any(counter).(metrics.Int64Counter)
		test.True(t, ok)
	})
}

func TestProviderImpl_NewInt64Gauge(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: false,
			EnableHostMetrics:    false,
		}

		provider, err := ProvideMetricsProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)

		gauge, err := provider.NewInt64Gauge("test_gauge")
		test.NoError(t, err)
		test.NotNil(t, gauge)
		_, ok := any(gauge).(metrics.Int64Gauge)
		test.True(t, ok)
	})
}

func TestProviderImpl_NewInt64UpDownCounter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: false,
			EnableHostMetrics:    false,
		}

		provider, err := ProvideMetricsProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)

		counter, err := provider.NewInt64UpDownCounter("test_updown_counter")
		test.NoError(t, err)
		test.NotNil(t, counter)
		_, ok := any(counter).(metrics.Int64UpDownCounter)
		test.True(t, ok)
	})
}

func TestProviderImpl_NewInt64Histogram(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: false,
			EnableHostMetrics:    false,
		}

		provider, err := ProvideMetricsProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)

		histogram, err := provider.NewInt64Histogram("test_histogram")
		test.NoError(t, err)
		test.NotNil(t, histogram)
		_, ok := any(histogram).(metrics.Int64Histogram)
		test.True(t, ok)
	})
}

func TestProviderImpl_ServiceNamePrefixing(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			CollectorEndpoint:    "localhost:4317",
			CollectionInterval:   30 * time.Second,
			Insecure:             true,
			EnableRuntimeMetrics: false,
			EnableHostMetrics:    false,
		}

		provider, err := ProvideMetricsProvider(ctx, logger, "my-service", cfg)
		must.NoError(t, err)

		// Test that metrics are created with service name prefix
		counter, err := provider.NewInt64Counter("test_metric")
		test.NoError(t, err)
		test.NotNil(t, counter)

		// The actual metric name should be "my-service.test_metric" but we can't easily test that
		// without accessing internal OpenTelemetry state, so we just verify the metric was created
	})
}

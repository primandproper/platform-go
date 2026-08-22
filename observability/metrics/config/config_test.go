package metricscfg

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics/otelgrpc"

	"github.com/shoenig/test"
)

func TestConfig_NewMetricsProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		metricsProvider, err := cfg.NewMetricsProvider(t.Context())

		test.NoError(t, err)
		test.NotNil(t, metricsProvider)
	})

	T.Run("enabled with otel provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:     true,
			Provider:    ProviderOtel,
			ServiceName: t.Name(),
			Otel: &otelgrpc.Config{
				CollectorEndpoint:  "localhost:4317",
				CollectionInterval: 30 * time.Second,
				Insecure:           true,
			},
		}

		metricsProvider, err := cfg.NewMetricsProvider(t.Context())

		test.NoError(t, err)
		test.NotNil(t, metricsProvider)
	})

	// A typo used to disable metrics in a way indistinguishable from choosing to.
	T.Run("enabled with unknown provider is an error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  true,
			Provider: "unknown",
		}

		metricsProvider, err := cfg.NewMetricsProvider(t.Context())

		test.ErrorIs(t, err, errors.ErrUnknownProvider)
		test.Nil(t, metricsProvider)
	})

	T.Run("enabled with the noop provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  true,
			Provider: ProviderNoop,
		}

		metricsProvider, err := cfg.NewMetricsProvider(t.Context())

		test.NoError(t, err)
		test.NotNil(t, metricsProvider)
	})

	T.Run("not enabled returns noop", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled: false,
		}

		metricsProvider, err := cfg.NewMetricsProvider(t.Context())

		test.NoError(t, err)
		test.NotNil(t, metricsProvider)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  true,
			Provider: ProviderOtel,
			Otel: &otelgrpc.Config{
				CollectorEndpoint:  t.Name(),
				CollectionInterval: 1,
			},
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("disabled is valid", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled: false,
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("enabled with invalid provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  true,
			Provider: "bogus",
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("enabled with otel provider but nil otel config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  true,
			Provider: ProviderOtel,
			Otel:     nil,
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewMetricsProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		metricsProvider, err := NewMetricsProvider(t.Context(), cfg)

		test.NoError(t, err)
		test.NotNil(t, metricsProvider)
	})
}

func TestConfig_NewMetricsProvider_disabled(T *testing.T) {
	T.Parallel()

	// Enabled=false used to short-circuit ahead of the provider switch, so a
	// typo'd name validated, built a noop, and only failed the day somebody
	// turned metrics on.
	T.Run("disabled with unknown provider is still an error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  false,
			Provider: "unknown",
		}

		metricsProvider, err := cfg.NewMetricsProvider(t.Context())

		test.ErrorIs(t, err, errors.ErrUnknownProvider)
		test.Nil(t, metricsProvider)
	})

	T.Run("disabled with unknown provider fails validation", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  false,
			Provider: "unknown",
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("disabled with a known provider is a noop", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  false,
			Provider: ProviderOtel,
		}

		metricsProvider, err := cfg.NewMetricsProvider(t.Context())

		test.NoError(t, err)
		test.NotNil(t, metricsProvider)
	})
}

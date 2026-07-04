package featureflagscfg

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	circuitbreakingcfg "github.com/primandproper/platform-go/v3/circuitbreaking/config"
	cbnoop "github.com/primandproper/platform-go/v3/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v3/featureflags/launchdarkly"
	"github.com/primandproper/platform-go/v3/featureflags/posthog"
	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v3/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v3/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v3/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			LaunchDarkly: &launchdarkly.Config{
				SDKKey:      t.Name(),
				InitTimeout: 123,
			},
			Provider: ProviderLaunchDarkly,
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with empty provider for noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "",
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with invalid provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "invalid_provider",
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with posthog provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			PostHog: &posthog.Config{
				ProjectAPIKey:  t.Name(),
				PersonalAPIKey: t.Name(),
			},
			Provider: ProviderPostHog,
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with launchdarkly provider missing config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderLaunchDarkly,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with posthog provider missing config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderPostHog,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestConfig_envTags(T *testing.T) {
	T.Parallel()

	T.Run("launchdarkly sub-config prefix separates cleanly from field names", func(t *testing.T) {
		t.Parallel()

		field, ok := reflect.TypeFor[Config]().FieldByName("LaunchDarkly")
		must.True(t, ok)
		// ",init" (not "init") is the option syntax that allocates the nil pointer sub-config.
		test.EqOp(t, ",init", field.Tag.Get("env"))
		// Trailing underscore keeps the prefix separated from nested field env names.
		test.EqOp(t, "LAUNCH_DARKLY_", field.Tag.Get("envPrefix"))

		sdkKey, ok := reflect.TypeFor[launchdarkly.Config]().FieldByName("SDKKey")
		must.True(t, ok)
		test.EqOp(t, "SDK_KEY", sdkKey.Tag.Get("env"))

		// Combined, the SDK key env var resolves to LAUNCH_DARKLY_SDK_KEY, not LAUNCH_DARKLYSDK_KEY.
		test.EqOp(t, "LAUNCH_DARKLY_SDK_KEY", field.Tag.Get("envPrefix")+sdkKey.Tag.Get("env"))
	})

	T.Run("pointer sub-configs use the init option", func(t *testing.T) {
		t.Parallel()

		for _, fieldName := range []string{"LaunchDarkly", "PostHog"} {
			field, ok := reflect.TypeFor[Config]().FieldByName(fieldName)
			must.True(t, ok)
			test.EqOp(t, ",init", field.Tag.Get("env"))
		}
	})
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()
	})
}

func TestConfig_ProvideFeatureFlagManager(T *testing.T) {
	T.Parallel()

	T.Run("with default/noop provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "",
		}

		ffm, err := cfg.ProvideFeatureFlagManager(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, http.DefaultClient, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, ffm)
	})

	T.Run("with unknown provider returns noop", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "something_unknown",
		}

		ffm, err := cfg.ProvideFeatureFlagManager(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, http.DefaultClient, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, ffm)
	})

	T.Run("with launchdarkly provider but nil config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderLaunchDarkly,
		}

		ffm, err := cfg.ProvideFeatureFlagManager(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, http.DefaultClient, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, ffm)
	})

	T.Run("with posthog provider but nil config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderPostHog,
		}

		ffm, err := cfg.ProvideFeatureFlagManager(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, http.DefaultClient, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, ffm)
	})

	T.Run("with provider string that has whitespace and mixed case", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "  LAUNCHDARKLY  ",
		}

		// Will fail because LaunchDarkly config is nil, but proves the normalization works
		ffm, err := cfg.ProvideFeatureFlagManager(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, http.DefaultClient, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, ffm)
	})
}

// TestProvideFeatureFlagManager is not parallel because it uses the circuit breaker subsystem
// which has a known race condition in the core library.
//
//nolint:paralleltest // see comment above
func TestProvideFeatureFlagManager(T *testing.T) {
	T.Run("with noop provider", func(t *testing.T) {
		ctx := t.Context()
		cfg := &Config{
			Provider: "",
		}

		ffm, err := ProvideFeatureFlagManager(ctx, cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), http.DefaultClient)
		must.NoError(t, err)
		must.NotNil(t, ffm)
	})

	T.Run("with circuit breaker error", func(t *testing.T) {
		ctx := t.Context()
		cbCfg := circuitbreakingcfg.Config{}
		cbCfg.EnsureDefaults()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, fmt.Sprintf("%s_circuit_breaker_tripped", cbCfg.Name), counterName)
				return &mockmetrics.Int64CounterMock{}, errors.New("arbitrary")
			},
		}

		cfg := &Config{
			Provider:       "",
			CircuitBreaker: cbCfg,
		}

		ffm, err := ProvideFeatureFlagManager(ctx, cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, http.DefaultClient)
		must.Error(t, err)
		must.Nil(t, ffm)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

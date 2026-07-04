package observability

import (
	"testing"

	loggingcfg "github.com/primandproper/platform-go/v3/observability/logging/config"
	tracingcfg "github.com/primandproper/platform-go/v3/observability/tracing/config"
	"github.com/primandproper/platform-go/v3/observability/tracing/oteltrace"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Logging: loggingcfg.Config{
				ServiceName: t.Name(),
			},
			Tracing: tracingcfg.Config{
				ServiceName:               t.Name(),
				SpanCollectionProbability: 1,
				Provider:                  tracingcfg.ProviderOtel,
				Otel: &oteltrace.Config{
					CollectorEndpoint: "0.0.0.0",
				},
			},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("propagates invalid sub-config error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		// A missing logging ServiceName is invalid; root validation must surface it
		// rather than silently passing (the pointer-receiver/value-field trap).
		cfg := &Config{
			Tracing: tracingcfg.Config{
				ServiceName:               t.Name(),
				SpanCollectionProbability: 1,
				Provider:                  tracingcfg.ProviderOtel,
				Otel: &oteltrace.Config{
					CollectorEndpoint: "0.0.0.0",
				},
			},
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestPillars_Shutdown(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{}

		pillars, err := cfg.ProvidePillars(ctx)
		must.NoError(t, err)
		must.NotNil(t, pillars)

		test.NoError(t, pillars.Shutdown(ctx))
	})

	T.Run("safe on empty Pillars", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, (&Pillars{}).Shutdown(t.Context()))
	})
}

func TestConfig_ProvidePillars(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{}

		pillars, err := cfg.ProvidePillars(ctx)
		must.NoError(t, err)
		must.NotNil(t, pillars)
		test.NotNil(t, pillars.Logger)
		test.NotNil(t, pillars.TracerProvider)
		test.NotNil(t, pillars.MetricsProvider)
		test.NotNil(t, pillars.Profiler)
	})
}

package observability

import (
	"testing"

	tracingcfg "github.com/primandproper/platform-go/observability/tracing/config"
	"github.com/primandproper/platform-go/observability/tracing/oteltrace"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
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

		test.NoError(t, cfg.ValidateWithContext(ctx))
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

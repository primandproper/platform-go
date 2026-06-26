package observability

import (
	"testing"

	loggingcfg "github.com/primandproper/platform-go/observability/logging/config"
	metricscfg "github.com/primandproper/platform-go/observability/metrics/config"
	profilingcfg "github.com/primandproper/platform-go/observability/profiling/config"
	tracingcfg "github.com/primandproper/platform-go/observability/tracing/config"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterO11yConfigs(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		i := do.New()
		do.ProvideValue(i, cfg)

		RegisterO11yConfigs(i)

		loggingConfig, err := do.Invoke[*loggingcfg.Config](i)
		must.NoError(t, err)
		test.NotNil(t, loggingConfig)

		metricsConfig, err := do.Invoke[*metricscfg.Config](i)
		must.NoError(t, err)
		test.NotNil(t, metricsConfig)

		tracingConfig, err := do.Invoke[*tracingcfg.Config](i)
		must.NoError(t, err)
		test.NotNil(t, tracingConfig)

		profilingConfig, err := do.Invoke[*profilingcfg.Config](i)
		must.NoError(t, err)
		test.NotNil(t, profilingConfig)
	})
}

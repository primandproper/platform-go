package featureflagscfg

import (
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v2/featureflags"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v2/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterFeatureFlagManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, metricsnoop.NewMetricsProvider())
		do.ProvideValue(i, http.DefaultClient)
		do.ProvideValue(i, &Config{})

		RegisterFeatureFlagManager(i)

		ffm, err := do.Invoke[featureflags.FeatureFlagManager](i)
		must.NoError(t, err)
		test.NotNil(t, ffm)
	})
}

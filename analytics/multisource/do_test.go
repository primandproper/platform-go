package multisource

import (
	"testing"

	analyticscfg "github.com/primandproper/platform-go/v6/analytics/config"
	"github.com/primandproper/platform-go/v6/analytics/segment"
	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	metricsnoop "github.com/primandproper/platform-go/v6/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterMultiSourceEventReporter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, metricsnoop.NewMetricsProvider())
		do.ProvideValue(i, map[string]*analyticscfg.SourceConfig{
			"ios": {
				Provider: analyticscfg.ProviderSegment,
				Segment:  &segment.Config{APIToken: t.Name()},
			},
		})

		RegisterMultiSourceEventReporter(i)

		reporter, err := do.Invoke[*MultiSourceEventReporter](i)
		must.NoError(t, err)
		test.NotNil(t, reporter)
	})
}

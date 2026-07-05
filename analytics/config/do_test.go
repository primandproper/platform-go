package analyticscfg

import (
	"testing"

	"github.com/primandproper/platform-go/v4/analytics"
	"github.com/primandproper/platform-go/v4/analytics/segment"
	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	metricsnoop "github.com/primandproper/platform-go/v4/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterEventReporter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, metricsnoop.NewMetricsProvider())
		do.ProvideValue(i, &Config{
			SourceConfig: SourceConfig{
				Provider: ProviderSegment,
				Segment:  &segment.Config{APIToken: t.Name()},
			},
		})

		RegisterEventReporter(i)

		reporter, err := do.Invoke[analytics.EventReporter](i)
		must.NoError(t, err)
		test.NotNil(t, reporter)
	})
}

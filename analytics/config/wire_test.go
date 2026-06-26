package analyticscfg

import (
	"testing"

	"github.com/primandproper/platform-go/analytics/segment"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/shoenig/test/must"
)

func TestProvideCollector(T *testing.T) {
	T.Parallel()

	T.Run("noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{}
		logger := loggingnoop.NewLogger()

		actual, err := ProvideEventReporter(ctx, cfg, logger, tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		must.NotNil(t, actual)
	})

	T.Run("with segment", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			SourceConfig: SourceConfig{
				Provider: ProviderSegment,
				Segment: &segment.Config{
					APIToken: t.Name(),
				},
			},
		}
		logger := loggingnoop.NewLogger()

		actual, err := ProvideEventReporter(ctx, cfg, logger, tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		must.NotNil(t, actual)
	})
}

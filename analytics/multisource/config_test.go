package multisource

import (
	"testing"

	analyticscfg "github.com/primandproper/platform-go/v4/analytics/config"
	"github.com/primandproper/platform-go/v4/analytics/posthog"
	"github.com/primandproper/platform-go/v4/analytics/segment"
	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v4/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestProvideMultiSourceEventReporter(T *testing.T) {
	T.Parallel()

	T.Run("with no proxy sources", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		reporter, err := ProvideMultiSourceEventReporter(ctx, nil, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		must.NotNil(t, reporter)
		test.MapEmpty(t, reporter.reporters)
	})

	T.Run("with valid segment source", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		sources := map[string]*analyticscfg.SourceConfig{
			"ios": {
				Provider: analyticscfg.ProviderSegment,
				Segment:  &segment.Config{APIToken: t.Name()},
			},
		}

		reporter, err := ProvideMultiSourceEventReporter(ctx, sources, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		must.NotNil(t, reporter)
		test.MapLen(t, 1, reporter.reporters)
	})

	T.Run("with invalid source falls back to noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		sources := map[string]*analyticscfg.SourceConfig{
			"ios": {
				Provider: analyticscfg.ProviderSegment,
				Segment:  &segment.Config{},
			},
		}

		reporter, err := ProvideMultiSourceEventReporter(ctx, sources, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		must.NotNil(t, reporter)
		test.MapLen(t, 1, reporter.reporters)
	})

	T.Run("with unrecognized provider uses noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		sources := map[string]*analyticscfg.SourceConfig{
			"web": {
				Provider: "bogus",
			},
		}

		reporter, err := ProvideMultiSourceEventReporter(ctx, sources, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		must.NotNil(t, reporter)
		test.MapLen(t, 1, reporter.reporters)
	})

	T.Run("with multiple posthog sources sharing an API key reuses one reporter", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		sources := map[string]*analyticscfg.SourceConfig{
			"ios": {
				Provider: analyticscfg.ProviderPostHog,
				Posthog:  &posthog.Config{APIKey: t.Name()},
			},
			"web": {
				Provider: analyticscfg.ProviderPostHog,
				Posthog:  &posthog.Config{APIKey: t.Name()},
			},
		}

		reporter, err := ProvideMultiSourceEventReporter(ctx, sources, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		must.NotNil(t, reporter)
		test.MapLen(t, 2, reporter.reporters)

		// Same API key -> the two sources share a single client instance.
		test.EqOp(t, reporter.reporters["ios"], reporter.reporters["web"])
	})

	T.Run("with posthog sources having distinct API keys creates distinct reporters", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		sources := map[string]*analyticscfg.SourceConfig{
			"ios": {
				Provider: analyticscfg.ProviderPostHog,
				Posthog:  &posthog.Config{APIKey: "ios-project-key"},
			},
			"web": {
				Provider: analyticscfg.ProviderPostHog,
				Posthog:  &posthog.Config{APIKey: "web-project-key"},
			},
		}

		reporter, err := ProvideMultiSourceEventReporter(ctx, sources, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		must.NotNil(t, reporter)
		test.MapLen(t, 2, reporter.reporters)

		// Distinct API keys -> each source gets its own client so credentials aren't discarded.
		test.NotEqOp(t, reporter.reporters["ios"], reporter.reporters["web"])
	})

	T.Run("with empty proxy sources map", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		sources := map[string]*analyticscfg.SourceConfig{}

		reporter, err := ProvideMultiSourceEventReporter(ctx, sources, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		must.NotNil(t, reporter)
		test.MapEmpty(t, reporter.reporters)
	})
}

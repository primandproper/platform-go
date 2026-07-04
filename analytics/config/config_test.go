package analyticscfg

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v3/analytics/posthog"
	"github.com/primandproper/platform-go/v3/analytics/segment"
	circuitbreakingcfg "github.com/primandproper/platform-go/v3/circuitbreaking/config"
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
			SourceConfig: SourceConfig{
				Provider: ProviderSegment,
				Segment:  &segment.Config{APIToken: t.Name()},
			},
		}

		must.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with invalid token", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			SourceConfig: SourceConfig{
				Provider: ProviderSegment,
			},
		}

		must.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("rejects an invalid proxy source", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			SourceConfig: SourceConfig{
				Provider: ProviderSegment,
				Segment:  &segment.Config{APIToken: t.Name()},
			},
			// A proxy source with no provider/credentials must fail validation rather
			// than silently degrading to a noop at runtime.
			ProxySources: ProxySourcesConfig{
				Web: &SourceConfig{Provider: ProviderSegment},
			},
		}

		must.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("accepts a valid proxy source", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			SourceConfig: SourceConfig{
				Provider: ProviderSegment,
				Segment:  &segment.Config{APIToken: t.Name()},
			},
			ProxySources: ProxySourcesConfig{
				Web: &SourceConfig{Provider: ProviderSegment, Segment: &segment.Config{APIToken: t.Name()}},
			},
		}

		must.NoError(t, cfg.ValidateWithContext(ctx))
	})
}

func TestConfig_ProvideCollector(T *testing.T) {
	T.Parallel()

	allProviders := []string{
		ProviderSegment,
		ProviderPostHog,
	}

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		for _, provider := range allProviders {
			cfg := &Config{
				SourceConfig: SourceConfig{
					Provider:       provider,
					Segment:        &segment.Config{APIToken: t.Name()},
					Posthog:        &posthog.Config{APIKey: t.Name()},
					CircuitBreaker: circuitbreakingcfg.Config{},
				},
			}

			_, err := cfg.ProvideCollector(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
			must.NoError(t, err)
		}
	})

	T.Run("with invalid values", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		for _, provider := range allProviders {
			cfg := &Config{
				SourceConfig: SourceConfig{
					Provider: provider,
					Segment:  &segment.Config{},
					Posthog:  &posthog.Config{},
				},
			}

			_, err := cfg.ProvideCollector(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
			must.Error(t, err)
		}
	})

	T.Run("with segment provider but nil segment config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			SourceConfig: SourceConfig{
				Provider: ProviderSegment,
			},
		}

		reporter, err := cfg.ProvideCollector(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		test.Nil(t, reporter)
		test.Error(t, err)
	})

	T.Run("with posthog provider but nil posthog config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			SourceConfig: SourceConfig{
				Provider: ProviderPostHog,
			},
		}

		reporter, err := cfg.ProvideCollector(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		test.Nil(t, reporter)
		test.Error(t, err)
	})

	T.Run("with unrecognized provider returns noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			SourceConfig: SourceConfig{
				Provider: "bogus",
			},
		}

		reporter, err := cfg.ProvideCollector(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		test.NotNil(t, reporter)
		test.NoError(t, err)
	})

	T.Run("with circuit breaker error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			SourceConfig: SourceConfig{
				Provider: ProviderSegment,
				Segment:  &segment.Config{APIToken: t.Name()},
				CircuitBreaker: circuitbreakingcfg.Config{
					Name:                   t.Name(),
					ErrorRate:              99,
					MinimumSampleThreshold: 1,
				},
			},
		}

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(_ string, options ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.SliceEmpty(t, options)
				return nil, errors.New("arbitrary")
			},
		}

		reporter, err := cfg.ProvideCollector(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp)
		test.Nil(t, reporter)
		test.Error(t, err)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

func TestSourceConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &SourceConfig{}
		cfg.EnsureDefaults()

		test.NotEq(t, "", cfg.CircuitBreaker.Name)
		test.NotEq(t, float64(0), cfg.CircuitBreaker.ErrorRate)
		test.NotEq(t, uint64(0), cfg.CircuitBreaker.MinimumSampleThreshold)
	})
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("with nil proxy sources", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.NotEq(t, "", cfg.CircuitBreaker.Name)
	})

	T.Run("with both proxy sources set", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			ProxySources: ProxySourcesConfig{
				IOS: &SourceConfig{},
				Web: &SourceConfig{},
			},
		}
		cfg.EnsureDefaults()

		test.NotEq(t, "", cfg.CircuitBreaker.Name)
		test.NotEq(t, "", cfg.ProxySources.IOS.CircuitBreaker.Name)
		test.NotEq(t, "", cfg.ProxySources.Web.CircuitBreaker.Name)
	})
}

func TestProxySourcesConfig_ToMap(T *testing.T) {
	T.Parallel()

	T.Run("with nil sources", func(t *testing.T) {
		t.Parallel()

		p := ProxySourcesConfig{}
		test.MapEmpty(t, p.ToMap())
	})

	T.Run("with only ios set", func(t *testing.T) {
		t.Parallel()

		ios := &SourceConfig{Provider: ProviderSegment}
		p := ProxySourcesConfig{IOS: ios}
		m := p.ToMap()

		test.MapLen(t, 1, m)
		test.EqOp(t, ios, m["ios"])
	})

	T.Run("with only web set", func(t *testing.T) {
		t.Parallel()

		web := &SourceConfig{Provider: ProviderPostHog}
		p := ProxySourcesConfig{Web: web}
		m := p.ToMap()

		test.MapLen(t, 1, m)
		test.EqOp(t, web, m["web"])
	})

	T.Run("with both sources set", func(t *testing.T) {
		t.Parallel()

		ios := &SourceConfig{Provider: ProviderSegment}
		web := &SourceConfig{Provider: ProviderPostHog}
		p := ProxySourcesConfig{IOS: ios, Web: web}
		m := p.ToMap()

		test.MapLen(t, 2, m)
		test.EqOp(t, ios, m["ios"])
		test.EqOp(t, web, m["web"])
	})
}

package analyticscfg

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v13/analytics/posthog"
	"github.com/primandproper/platform-go/v13/analytics/segment"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

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
				"web": {Provider: ProviderSegment},
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
				"web": {Provider: ProviderSegment, Segment: &segment.Config{APIToken: t.Name()}},
			},
		}

		must.NoError(t, cfg.ValidateWithContext(ctx))
	})
}

func TestConfig_NewCollector(T *testing.T) {
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

			_, err := cfg.NewCollector(ctx)
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

			_, err := cfg.NewCollector(ctx)
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

		reporter, err := cfg.NewCollector(ctx)
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

		reporter, err := cfg.NewCollector(ctx)
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

		// A typo'd provider is reported rather than quietly becoming a noop that
		// drops every event while looking configured.
		reporter, err := cfg.NewCollector(ctx)
		test.Nil(t, reporter)
		test.ErrorIs(t, err, platformerrors.ErrUnknownProvider)
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

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, options ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.SliceEmpty(t, options)
				return nil, errors.New("arbitrary")
			},
		}

		reporter, err := cfg.NewCollector(ctx, WithMetricsProvider(mp))
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
				"ios": {},
				"web": {},
			},
		}
		cfg.EnsureDefaults()

		test.NotEq(t, "", cfg.CircuitBreaker.Name)
		test.NotEq(t, "", cfg.ProxySources["ios"].CircuitBreaker.Name)
		test.NotEq(t, "", cfg.ProxySources["web"].CircuitBreaker.Name)
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
		p := ProxySourcesConfig{"ios": ios}
		m := p.ToMap()

		test.MapLen(t, 1, m)
		test.EqOp(t, ios, m["ios"])
	})

	T.Run("with only web set", func(t *testing.T) {
		t.Parallel()

		web := &SourceConfig{Provider: ProviderPostHog}
		p := ProxySourcesConfig{"web": web}
		m := p.ToMap()

		test.MapLen(t, 1, m)
		test.EqOp(t, web, m["web"])
	})

	T.Run("with both sources set", func(t *testing.T) {
		t.Parallel()

		ios := &SourceConfig{Provider: ProviderSegment}
		web := &SourceConfig{Provider: ProviderPostHog}
		p := ProxySourcesConfig{"ios": ios, "web": web}
		m := p.ToMap()

		test.MapLen(t, 2, m)
		test.EqOp(t, ios, m["ios"])
		test.EqOp(t, web, m["web"])
	})
}

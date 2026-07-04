package llmcfg

import (
	"errors"
	"reflect"
	"testing"

	"github.com/primandproper/platform-go/v3/llm/anthropic"
	"github.com/primandproper/platform-go/v3/llm/openai"
	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v3/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v3/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("openai provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderOpenAI,
			OpenAI: &openai.Config{
				APIKey: "test-key",
			},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("anthropic provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderAnthropic,
			Anthropic: &anthropic.Config{
				APIKey: "test-key",
			},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("empty provider is valid", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("unknown provider is invalid", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "nonsense",
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("openai provider missing config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderOpenAI,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("anthropic provider missing config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderAnthropic,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestConfig_envTags(T *testing.T) {
	T.Parallel()

	T.Run("pointer sub-configs use the init option so env populates them", func(t *testing.T) {
		t.Parallel()

		for _, fieldName := range []string{"OpenAI", "Anthropic"} {
			field, ok := reflect.TypeFor[Config]().FieldByName(fieldName)
			must.True(t, ok)
			// ",init" (not "init") allocates the nil pointer sub-config from env.
			test.EqOp(t, ",init", field.Tag.Get("env"))
		}
	})
}

func TestConfig_ProvideLLMProvider(T *testing.T) {
	T.Parallel()

	T.Run("empty provider falls back to noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{Provider: ""}

		provider, err := cfg.ProvideLLMProvider(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
		must.NoError(t, err)
		must.NotNil(t, provider)
	})

	T.Run("unknown provider falls back to noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{Provider: "unknown"}

		provider, err := cfg.ProvideLLMProvider(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
		must.NoError(t, err)
		must.NotNil(t, provider)
	})

	T.Run("openai provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderOpenAI,
			OpenAI: &openai.Config{
				APIKey: "test-key",
			},
		}

		provider, err := cfg.ProvideLLMProvider(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
		must.NoError(t, err)
		must.NotNil(t, provider)
	})

	T.Run("anthropic provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderAnthropic,
			Anthropic: &anthropic.Config{
				APIKey: "test-key",
			},
		}

		provider, err := cfg.ProvideLLMProvider(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
		must.NoError(t, err)
		must.NotNil(t, provider)
	})

	T.Run("openai provider with metrics error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderOpenAI,
			OpenAI: &openai.Config{
				APIKey: "test-key",
			},
		}

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
			},
		}

		provider, err := cfg.ProvideLLMProvider(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp)
		test.Nil(t, provider)
		test.Error(t, err)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("anthropic provider with metrics error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderAnthropic,
			Anthropic: &anthropic.Config{
				APIKey: "test-key",
			},
		}

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
			},
		}

		provider, err := cfg.ProvideLLMProvider(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp)
		test.Nil(t, provider)
		test.Error(t, err)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

func TestProvideLLMProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		provider, err := ProvideLLMProvider(t.Context(), cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
		must.NoError(t, err)
		test.NotNil(t, provider)
	})
}

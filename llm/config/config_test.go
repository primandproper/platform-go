package llmcfg

import (
	"errors"
	"reflect"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/llm/anthropic"
	"github.com/primandproper/platform-go/v13/llm/openai"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/metrics/metricstest"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

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

	T.Run("noop provider is valid", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{Provider: ProviderNoop}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("empty provider is invalid", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{}

		// Turning the LLM off has to be asked for by name; leaving the provider
		// unset is a mistake, not a way to say "no LLM".
		test.Error(t, cfg.ValidateWithContext(ctx))
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

func TestConfig_NewLLMProvider(T *testing.T) {
	T.Parallel()

	T.Run("empty provider falls back to noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{Provider: ProviderNoop}

		provider, err := cfg.NewLLMProvider(ctx, nil)
		must.NoError(t, err)
		must.NotNil(t, provider)
	})

	T.Run("unknown provider is reported rather than becoming a noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{Provider: "unknown"}

		provider, err := cfg.NewLLMProvider(ctx, nil)
		test.ErrorIs(t, err, platformerrors.ErrUnknownProvider)
		test.Nil(t, provider)
	})

	T.Run("empty provider is reported rather than becoming a noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{}

		provider, err := cfg.NewLLMProvider(ctx, nil)
		test.ErrorIs(t, err, platformerrors.ErrUnknownProvider)
		test.Nil(t, provider)
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

		provider, err := cfg.NewLLMProvider(ctx, nil)
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

		provider, err := cfg.NewLLMProvider(ctx, nil)
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

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		provider, err := cfg.NewLLMProvider(ctx, WithMetricsProvider(mp))
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

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		provider, err := cfg.NewLLMProvider(ctx, WithMetricsProvider(mp))
		test.Nil(t, provider)
		test.Error(t, err)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

func TestNewLLMProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderNoop}

		provider, err := NewLLMProvider(t.Context(), cfg, nil)
		must.NoError(t, err)
		test.NotNil(t, provider)
	})
}

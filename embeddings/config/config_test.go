package embeddingscfg

import (
	"reflect"
	"testing"

	"github.com/primandproper/platform-go/v13/embeddings/cohere"
	"github.com/primandproper/platform-go/v13/embeddings/ollama"
	"github.com/primandproper/platform-go/v13/embeddings/openai"
	"github.com/primandproper/platform-go/v13/errors"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_envTags(T *testing.T) {
	T.Parallel()

	T.Run("pointer sub-configs use the init option so env populates them", func(t *testing.T) {
		t.Parallel()

		for _, fieldName := range []string{"OpenAI", "Ollama", "Cohere"} {
			field, ok := reflect.TypeFor[Config]().FieldByName(fieldName)
			must.True(t, ok)
			// ",init" (not "init") allocates the nil pointer sub-config from env.
			test.EqOp(t, ",init", field.Tag.Get("env"))
		}
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("empty provider is valid", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ""}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with invalid provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "invalid"}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("openai provider with config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderOpenAI,
			OpenAI:   &openai.Config{APIKey: t.Name()},
		}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("openai provider requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderOpenAI}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("ollama provider with config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderOllama,
			Ollama:   &ollama.Config{},
		}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("ollama provider requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderOllama}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("cohere provider with config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderCohere,
			Cohere:   &cohere.Config{APIKey: t.Name()},
		}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("cohere provider requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderCohere}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestConfig_NewEmbedder_Empty(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ""}
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()

		embedder, err := cfg.NewEmbedder(t.Context(), WithLogger(logger), WithTracerProvider(tracerProvider), WithMetricsProvider(metricsProvider))
		must.NoError(t, err)
		must.NotNil(t, embedder, must.Sprintf("expected non-nil embedder (noop)"))
	})
}

func TestConfig_NewEmbedder_OpenAI(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderOpenAI,
			OpenAI: &openai.Config{
				APIKey: "test-key",
			},
		}
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()

		embedder, err := cfg.NewEmbedder(t.Context(), WithLogger(logger), WithTracerProvider(tracerProvider), WithMetricsProvider(metricsProvider))
		must.NoError(t, err)
		must.NotNil(t, embedder)
	})
}

func TestConfig_NewEmbedder_Ollama(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderOllama,
			Ollama:   &ollama.Config{},
		}
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()

		embedder, err := cfg.NewEmbedder(t.Context(), WithLogger(logger), WithTracerProvider(tracerProvider), WithMetricsProvider(metricsProvider))
		must.NoError(t, err)
		must.NotNil(t, embedder)
	})
}

func TestConfig_NewEmbedder_Cohere(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderCohere,
			Cohere: &cohere.Config{
				APIKey: "test-key",
			},
		}
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()

		embedder, err := cfg.NewEmbedder(t.Context(), WithLogger(logger), WithTracerProvider(tracerProvider), WithMetricsProvider(metricsProvider))
		must.NoError(t, err)
		must.NotNil(t, embedder)
	})
}

func TestConfig_NewEmbedder_Noop(T *testing.T) {
	T.Parallel()

	T.Run("named explicitly", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderNoop}

		embedder, err := cfg.NewEmbedder(t.Context())
		must.NoError(t, err)
		must.NotNil(t, embedder)
	})
}

func TestConfig_NewEmbedder_UnknownProvider(T *testing.T) {
	T.Parallel()

	T.Run("reports the provider rather than returning a silent noop", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "openai-but-misspelled"}

		embedder, err := cfg.NewEmbedder(t.Context())
		test.ErrorIs(t, err, errors.ErrUnknownProvider)
		test.Nil(t, embedder)
	})

	T.Run("nil config is an error, not a nil-pointer dereference", func(t *testing.T) {
		t.Parallel()

		embedder, err := NewEmbedder(t.Context(), nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
		test.Nil(t, embedder)
	})
}

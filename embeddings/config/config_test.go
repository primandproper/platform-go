package embeddingscfg

import (
	"testing"

	"github.com/primandproper/platform-go/embeddings/cohere"
	"github.com/primandproper/platform-go/embeddings/ollama"
	"github.com/primandproper/platform-go/embeddings/openai"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

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

func TestConfig_ProvideEmbedder_Empty(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ""}
		logger := loggingnoop.NewLogger()
		tracer := tracing.NewTracerForTest("test")

		embedder, err := cfg.ProvideEmbedder(t.Context(), logger, tracer)
		must.NoError(t, err)
		must.NotNil(t, embedder, must.Sprintf("expected non-nil embedder (noop)"))
	})
}

func TestConfig_ProvideEmbedder_OpenAI(T *testing.T) {
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
		tracer := tracing.NewTracerForTest("test")

		embedder, err := cfg.ProvideEmbedder(t.Context(), logger, tracer)
		must.NoError(t, err)
		must.NotNil(t, embedder)
	})
}

func TestConfig_ProvideEmbedder_Ollama(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderOllama,
			Ollama:   &ollama.Config{},
		}
		logger := loggingnoop.NewLogger()
		tracer := tracing.NewTracerForTest("test")

		embedder, err := cfg.ProvideEmbedder(t.Context(), logger, tracer)
		must.NoError(t, err)
		must.NotNil(t, embedder)
	})
}

func TestConfig_ProvideEmbedder_Cohere(T *testing.T) {
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
		tracer := tracing.NewTracerForTest("test")

		embedder, err := cfg.ProvideEmbedder(t.Context(), logger, tracer)
		must.NoError(t, err)
		must.NotNil(t, embedder)
	})
}

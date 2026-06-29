package embeddingscfg

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v2/embeddings"
	"github.com/primandproper/platform-go/v2/embeddings/cohere"
	embeddingsnoop "github.com/primandproper/platform-go/v2/embeddings/noop"
	"github.com/primandproper/platform-go/v2/embeddings/ollama"
	"github.com/primandproper/platform-go/v2/embeddings/openai"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/tracing"
)

// ProvideEmbedder provides an Embedder from config.
func ProvideEmbedder(ctx context.Context, c *Config, logger logging.Logger, tracer tracing.Tracer) (embeddings.Embedder, error) {
	switch strings.TrimSpace(strings.ToLower(c.Provider)) {
	case ProviderOpenAI:
		return openai.NewEmbedder(ctx, c.OpenAI, logger, tracer)
	case ProviderOllama:
		return ollama.NewEmbedder(ctx, c.Ollama, logger, tracer)
	case ProviderCohere:
		return cohere.NewEmbedder(ctx, c.Cohere, logger, tracer)
	default:
		return embeddingsnoop.NewEmbedder(), nil
	}
}

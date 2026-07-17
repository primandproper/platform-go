package embeddingscfg

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v5/embeddings"
	"github.com/primandproper/platform-go/v5/embeddings/cohere"
	embeddingsnoop "github.com/primandproper/platform-go/v5/embeddings/noop"
	"github.com/primandproper/platform-go/v5/embeddings/ollama"
	"github.com/primandproper/platform-go/v5/embeddings/openai"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/tracing"
)

// NewEmbedder provides an Embedder from config.
func NewEmbedder(ctx context.Context, c *Config, logger logging.Logger, tracer tracing.Tracer) (embeddings.Embedder, error) {
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

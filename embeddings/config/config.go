package embeddingscfg

import (
	"context"

	"github.com/primandproper/platform-go/v4/embeddings"
	"github.com/primandproper/platform-go/v4/embeddings/cohere"
	"github.com/primandproper/platform-go/v4/embeddings/ollama"
	"github.com/primandproper/platform-go/v4/embeddings/openai"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderOpenAI is the OpenAI provider.
	ProviderOpenAI = "openai"
	// ProviderOllama is the Ollama provider.
	ProviderOllama = "ollama"
	// ProviderCohere is the Cohere provider.
	ProviderCohere = "cohere"
)

// Config is the configuration for the embeddings provider.
type Config struct {
	OpenAI   *openai.Config `env:",init"    envPrefix:"OPENAI_" json:"openai"`
	Ollama   *ollama.Config `env:",init"    envPrefix:"OLLAMA_" json:"ollama"`
	Cohere   *cohere.Config `env:",init"    envPrefix:"COHERE_" json:"cohere"`
	Provider string         `env:"PROVIDER" json:"provider"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.Provider, validation.In(ProviderOpenAI, ProviderOllama, ProviderCohere, "")),
		validation.Field(&c.OpenAI, validation.When(c.Provider == ProviderOpenAI, validation.Required)),
		validation.Field(&c.Ollama, validation.When(c.Provider == ProviderOllama, validation.Required)),
		validation.Field(&c.Cohere, validation.When(c.Provider == ProviderCohere, validation.Required)),
	)
}

// ProvideEmbedder provides an Embedder based on config.
func (c *Config) ProvideEmbedder(ctx context.Context, logger logging.Logger, tracer tracing.Tracer) (embeddings.Embedder, error) {
	return ProvideEmbedder(ctx, c, logger, tracer)
}

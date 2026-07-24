package embeddingscfg

import (
	"context"

	"github.com/primandproper/platform-go/v6/embeddings"
	"github.com/primandproper/platform-go/v6/embeddings/cohere"
	"github.com/primandproper/platform-go/v6/embeddings/ollama"
	"github.com/primandproper/platform-go/v6/embeddings/openai"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/tracing"

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
	OpenAI   *openai.Config `env:",init"    envPrefix:"OPENAI_" json:"openai"   yaml:"openai"`
	Ollama   *ollama.Config `env:",init"    envPrefix:"OLLAMA_" json:"ollama"   yaml:"ollama"`
	Cohere   *cohere.Config `env:",init"    envPrefix:"COHERE_" json:"cohere"   yaml:"cohere"`
	Provider string         `env:"PROVIDER" json:"provider"     yaml:"provider"`
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

// NewEmbedder provides an Embedder based on config.
func (c *Config) NewEmbedder(ctx context.Context, logger logging.Logger, tracer tracing.Tracer) (embeddings.Embedder, error) {
	return NewEmbedder(ctx, c, logger, tracer)
}

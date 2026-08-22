// Package embeddingscfg selects and builds an embeddings.Embedder from
// configuration: OpenAI, Ollama, Cohere, or the noop embedder.
//
// Unlike most selection seams in this module, the empty provider is accepted
// here and selects the noop embedder. Embeddings are an optional capability
// rather than one a service silently loses, so a deployment that never mentions
// them is a configured deployment; the noop can also be named outright.
package embeddingscfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v13/embeddings"
	"github.com/primandproper/platform-go/v13/embeddings/cohere"
	"github.com/primandproper/platform-go/v13/embeddings/ollama"
	"github.com/primandproper/platform-go/v13/embeddings/openai"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderOpenAI is the OpenAI provider.
	ProviderOpenAI = "openai"
	// ProviderOllama is the Ollama provider.
	ProviderOllama = "ollama"
	// ProviderCohere is the Cohere provider.
	ProviderCohere = "cohere"
	// ProviderNoop names the embedder that returns no vectors. Embeddings are an
	// optional capability, so opting out is supported — but it has to be named,
	// or spelled as the empty provider.
	ProviderNoop = "noop"
)

// Config is the configuration for the embeddings provider.
type Config struct {
	OpenAI   *openai.Config `env:",init"    envPrefix:"OPENAI_"       json:"openai,omitempty"   yaml:"openai,omitempty"`
	Ollama   *ollama.Config `env:",init"    envPrefix:"OLLAMA_"       json:"ollama,omitempty"   yaml:"ollama,omitempty"`
	Cohere   *cohere.Config `env:",init"    envPrefix:"COHERE_"       json:"cohere,omitempty"   yaml:"cohere,omitempty"`
	Provider string         `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`
}

// providers are every provider this package implements, plus the empty string,
// which selects the noop embedder: embeddings are an optional capability, so
// leaving them unconfigured is a supported deployment. Validation and
// NewEmbedder both read it.
var providers = []string{"", ProviderOpenAI, ProviderOllama, ProviderCohere, ProviderNoop}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config.
//
// The sub-configs for providers that were not selected are skipped rather than
// merely unguarded: ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run, and `env:",init"` leaves every sub-config non-nil. A
// validation.When guard alone stops the Required rule and nothing else, so
// OpenAI's and Cohere's API keys were required at once and no config could load.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	provider := cfgnorm.Provider(c.Provider)

	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.Provider, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "OpenAI" and " cohere " while NewEmbedder built them.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "embeddings provider %q", c.Provider)
			}

			return nil
		})),
		validation.Field(&c.OpenAI, validation.Skip.When(provider != ProviderOpenAI), validation.Required),
		validation.Field(&c.Ollama, validation.Skip.When(provider != ProviderOllama), validation.Required),
		validation.Field(&c.Cohere, validation.Skip.When(provider != ProviderCohere), validation.Required),
	)
}

// NewEmbedder provides an Embedder based on config.
func (c *Config) NewEmbedder(
	ctx context.Context,
	opts ...Option,
) (embeddings.Embedder, error) {
	return NewEmbedder(ctx, c, opts...)
}

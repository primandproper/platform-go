package openai

import (
	"context"
	"time"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// Config configures the OpenAI embeddings provider.
type Config struct {
	APIKey       string        `env:"API_KEY"       json:"apiKey,omitempty"       yaml:"apiKey,omitempty"`
	BaseURL      string        `env:"BASE_URL"      json:"baseURL,omitempty"      yaml:"baseURL,omitempty"`
	DefaultModel string        `env:"DEFAULT_MODEL" json:"defaultModel,omitempty" yaml:"defaultModel,omitempty"`
	Timeout      time.Duration `env:"TIMEOUT"       json:"timeout"                yaml:"timeout"`
}

// ValidateWithContext validates the config.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.APIKey, validation.Required),
	)
}

package ably

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config holds Ably async notifier configuration.
type Config struct {
	APIKey string `env:"API_KEY" json:"apiKey" yaml:"apiKey"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.APIKey, validation.Required),
	)
}

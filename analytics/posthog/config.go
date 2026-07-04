package posthog

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

type Config struct {
	APIKey string `env:"API_KEY" json:"apiKey"`
	// Endpoint is the PostHog ingestion host. Leave empty for PostHog US Cloud
	// (the default); set it for EU Cloud (https://eu.posthog.com) or a self-hosted
	// instance.
	Endpoint string `env:"ENDPOINT" json:"endpoint"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.APIKey, validation.Required),
	)
}

package stripe

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

type (
	// Config configures our Stripe interface.
	Config struct {
		APIKey        string `env:"API_KEY"        json:"apiKey"        yaml:"apiKey"`
		WebhookSecret string `env:"WEBHOOK_SECRET" json:"webhookSecret" yaml:"webhookSecret"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.WebhookSecret, validation.Required),
	)
}

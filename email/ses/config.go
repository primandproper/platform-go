package ses

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures AWS SES to send email.
type Config struct {
	Region string `env:"REGION" json:"region" yaml:"region"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Region, validation.Required),
	)
}

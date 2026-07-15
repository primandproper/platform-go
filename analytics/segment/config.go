package segment

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

type Config struct {
	APIToken string `env:"API_TOKEN" json:"apiToken" yaml:"apiToken"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.APIToken, validation.Required),
	)
}

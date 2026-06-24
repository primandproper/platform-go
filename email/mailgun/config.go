package mailgun

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

type (
	// Config configures Mailgun to send email.
	Config struct {
		PrivateAPIKey string `env:"PRIVATE_API_KEY" json:"privateAPIKey"`
		Domain        string `env:"DOMAIN"          json:"domain"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (s *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, s,
		validation.Field(&s.PrivateAPIKey, validation.Required),
		validation.Field(&s.Domain, validation.Required),
	)
}

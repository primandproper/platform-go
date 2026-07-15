package oteltrace

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

type (
	// Config contains settings related to tracing.
	Config struct {
		_ struct{} `json:"-" yaml:"-"`

		CollectorEndpoint string `env:"COLLECTOR_ENDPOINT" json:"collector_endpoint,omitempty" yaml:"collector_endpoint,omitempty"`
		Insecure          bool   `env:"INSECURE"           json:"insecure,omitempty"           yaml:"insecure,omitempty"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config struct.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.CollectorEndpoint, validation.Required),
	)
}

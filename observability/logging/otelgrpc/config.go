package otelgrpc

import (
	"context"
	"time"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

type (
	Config struct {
		_ struct{} `json:"-"`

		CollectorEndpoint string        `env:"ENDPOINT_URL" json:"endpointURL"`
		Insecure          bool          `env:"INSECURE"     json:"insecure"`
		Timeout           time.Duration `env:"TIMEOUT"      json:"timeout"`
	}
)

func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.CollectorEndpoint, validation.Required),
	)
}

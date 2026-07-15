package otelgrpc

import (
	"context"
	"time"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

type (
	Config struct {
		_ struct{} `json:"-" yaml:"-"`

		CollectorEndpoint string        `env:"ENDPOINT_URL" json:"endpointURL" yaml:"endpointURL"`
		Insecure          bool          `env:"INSECURE"     json:"insecure"    yaml:"insecure"`
		Timeout           time.Duration `env:"TIMEOUT"      json:"timeout"     yaml:"timeout"`
	}
)

func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.CollectorEndpoint, validation.Required),
	)
}

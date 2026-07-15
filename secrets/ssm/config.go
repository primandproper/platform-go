package ssm

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures the AWS SSM Parameter Store client.
type Config struct {
	Region string `env:"REGION" json:"region"           yaml:"region"`
	Prefix string `env:"PREFIX" json:"prefix,omitempty" yaml:"prefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Region, validation.Required),
	)
}

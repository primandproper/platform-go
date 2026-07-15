package gcp

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures the GCP Secret Manager client.
type Config struct {
	ProjectID string `env:"PROJECT_ID" json:"projectID" yaml:"projectID"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.ProjectID, validation.Required),
	)
}

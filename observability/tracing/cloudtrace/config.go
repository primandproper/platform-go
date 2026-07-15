package cloudtrace

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

type (
	// Config contains settings related to tracing.
	Config struct {
		_ struct{} `json:"-" yaml:"-"`

		ProjectID string `env:"GOOGLE_CLOUD_TRACE_PROJECT_ID" json:"projectID,omitempty" yaml:"projectID,omitempty"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config struct.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.ProjectID, validation.Required),
	)
}

package objectstorage

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

type (
	// BackblazeB2Config configures a Backblaze B2-based objectstorage provider.
	BackblazeB2Config struct {
		_ struct{} `json:"-" yaml:"-"`

		ApplicationKeyID string `env:"APPLICATION_KEY_ID" json:"applicationKeyID" yaml:"applicationKeyID"`
		ApplicationKey   string `env:"APPLICATION_KEY"    json:"applicationKey"   yaml:"applicationKey"`
		Region           string `env:"REGION"             json:"region"           yaml:"region"`
	}
)

var _ validation.ValidatableWithContext = (*BackblazeB2Config)(nil)

// ValidateWithContext validates the BackblazeB2Config.
func (c *BackblazeB2Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.ApplicationKeyID, validation.Required),
		validation.Field(&c.ApplicationKey, validation.Required),
		validation.Field(&c.Region, validation.Required),
	)
}

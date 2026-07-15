package objectstorage

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

type (
	// R2Config configures a Cloudflare R2-based objectstorage provider.
	R2Config struct {
		_ struct{} `json:"-" yaml:"-"`

		AccountID       string `env:"ACCOUNT_ID"        json:"accountID"       yaml:"accountID"`
		AccessKeyID     string `env:"ACCESS_KEY_ID"     json:"accessKeyID"     yaml:"accessKeyID"`
		SecretAccessKey string `env:"SECRET_ACCESS_KEY" json:"secretAccessKey" yaml:"secretAccessKey"`
	}
)

var _ validation.ValidatableWithContext = (*R2Config)(nil)

// ValidateWithContext validates the R2Config.
func (c *R2Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.AccountID, validation.Required),
		validation.Field(&c.AccessKeyID, validation.Required),
		validation.Field(&c.SecretAccessKey, validation.Required),
	)
}

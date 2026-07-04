package objectstorage

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

type (
	// FilesystemConfig configures a filesystem-based objectstorage provider.
	FilesystemConfig struct {
		_ struct{} `json:"-"`

		RootDirectory string `env:"ROOT_DIRECTORY" json:"rootDirectory"`
	}
)

var _ validation.ValidatableWithContext = (*FilesystemConfig)(nil)

// ValidateWithContext validates the FilesystemConfig.
func (c *FilesystemConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.RootDirectory, validation.Required),
	)
}

package objectstorage

import (
	"context"
	"os"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// defaultDirectoryMode is the mode for directories the filesystem backend creates.
// 0700 (owner-only) is used instead of gocloud's 0777 default so other users on the
// host can't traverse into the upload directory and read stored objects.
const defaultDirectoryMode os.FileMode = 0o700

type (
	// FilesystemConfig configures a filesystem-based objectstorage provider.
	FilesystemConfig struct {
		_ struct{} `json:"-" yaml:"-"`

		RootDirectory string `env:"ROOT_DIRECTORY" json:"rootDirectory" yaml:"rootDirectory"`
		// DirectoryMode is the os.FileMode for directories the backend creates.
		// Defaults to 0700 when unset (zero).
		DirectoryMode os.FileMode `env:"DIRECTORY_MODE" json:"directoryMode" yaml:"directoryMode"`
	}
)

// directoryMode returns the configured directory mode, or the 0700 default.
func (c *FilesystemConfig) directoryMode() os.FileMode {
	if c.DirectoryMode == 0 {
		return defaultDirectoryMode
	}
	return c.DirectoryMode
}

var _ validation.ValidatableWithContext = (*FilesystemConfig)(nil)

// ValidateWithContext validates the FilesystemConfig.
func (c *FilesystemConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.RootDirectory, validation.Required),
	)
}

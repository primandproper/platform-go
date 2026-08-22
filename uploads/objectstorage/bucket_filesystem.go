package objectstorage

import (
	"context"
	"encoding"
	"os"
	"strconv"
	"strings"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// defaultDirectoryMode is the mode for directories the filesystem backend creates.
// 0700 (owner-only) is used instead of gocloud's 0777 default so other users on the
// host can't traverse into the upload directory and read stored objects.
const defaultDirectoryMode DirectoryMode = 0o700

// DirectoryMode is a Unix file mode that parses as octal from configuration.
//
// It exists because os.FileMode is a uint32, and every config decoder reads a
// bare integer in base 10 — so DIRECTORY_MODE=0700 parsed to decimal 700, which
// is 0o1274: the sticky bit plus a permission set nobody asked for. Every way
// anyone writes a Unix mode is octal, so that is what this parses.
type DirectoryMode os.FileMode

var (
	_ encoding.TextUnmarshaler = (*DirectoryMode)(nil)
	_ encoding.TextMarshaler   = DirectoryMode(0)
)

// UnmarshalText parses a mode in octal, with or without a leading "0" or "0o".
func (m *DirectoryMode) UnmarshalText(text []byte) error {
	raw := strings.TrimSpace(string(text))
	if raw == "" {
		return nil
	}

	// Base 8 explicitly rather than ParseUint's base-0 auto-detection: base 0
	// would read an unprefixed "700" as decimal, which is the bug this type
	// exists to prevent.
	parsed, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(raw, "0o"), "0O"), 8, 32)
	if err != nil {
		return platformerrors.Wrapf(err, "parsing directory mode %q as octal", raw)
	}

	*m = DirectoryMode(parsed)

	return nil
}

// MarshalText renders the mode as octal, so a round trip through config is
// stable.
func (m DirectoryMode) MarshalText() ([]byte, error) {
	return []byte("0o" + strconv.FormatUint(uint64(m), 8)), nil
}

// FileMode returns the mode as an os.FileMode.
func (m DirectoryMode) FileMode() os.FileMode {
	return os.FileMode(m)
}

type (
	// FilesystemConfig configures a filesystem-based objectstorage provider.
	FilesystemConfig struct {
		_ struct{} `json:"-" yaml:"-"`

		RootDirectory string `env:"ROOT_DIRECTORY" json:"rootDirectory,omitempty" yaml:"rootDirectory,omitempty"`
		// DirectoryMode is the mode for directories the backend creates, parsed as
		// octal. Defaults to 0700 when unset (zero).
		DirectoryMode DirectoryMode `env:"DIRECTORY_MODE" json:"directoryMode,omitempty" yaml:"directoryMode,omitempty"`
	}
)

// directoryMode returns the configured directory mode, or the 0700 default.
func (c *FilesystemConfig) directoryMode() os.FileMode {
	if c.DirectoryMode == 0 {
		return defaultDirectoryMode.FileMode()
	}

	return c.DirectoryMode.FileMode()
}

var _ validation.ValidatableWithContext = (*FilesystemConfig)(nil)

// ValidateWithContext validates the FilesystemConfig.
func (c *FilesystemConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.RootDirectory, validation.Required),
	)
}

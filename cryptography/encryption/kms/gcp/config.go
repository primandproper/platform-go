package gcp

import (
	"context"
	"strings"

	perrors "github.com/primandproper/platform-go/v13/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// keyNamePrefix is the leading segment of every Cloud KMS key resource name.
const keyNamePrefix = "projects/"

// Config configures the Cloud KMS key wrapper.
type Config struct {
	// KeyName is the full resource name of the crypto key that wraps, in the
	// form projects/P/locations/L/keyRings/R/cryptoKeys/K.
	//
	// It names the key, not a key version. Cloud KMS picks the primary version
	// for encryption and reads the version out of the ciphertext for
	// decryption, which is what lets the wrapping key rotate underneath
	// without this package participating.
	KeyName string `env:"KEY_NAME" json:"keyName,omitempty" yaml:"keyName,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.KeyName, validation.Required, validation.By(func(any) error {
			if !strings.HasPrefix(cfg.KeyName, keyNamePrefix) {
				return perrors.Errorf("cloud kms key name %q must be a full resource name beginning with %q", cfg.KeyName, keyNamePrefix)
			}

			return nil
		})),
	)
}

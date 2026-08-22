// Package uploadscfg carries the uploads configuration and hands its object
// storage half to a do injector.
//
// It selects nothing. The backend choice — S3, GCS, R2, Backblaze B2, the
// filesystem, memory — lives in objectstorage.Config, which this package wraps
// and validates rather than restates. What it adds is the envelope the
// environment is parsed into and RegisterStorageConfig, which extracts the inner
// config so consumers depend on that rather than on this one.
package uploadscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/uploads/objectstorage"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config contains settings for the uploads object storage.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	Storage objectstorage.Config `envPrefix:"STORAGE_" json:"storageConfig,omitzero" yaml:"storageConfig,omitempty"`
	Debug   bool                 `env:"DEBUG"          json:"debug,omitempty"        yaml:"debug,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Storage),
	)
}

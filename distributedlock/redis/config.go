package redis

import (
	"context"

	"github.com/primandproper/platform-go/v13/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures a Redis-backed distributed locker.
type Config struct {
	Username  string   `env:"USERNAME"   json:"username,omitempty"  yaml:"username,omitempty"`
	Password  string   `env:"PASSWORD"   json:"password,omitempty"  yaml:"password,omitempty"`
	KeyPrefix string   `env:"KEY_PREFIX" envDefault:"lock:"         json:"keyPrefix,omitempty" yaml:"keyPrefix,omitempty"`
	Addresses []string `env:"ADDRESSES"  json:"addresses,omitempty" yaml:"addresses,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if cfg == nil {
		return errors.ErrNilInputParameter
	}
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Addresses, validation.Required, validation.Length(1, 0)),
	)
}

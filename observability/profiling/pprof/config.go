package pprof

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// DefaultPort is the default port for the pprof HTTP server.
	DefaultPort = 6060
)

// Config holds pprof-specific profiling configuration.
type Config struct {
	Port               uint16 `env:"PORT"                 json:"port"               yaml:"port"`
	EnableMutexProfile bool   `env:"ENABLE_MUTEX_PROFILE" json:"enableMutexProfile" yaml:"enableMutexProfile"`
	EnableBlockProfile bool   `env:"ENABLE_BLOCK_PROFILE" json:"enableBlockProfile" yaml:"enableBlockProfile"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config struct.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.Port, validation.Min(uint16(1)), validation.Max(uint16(65535))),
	)
}

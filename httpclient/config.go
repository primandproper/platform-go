package httpclient

import (
	"context"
	"time"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	defaultTimeout             = 10 * time.Second
	defaultMaxIdleConns        = 100
	defaultMaxIdleConnsPerHost = 100
)

// Config configures an HTTP client.
type Config struct {
	Timeout             time.Duration `env:"TIMEOUT"                 json:"timeout"             yaml:"timeout"`
	MaxIdleConns        int           `env:"MAX_IDLE_CONNS"          json:"maxIdleConns"        yaml:"maxIdleConns"`
	MaxIdleConnsPerHost int           `env:"MAX_IDLE_CONNS_PER_HOST" json:"maxIdleConnsPerHost" yaml:"maxIdleConnsPerHost"`
	EnableTracing       bool          `env:"ENABLE_TRACING"          json:"enableTracing"       yaml:"enableTracing"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults sets default values for zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxIdleConns == 0 {
		cfg.MaxIdleConns = defaultMaxIdleConns
	}
	if cfg.MaxIdleConnsPerHost == 0 {
		cfg.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	}
}

// ValidateWithContext validates the config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Timeout, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.MaxIdleConns, validation.Required, validation.Min(1)),
		validation.Field(&cfg.MaxIdleConnsPerHost, validation.Required, validation.Min(1)),
	)
}

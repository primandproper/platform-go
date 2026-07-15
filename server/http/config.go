package http

import (
	"context"
	"time"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

type (
	// Config describes the settings pertinent to the HTTP serving portion of the service.
	Config struct {
		_ struct{} `json:"-" yaml:"-"`

		SSLCertificateFile    string        `env:"SSL_CERTIFICATE_FILEPATH"     json:"sslCertificate,omitempty"    yaml:"sslCertificate,omitempty"`
		SSLCertificateKeyFile string        `env:"SSL_CERTIFICATE_KEY_FILEPATH" json:"sslCertificateKey,omitempty" yaml:"sslCertificateKey,omitempty"`
		StartupDeadline       time.Duration `env:"STARTUP_DEADLINE"             json:"startupDeadline,omitempty"   yaml:"startupDeadline,omitempty"`
		ReadTimeout           time.Duration `env:"READ_TIMEOUT"                 json:"readTimeout,omitempty"       yaml:"readTimeout,omitempty"`
		WriteTimeout          time.Duration `env:"WRITE_TIMEOUT"                json:"writeTimeout,omitempty"      yaml:"writeTimeout,omitempty"`
		IdleTimeout           time.Duration `env:"IDLE_TIMEOUT"                 json:"idleTimeout,omitempty"       yaml:"idleTimeout,omitempty"`
		Port                  uint16        `env:"PORT"                         json:"port"                        yaml:"port"`
		Debug                 bool          `env:"DEBUG"                        json:"debug"                       yaml:"debug"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(
		ctx,
		cfg,
		validation.Field(&cfg.Port, validation.Required),
		validation.Field(&cfg.StartupDeadline, validation.Required),
	)
}

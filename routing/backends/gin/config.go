package gin

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures the gin (gin-gonic/gin) router backend. Its fields mirror
// the other backends so a service can switch backends without changing its
// configuration shape.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	ServiceName            string   `env:"SERVICE_NAME"              json:"serviceName,omitempty"         yaml:"serviceName,omitempty"`
	ValidDomains           []string `env:"VALID_DOMAINS"             json:"validDomains,omitempty"        yaml:"validDomains,omitempty"`
	EnableCORSForLocalhost bool     `env:"ENABLE_CORS_FOR_LOCALHOST" json:"enableCORSForLocalhost"        yaml:"enableCORSForLocalhost"`
	SilenceRouteLogging    bool     `env:"SILENCE_ROUTE_LOGGING"     json:"silenceRouteLogging,omitempty" yaml:"silenceRouteLogging,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a router config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.ServiceName, validation.Required),
	)
}

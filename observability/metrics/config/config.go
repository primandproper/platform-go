package metricscfg

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	metricsnoop "github.com/primandproper/platform-go/v5/observability/metrics/noop"
	"github.com/primandproper/platform-go/v5/observability/metrics/otelgrpc"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderOtel represents the open source tracing server.
	ProviderOtel = "otelgrpc"
)

type (
	// Config contains settings related to tracing.
	Config struct {
		_ struct{} `json:"-" yaml:"-"`

		Otel        *otelgrpc.Config `env:"init"         envPrefix:"OTEL_"         json:"otelgrpc,omitempty" yaml:"otelgrpc,omitempty"`
		ServiceName string           `env:"SERVICE_NAME" json:"serviceName"        yaml:"serviceName"`
		Provider    string           `env:"PROVIDER"     json:"provider,omitempty" yaml:"provider,omitempty"`
		Enabled     bool             `env:"ENABLED"      json:"enabled"            yaml:"enabled"`
	}
)

// NewMetricsProvider provides a metrics provider.
func (c *Config) NewMetricsProvider(ctx context.Context, logger logging.Logger) (metrics.Provider, error) {
	if !c.Enabled {
		return metricsnoop.NewMetricsProvider(), nil
	}

	switch strings.TrimSpace(strings.ToLower(c.Provider)) {
	case ProviderOtel:
		return otelgrpc.NewMetricsProvider(ctx, logger, c.ServiceName, c.Otel)
	default:
		return metricsnoop.NewMetricsProvider(), nil
	}
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config struct.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.Provider, validation.When(c.Enabled, validation.In("", ProviderOtel))),
		validation.Field(&c.Otel, validation.When(c.Enabled && c.Provider == ProviderOtel, validation.Required).Else(validation.Nil)),
	)
}

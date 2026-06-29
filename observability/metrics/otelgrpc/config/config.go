package config

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v2/observability/metrics/otelgrpc"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderOtel indicates you'd like to report metrics to OpenTelemetry.
	ProviderOtel = "otelgrpc"
)

type (
	// Config contains settings related to tracing.
	Config struct {
		_                         struct{}         `json:"-"`
		Otel                      *otelgrpc.Config `json:"otel"`
		Provider                  string           `json:"provider"`
		CollectorEndpoint         string           `json:"collector_endpoint,omitempty"`
		ServiceName               string           `json:"service_name,omitempty"`
		MetricsCollectionInterval time.Duration    `json:"metricsCollectionInterval,omitempty"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config struct.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.CollectorEndpoint, validation.Required),
		validation.Field(&c.ServiceName, validation.Required),
	)
}

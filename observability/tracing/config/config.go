package tracingcfg

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"
	"github.com/primandproper/platform-go/v4/observability/tracing/cloudtrace"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"
	"github.com/primandproper/platform-go/v4/observability/tracing/oteltrace"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderOtel represents the open source tracing server.
	ProviderOtel = "otelgrpc"
	// ProviderCloudTrace represents the GCP Cloud Trace service.
	ProviderCloudTrace = "cloudtrace"
)

type (
	// Config contains settings related to tracing.
	Config struct {
		_ struct{} `json:"-"`

		CloudTrace                *cloudtrace.Config `env:"init"                        envPrefix:"CLOUDTRACE_"                    json:"cloudTrace,omitempty"`
		Otel                      *oteltrace.Config  `env:"init"                        envPrefix:"OTELGRPC_"                      json:"otelgrpc,omitempty"`
		ServiceName               string             `env:"SERVICE_NAME"                json:"service_name,omitempty"`
		Provider                  string             `env:"PROVIDER"                    json:"provider,omitempty"`
		SpanCollectionProbability float64            `env:"SPAN_COLLECTION_PROBABILITY" json:"spanCollectionProbability,omitempty"`
	}
)

// NewTracerProvider provides a TracerProvider.
func (c *Config) NewTracerProvider(ctx context.Context, l logging.Logger) (tracing.TracerProvider, error) {
	logger := l.WithValue("tracing_provider", c.Provider)

	p := strings.TrimSpace(strings.ToLower(c.Provider))

	switch p {
	case ProviderOtel:
		logger.WithValue("otel", c.Otel).Info("configuring otelgrpc provider")
		tp, err := oteltrace.SetupOtelGRPC(ctx, c.ServiceName, c.SpanCollectionProbability, c.Otel)
		if err != nil {
			return nil, errors.Wrap(err, "configuring otelgrpc provider")
		}

		return tp, nil
	case ProviderCloudTrace:
		logger.Info("configuring cloud trace provider")
		tp, err := cloudtrace.SetupCloudTrace(ctx, c.ServiceName, c.SpanCollectionProbability, c.CloudTrace)
		if err != nil {
			return nil, errors.Wrap(err, "configuring cloud trace provider")
		}

		return tp, nil
	default:
		logger.Info("invalid tracing provider")
		return tracingnoop.NewTracerProvider(), nil
	}
}

// NewTracer provides an instrumentation handler.
func (c *Config) NewTracer(ctx context.Context, l logging.Logger, name string) (tracing.Tracer, error) {
	tp, err := c.NewTracerProvider(ctx, l)
	if err != nil {
		return nil, errors.Wrap(err, "configuring tracing provider")
	}

	return tracing.NewNamedTracer(tp, name), nil
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config struct.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.Provider, validation.In("", ProviderOtel, ProviderCloudTrace)),
		validation.Field(&c.Otel, validation.When(c.Provider == ProviderOtel, validation.Required).Else(validation.Nil)),
		validation.Field(&c.CloudTrace, validation.When(c.Provider == ProviderCloudTrace, validation.Required).Else(validation.Nil)),
		// ServiceName is only meaningful when a real provider is configured; requiring
		// it (and the probability) on the noop/default path is wrong. SpanCollectionProbability
		// is a 0–1 fraction, so a 0.0 ("sample nothing") is valid and must not be rejected
		// by Required.
		validation.Field(&c.ServiceName, validation.When(c.Provider != "", validation.Required)),
		validation.Field(&c.SpanCollectionProbability, validation.Min(0.0), validation.Max(1.0)),
	)
}

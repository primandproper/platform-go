// Package tracingcfg selects and builds a tracing.Provider from configuration:
// the OTel gRPC exporter, GCP Cloud Trace, or no tracing at all.
//
// One of the four pillar config packages, so it has no WithPillars option —
// observability imports it to build a Pillars.
//
// SpanCollectionProbability is the sampling rate every span in the process is
// decided by, and it lives here rather than on each component because a trace
// sampled at one hop and dropped at the next is not a trace. The empty provider
// and "noop" both select no tracing, which is the deliberate opt-out; an
// unrecognized name is an error rather than a third way to spell it.
package tracingcfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/observability/tracing/cloudtrace"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"
	"github.com/primandproper/platform-go/v13/observability/tracing/oteltrace"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderOtel represents the open source tracing server.
	ProviderOtel = "otelgrpc"
	// ProviderCloudTrace represents the GCP Cloud Trace service.
	ProviderCloudTrace = "cloudtrace"
	// ProviderNoop, and the empty string, select no tracing at all. That is the
	// deliberate opt-out and stays supported; what is no longer supported is a
	// provider name this package does not recognize, which used to disable
	// tracing silently and looked exactly like the opt-out.
	ProviderNoop = "noop"
)

type (
	// Config contains settings related to tracing.
	Config struct {
		_ struct{} `json:"-" yaml:"-"`

		CloudTrace                *cloudtrace.Config `env:",init"                       envPrefix:"CLOUDTRACE_"                    json:"cloudTrace,omitempty"                yaml:"cloudTrace,omitempty"`
		Otel                      *oteltrace.Config  `env:",init"                       envPrefix:"OTELGRPC_"                      json:"otelgrpc,omitempty"                  yaml:"otelgrpc,omitempty"`
		ServiceName               string             `env:"SERVICE_NAME"                json:"service_name,omitempty"              yaml:"service_name,omitempty"`
		Provider                  string             `env:"PROVIDER"                    json:"provider,omitempty"                  yaml:"provider,omitempty"`
		SpanCollectionProbability float64            `env:"SPAN_COLLECTION_PROBABILITY" json:"spanCollectionProbability,omitempty" yaml:"spanCollectionProbability,omitempty"`
	}
)

// providers are every provider this package implements, plus the empty string,
// which selects no tracing — the deliberate opt-out. Validation and
// NewTracerProvider both read it.
var providers = []string{"", ProviderNoop, ProviderOtel, ProviderCloudTrace}

// NewTracerProvider provides a TracerProvider.
func (c *Config) NewTracerProvider(ctx context.Context, opts ...Option) (tracing.Provider, error) {
	if c == nil {
		return nil, errors.ErrNilInputParameter
	}

	// EnsureLogger, not the raw option: the logger is optional now, and every
	// branch below logs what it configured.
	logger := logging.EnsureLogger(newOptions(opts).logger).WithValue("tracing_provider", c.Provider)

	p, err := cfgnorm.SelectProvider(c.Provider, providers, "tracing provider")
	if err != nil {
		return nil, err
	}

	// Validated here because the sub-config is what SetupOtelGRPC and
	// SetupCloudTrace dereference: naming otelgrpc with no otelgrpc block used
	// to reach a nil pointer read rather than a startup error.
	if err = c.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating tracing config")
	}

	switch p {
	case ProviderOtel:
		logger.WithValue("otel", c.Otel).Info("configuring otelgrpc provider")
		tp, setupErr := oteltrace.SetupOtelGRPC(ctx, c.ServiceName, c.SpanCollectionProbability, c.Otel)
		if setupErr != nil {
			return nil, errors.Wrap(setupErr, "configuring otelgrpc provider")
		}

		return tp, nil
	case ProviderCloudTrace:
		logger.Info("configuring cloud trace provider")
		tp, setupErr := cloudtrace.SetupCloudTrace(ctx, c.ServiceName, c.SpanCollectionProbability, c.CloudTrace)
		if setupErr != nil {
			return nil, errors.Wrap(setupErr, "configuring cloud trace provider")
		}

		return tp, nil
	case "", ProviderNoop:
		logger.Info("tracing disabled")
		return tracingnoop.NewTracerProvider(), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "tracing provider %q", c.Provider)
	}
}

// NewTracer provides an instrumentation handler.
func (c *Config) NewTracer(ctx context.Context, name string, opts ...Option) (tracing.Tracer, error) {
	tp, err := c.NewTracerProvider(ctx, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "configuring tracing provider")
	}

	return tracing.NewNamedTracer(tp, name), nil
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config struct.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	// Release the sub-configs env parsing's ",init" allocated and nothing filled
	// in, so the Nil rules below read "the operator configured this" rather than
	// "env parsing ran".
	cfgnorm.ZeroToNil(&c.Otel)
	cfgnorm.ZeroToNil(&c.CloudTrace)

	provider := cfgnorm.Provider(c.Provider)

	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.Provider, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "CloudTrace" while NewTracerProvider built it.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "tracing provider %q", c.Provider)
			}

			return nil
		})),
		validation.Field(&c.Otel, validation.When(provider == ProviderOtel, validation.Required).Else(validation.Nil)),
		validation.Field(&c.CloudTrace, validation.When(provider == ProviderCloudTrace, validation.Required).Else(validation.Nil)),
		// ServiceName is only meaningful when a real provider is configured; requiring
		// it (and the probability) on the noop/default path is wrong. SpanCollectionProbability
		// is a 0–1 fraction, so a 0.0 ("sample nothing") is valid and must not be rejected
		// by Required.
		validation.Field(&c.ServiceName, validation.When(provider != "" && provider != ProviderNoop, validation.Required)),
		validation.Field(&c.SpanCollectionProbability, validation.Min(0.0), validation.Max(1.0)),
	)
}

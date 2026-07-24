package observability

import (
	"context"

	"github.com/primandproper/platform-go/v6/errors"
	"github.com/primandproper/platform-go/v6/observability/logging"
	loggingcfg "github.com/primandproper/platform-go/v6/observability/logging/config"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	metricscfg "github.com/primandproper/platform-go/v6/observability/metrics/config"
	"github.com/primandproper/platform-go/v6/observability/profiling"
	profilingcfg "github.com/primandproper/platform-go/v6/observability/profiling/config"
	"github.com/primandproper/platform-go/v6/observability/tracing"
	tracingcfg "github.com/primandproper/platform-go/v6/observability/tracing/config"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/hashicorp/go-multierror"
)

type (
	// Config contains settings about how we report our metrics.
	Config struct {
		_         struct{}            `json:"-"`
		Profiling profilingcfg.Config `envPrefix:"PROFILING_" json:"profiling"`
		Logging   loggingcfg.Config   `envPrefix:"LOGGING_"   json:"logging"`
		Metrics   metricscfg.Config   `envPrefix:"METRICS_"   json:"metrics"`
		Tracing   tracingcfg.Config   `envPrefix:"TRACING_"   json:"tracing"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
//
// The sub-configs are struct values whose ValidateWithContext methods have
// pointer receivers. ozzo dereferences each field pointer to a value before
// checking for the ValidatableWithContext interface, so a bare
// validation.Field(&cfg.Logging) never invokes the sub-config's validation.
// Invoke each one explicitly instead.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Logging, validation.By(func(any) error { return cfg.Logging.ValidateWithContext(ctx) })),
		validation.Field(&cfg.Metrics, validation.By(func(any) error { return cfg.Metrics.ValidateWithContext(ctx) })),
		validation.Field(&cfg.Tracing, validation.By(func(any) error { return cfg.Tracing.ValidateWithContext(ctx) })),
		validation.Field(&cfg.Profiling, validation.By(func(any) error { return cfg.Profiling.ValidateWithContext(ctx) })),
	)
}

// Pillars holds the four observability pillars: logging, tracing, metrics, and profiling.
type Pillars struct {
	Logger          logging.Logger
	TracerProvider  tracing.TracerProvider
	MetricsProvider metrics.Provider
	Profiler        profiling.Provider
}

// NewPillars creates and returns all four observability pillars.
func (cfg *Config) NewPillars(ctx context.Context) (*Pillars, error) {
	logger, err := cfg.Logging.NewLogger(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "setting up logger")
	}

	tracerProvider, err := cfg.Tracing.NewTracerProvider(ctx, logger)
	if err != nil {
		return nil, errors.Wrap(err, "setting up tracer provider")
	}

	metricsProvider, err := cfg.Metrics.NewMetricsProvider(ctx, logger)
	if err != nil {
		return nil, errors.Wrap(err, "setting up metrics provider")
	}

	profiler, err := cfg.Profiling.NewProfilingProvider(ctx, logger)
	if err != nil {
		return nil, errors.Wrap(err, "setting up profiling provider")
	}

	return &Pillars{
		Logger:          logger,
		TracerProvider:  tracerProvider,
		MetricsProvider: metricsProvider,
		Profiler:        profiler,
	}, nil
}

// Shutdown gracefully stops the observability pillars, flushing any buffered
// telemetry so records are not dropped on exit. It is safe to call on a
// partially populated Pillars.
func (p *Pillars) Shutdown(ctx context.Context) error {
	errs := &multierror.Error{}

	if s, ok := p.Logger.(interface{ Shutdown(context.Context) error }); ok {
		if err := s.Shutdown(ctx); err != nil {
			errs = multierror.Append(errs, errors.Wrap(err, "shutting down logger"))
		}
	}

	if p.TracerProvider != nil {
		if err := p.TracerProvider.ForceFlush(ctx); err != nil {
			errs = multierror.Append(errs, errors.Wrap(err, "flushing tracer provider"))
		}
	}

	if p.MetricsProvider != nil {
		if err := p.MetricsProvider.Shutdown(ctx); err != nil {
			errs = multierror.Append(errs, errors.Wrap(err, "shutting down metrics provider"))
		}
	}

	if p.Profiler != nil {
		if err := p.Profiler.Shutdown(ctx); err != nil {
			errs = multierror.Append(errs, errors.Wrap(err, "shutting down profiler"))
		}
	}

	return errs.ErrorOrNil()
}

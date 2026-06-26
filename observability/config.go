package observability

import (
	"context"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability/logging"
	loggingcfg "github.com/primandproper/platform-go/observability/logging/config"
	"github.com/primandproper/platform-go/observability/metrics"
	metricscfg "github.com/primandproper/platform-go/observability/metrics/config"
	"github.com/primandproper/platform-go/observability/profiling"
	profilingcfg "github.com/primandproper/platform-go/observability/profiling/config"
	"github.com/primandproper/platform-go/observability/tracing"
	tracingcfg "github.com/primandproper/platform-go/observability/tracing/config"

	validation "github.com/go-ozzo/ozzo-validation/v4"
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
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Logging),
		validation.Field(&cfg.Metrics),
		validation.Field(&cfg.Tracing),
		validation.Field(&cfg.Profiling),
	)
}

// Pillars holds the four observability pillars: logging, tracing, metrics, and profiling.
type Pillars struct {
	Logger          logging.Logger
	TracerProvider  tracing.TracerProvider
	MetricsProvider metrics.Provider
	Profiler        profiling.Provider
}

// ProvidePillars creates and returns all four observability pillars.
func (cfg *Config) ProvidePillars(ctx context.Context) (*Pillars, error) {
	logger, err := cfg.Logging.ProvideLogger(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "setting up logger")
	}

	tracerProvider, err := cfg.Tracing.ProvideTracerProvider(ctx, logger)
	if err != nil {
		return nil, errors.Wrap(err, "setting up tracer provider")
	}

	metricsProvider, err := cfg.Metrics.ProvideMetricsProvider(ctx, logger)
	if err != nil {
		return nil, errors.Wrap(err, "setting up metrics provider")
	}

	profiler, err := cfg.Profiling.ProvideProfilingProvider(ctx, logger)
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

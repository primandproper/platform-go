// Package otelgrpc implements metrics.Provider against an OTLP collector reached
// over gRPC. It is the only non-noop metrics provider this module ships.
//
// Measurements are not pushed as they are taken. A periodic reader exports on
// cfg.CollectionInterval, so that interval is both the resolution a dashboard
// gets and the amount of measurement a process loses if it exits without
// Shutdown — which flushes, and which the DI container and
// observability.Pillars.Shutdown both call.
//
// Every instrument's name is prefixed with the service name at creation, so an
// instrument a package registers as "cache_hits" arrives as
// "<service>.cache_hits" and instruments of the same name from different
// services stay distinct.
//
// # What it costs to build
//
// Constructing a provider sets the OpenTelemetry global meter provider, which is
// process-wide: this belongs in a composition root, built once. Exemplars are
// enabled unconditionally, which is what links a metric back to the trace that
// produced it — and which means anything that samples traces changes what the
// exemplars point at.
//
// Runtime and host instrumentation are opt-in per config. Both start collectors
// that run for the life of the process rather than per request, and host metrics
// in particular describe the machine, not the service — on a shared node, several
// services reporting them describe the same node several times.
package otelgrpc

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	o11yutils "github.com/primandproper/platform-go/v13/observability/utils"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"go.opentelemetry.io/contrib/instrumentation/host"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/exemplar"
)

var (
	ErrNilConfig = errors.New("nil config")
)

type Config struct {
	CollectorEndpoint    string        `env:"COLLECTOR_ENDPOINT"     json:"metricsCollectorEndpoint,omitempty" yaml:"metricsCollectorEndpoint,omitempty"`
	CollectionInterval   time.Duration `env:"COLLECTION_INTERVAL"    json:"collectionInterval,omitempty"       yaml:"collectionInterval,omitempty"`
	Insecure             bool          `env:"INSECURE"               json:"insecure,omitempty"                 yaml:"insecure,omitempty"`
	EnableRuntimeMetrics bool          `env:"ENABLE_RUNTIME_METRICS" json:"enableRuntimeMetrics,omitempty"     yaml:"enableRuntimeMetrics,omitempty"`
	EnableHostMetrics    bool          `env:"ENABLE_HOST_METRICS"    json:"enableHostMetrics,omitempty"        yaml:"enableHostMetrics,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config struct.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.CollectorEndpoint, validation.Required),
		validation.Field(&c.CollectionInterval, validation.Required),
	)
}

func setupMetricsProvider(ctx context.Context, logger logging.Logger, serviceName string, cfg *Config) (metric.MeterProvider, func(context.Context) error, error) {
	if cfg == nil {
		return nil, nil, ErrNilConfig
	}

	res, err := o11yutils.OtelResource(ctx, serviceName)
	if err != nil {
		return nil, nil, err
	}

	options := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(cfg.CollectorEndpoint),
	}

	if cfg.Insecure {
		logger.Info("using insecure connection to metrics collector")
		options = append(options, otlpmetricgrpc.WithInsecure())
	}

	metricExp, err := otlpmetricgrpc.New(
		ctx,
		options...,
	)
	if err != nil {
		return nil, nil, errors.Wrap(err, "setting up metrics exporter")
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithExemplarFilter(exemplar.AlwaysOnFilter),
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(
				metricExp,
				sdkmetric.WithInterval(cfg.CollectionInterval),
			),
		),
	)

	// Registering the global provider is NewMetricsProvider's job, once, after
	// this returns. Doing it here as well meant every setup assigned the same
	// process-global twice.

	logger.WithValue("config", cfg).Info("set up meter provider")

	if cfg.EnableRuntimeMetrics {
		if err = runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
			return nil, nil, errors.Wrap(err, "starting runtime metrics")
		}
		logger.Info("started runtime metrics")
	}

	if cfg.EnableHostMetrics {
		if err = host.Start(host.WithMeterProvider(meterProvider)); err != nil {
			return nil, nil, errors.Wrap(err, "starting host metrics")
		}
		logger.Info("started host metrics")
	}

	return meterProvider, meterProvider.Shutdown, nil
}

func NewMetricsProvider(ctx context.Context, logger logging.Logger, serviceName string, cfg *Config) (*Provider, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	logger.WithValue("service.name", serviceName).
		WithValue("interval", cfg.CollectionInterval.String()).
		Info("setting up metrics provider")

	meterProvider, shutdown, err := setupMetricsProvider(ctx, logger, serviceName, cfg)
	if err != nil {
		return nil, errors.Wrap(err, "creating metric provider")
	}

	// Set the global meter provider
	otel.SetMeterProvider(meterProvider)

	i := &Provider{
		logger:        logging.EnsureLogger(logger),
		serviceName:   serviceName,
		meterProvider: meterProvider,
		mp:            meterProvider.Meter(serviceName),
		shutdownFunctions: []func(context.Context) error{
			shutdown,
		},
	}

	return i, nil
}

var _ metrics.Provider = (*Provider)(nil)

// Provider is the OTLP-over-gRPC metrics.Provider. It is exported, and returned
// by NewMetricsProvider, so a caller who has chosen the OTLP exporter can depend
// on that choice rather than on the seam every provider shares.
type Provider struct {
	mp                metric.Meter
	meterProvider     metric.MeterProvider
	logger            logging.Logger
	serviceName       string
	shutdownFunctions []func(context.Context) error
}

func (m *Provider) MeterProvider() metric.MeterProvider {
	return m.meterProvider
}

func (m *Provider) Shutdown(ctx context.Context) error {
	errs := make([]error, 0, len(m.shutdownFunctions))

	for _, fn := range m.shutdownFunctions {
		if err := fn(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// qualify prefixes an instrument name with the service name and records the
// creation at Debug.
//
// Each of these was an Info line. A process registers its instruments while
// starting up — around fifty of them across this repo's packages — so the first
// thing an operator saw in the log was fifty lines reporting that metrics
// plumbing had been plumbed, ahead of anything the service had actually done.
func (m *Provider) qualify(name, kind string) string {
	m.logger.WithValues(map[string]any{
		"instrument.name": name,
		"instrument.kind": kind,
	}).Debug("creating instrument")

	return fmt.Sprintf("%s.%s", m.serviceName, name)
}

func (m *Provider) NewFloat64Counter(name string, options ...metric.Float64CounterOption) (metrics.Float64Counter, error) {
	z, err := m.mp.Float64Counter(m.qualify(name, "float64_counter"), options...)
	if err != nil {
		return nil, errors.Wrap(err, "creating float64_counter instrument")
	}

	return &metrics.Float64CounterImpl{X: z}, nil
}

func (m *Provider) NewFloat64Gauge(name string, options ...metric.Float64GaugeOption) (metrics.Float64Gauge, error) {
	z, err := m.mp.Float64Gauge(m.qualify(name, "float64_gauge"), options...)
	if err != nil {
		return nil, errors.Wrap(err, "creating float64_gauge instrument")
	}

	return &metrics.Float64GaugeImpl{X: z}, nil
}

func (m *Provider) NewFloat64UpDownCounter(name string, options ...metric.Float64UpDownCounterOption) (metrics.Float64UpDownCounter, error) {
	z, err := m.mp.Float64UpDownCounter(m.qualify(name, "float64_up_down_counter"), options...)
	if err != nil {
		return nil, errors.Wrap(err, "creating float64_up_down_counter instrument")
	}

	return &metrics.Float64UpDownCounterImpl{X: z}, nil
}

func (m *Provider) NewFloat64Histogram(name string, options ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
	z, err := m.mp.Float64Histogram(m.qualify(name, "float64_histogram"), options...)
	if err != nil {
		return nil, errors.Wrap(err, "creating float64_histogram instrument")
	}

	return &metrics.Float64HistogramImpl{X: z}, nil
}

func (m *Provider) NewInt64Counter(name string, options ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
	z, err := m.mp.Int64Counter(m.qualify(name, "int64_counter"), options...)
	if err != nil {
		return nil, errors.Wrap(err, "creating int64_counter instrument")
	}

	return &metrics.Int64CounterImpl{X: z}, nil
}

func (m *Provider) NewInt64Gauge(name string, options ...metric.Int64GaugeOption) (metrics.Int64Gauge, error) {
	z, err := m.mp.Int64Gauge(m.qualify(name, "int64_gauge"), options...)
	if err != nil {
		return nil, errors.Wrap(err, "creating int64_gauge instrument")
	}

	return &metrics.Int64GaugeImpl{X: z}, nil
}

func (m *Provider) NewInt64UpDownCounter(name string, options ...metric.Int64UpDownCounterOption) (metrics.Int64UpDownCounter, error) {
	z, err := m.mp.Int64UpDownCounter(m.qualify(name, "int64_up_down_counter"), options...)
	if err != nil {
		return nil, errors.Wrap(err, "creating int64_up_down_counter instrument")
	}

	return &metrics.Int64UpDownCounterImpl{X: z}, nil
}

func (m *Provider) NewInt64Histogram(name string, options ...metric.Int64HistogramOption) (metrics.Int64Histogram, error) {
	z, err := m.mp.Int64Histogram(m.qualify(name, "int64_histogram"), options...)
	if err != nil {
		return nil, errors.Wrap(err, "creating int64_histogram instrument")
	}

	return &metrics.Int64HistogramImpl{X: z}, nil
}

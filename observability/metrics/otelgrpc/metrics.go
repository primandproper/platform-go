package otelgrpc

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v3/errors"
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	o11yutils "github.com/primandproper/platform-go/v3/observability/utils"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/hashicorp/go-multierror"
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
	CollectorEndpoint    string        `env:"COLLECTOR_ENDPOINT"     json:"metricsCollectorEndpoint"`
	CollectionInterval   time.Duration `env:"COLLECTION_INTERVAL"    json:"collectionInterval"`
	Insecure             bool          `env:"INSECURE"               json:"insecure"`
	EnableRuntimeMetrics bool          `env:"ENABLE_RUNTIME_METRICS" json:"enableRuntimeMetrics"`
	EnableHostMetrics    bool          `env:"ENABLE_HOST_METRICS"    json:"enableHostMetrics"`
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

	res := o11yutils.MustOtelResource(ctx, serviceName)

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
	otel.SetMeterProvider(meterProvider)

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

func ProvideMetricsProvider(ctx context.Context, logger logging.Logger, serviceName string, cfg *Config) (metrics.Provider, error) {
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

	i := &providerImpl{
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

var _ metrics.Provider = (*providerImpl)(nil)

type providerImpl struct {
	mp                metric.Meter
	meterProvider     metric.MeterProvider
	logger            logging.Logger
	serviceName       string
	shutdownFunctions []func(context.Context) error
}

func (m *providerImpl) MeterProvider() metric.MeterProvider {
	return m.meterProvider
}

func (m *providerImpl) Shutdown(ctx context.Context) error {
	errs := &multierror.Error{}

	for _, fn := range m.shutdownFunctions {
		if err := fn(ctx); err != nil {
			errs = multierror.Append(errs, err)
		}
	}

	return errs.ErrorOrNil()
}

func (m *providerImpl) NewFloat64Counter(name string, options ...metric.Float64CounterOption) (metrics.Float64Counter, error) {
	logger := m.logger.WithValue("name", name)
	logger.Info("NewFloat64Counter invoked")

	z, err := m.mp.Float64Counter(fmt.Sprintf("%s.%s", m.serviceName, name), options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Float64CounterImpl{X: z}, nil
}

func (m *providerImpl) NewFloat64Gauge(name string, options ...metric.Float64GaugeOption) (metrics.Float64Gauge, error) {
	logger := m.logger.WithValue("name", name)
	logger.Info("NewFloat64Gauge invoked")

	z, err := m.mp.Float64Gauge(fmt.Sprintf("%s.%s", m.serviceName, name), options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Float64GaugeImpl{X: z}, nil
}

func (m *providerImpl) NewFloat64UpDownCounter(name string, options ...metric.Float64UpDownCounterOption) (metrics.Float64UpDownCounter, error) {
	logger := m.logger.WithValue("name", name)
	logger.Info("NewFloat64UpDownCounter invoked")

	z, err := m.mp.Float64UpDownCounter(fmt.Sprintf("%s.%s", m.serviceName, name), options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Float64UpDownCounterImpl{X: z}, nil
}

func (m *providerImpl) NewFloat64Histogram(name string, options ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
	logger := m.logger.WithValue("name", name)
	logger.Info("NewFloat64Histogram invoked")

	z, err := m.mp.Float64Histogram(fmt.Sprintf("%s.%s", m.serviceName, name), options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Float64HistogramImpl{X: z}, nil
}

func (m *providerImpl) NewInt64Counter(name string, options ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
	logger := m.logger.WithValue("name", name)
	logger.Info("NewInt64Counter invoked")

	z, err := m.mp.Int64Counter(fmt.Sprintf("%s.%s", m.serviceName, name), options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Int64CounterImpl{X: z}, nil
}

func (m *providerImpl) NewInt64Gauge(name string, options ...metric.Int64GaugeOption) (metrics.Int64Gauge, error) {
	logger := m.logger.WithValue("name", name)
	logger.Info("NewInt64Gauge invoked")

	z, err := m.mp.Int64Gauge(fmt.Sprintf("%s.%s", m.serviceName, name), options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Int64GaugeImpl{X: z}, nil
}

func (m *providerImpl) NewInt64UpDownCounter(name string, options ...metric.Int64UpDownCounterOption) (metrics.Int64UpDownCounter, error) {
	logger := m.logger.WithValue("name", name)
	logger.Info("NewInt64UpDownCounter invoked")

	z, err := m.mp.Int64UpDownCounter(fmt.Sprintf("%s.%s", m.serviceName, name), options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Int64UpDownCounterImpl{X: z}, nil
}

func (m *providerImpl) NewInt64Histogram(name string, options ...metric.Int64HistogramOption) (metrics.Int64Histogram, error) {
	logger := m.logger.WithValue("name", name)
	logger.Info("NewInt64Histogram invoked")

	z, err := m.mp.Int64Histogram(fmt.Sprintf("%s.%s", m.serviceName, name), options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Int64HistogramImpl{X: z}, nil
}

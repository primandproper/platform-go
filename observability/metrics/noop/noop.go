package noop

import (
	"context"

	"github.com/primandproper/platform-go/v2/observability/metrics"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

var _ metrics.Provider = (*MetricsProvider)(nil)

// MetricsProvider is a no-op MetricsProvider.
type MetricsProvider struct{}

// NewMetricsProvider returns a no-op MetricsProvider.
func NewMetricsProvider() metrics.Provider {
	return &MetricsProvider{}
}

// NewFloat64Counter is a no-op.
func (*MetricsProvider) NewFloat64Counter(name string, options ...metric.Float64CounterOption) (metrics.Float64Counter, error) {
	y, err := otel.Meter("noop").Float64Counter(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Float64CounterImpl{X: y}, nil
}

// NewFloat64Gauge is a no-op.
func (*MetricsProvider) NewFloat64Gauge(name string, options ...metric.Float64GaugeOption) (metrics.Float64Gauge, error) {
	y, err := otel.Meter("noop").Float64Gauge(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Float64GaugeImpl{X: y}, nil
}

// NewFloat64UpDownCounter is a no-op.
func (*MetricsProvider) NewFloat64UpDownCounter(name string, options ...metric.Float64UpDownCounterOption) (metrics.Float64UpDownCounter, error) {
	y, err := otel.Meter("noop").Float64UpDownCounter(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Float64UpDownCounterImpl{X: y}, nil
}

// NewFloat64Histogram is a no-op.
func (*MetricsProvider) NewFloat64Histogram(name string, options ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
	y, err := otel.Meter("noop").Float64Histogram(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Float64HistogramImpl{X: y}, nil
}

// NewInt64Counter is a no-op.
func (*MetricsProvider) NewInt64Counter(name string, options ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
	y, err := otel.Meter("noop").Int64Counter(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Int64CounterImpl{X: y}, nil
}

// NewInt64Gauge is a no-op.
func (*MetricsProvider) NewInt64Gauge(name string, options ...metric.Int64GaugeOption) (metrics.Int64Gauge, error) {
	y, err := otel.Meter("noop").Int64Gauge(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Int64GaugeImpl{X: y}, nil
}

// NewInt64UpDownCounter is a no-op.
func (*MetricsProvider) NewInt64UpDownCounter(name string, options ...metric.Int64UpDownCounterOption) (metrics.Int64UpDownCounter, error) {
	y, err := otel.Meter("noop").Int64UpDownCounter(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Int64UpDownCounterImpl{X: y}, nil
}

// NewInt64Histogram is a no-op.
func (*MetricsProvider) NewInt64Histogram(name string, options ...metric.Int64HistogramOption) (metrics.Int64Histogram, error) {
	y, err := otel.Meter("noop").Int64Histogram(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Int64HistogramImpl{X: y}, nil
}

// MeterProvider returns the OTel noop MeterProvider.
func (*MetricsProvider) MeterProvider() metric.MeterProvider {
	return metricnoop.NewMeterProvider()
}

// Shutdown is a no-op.
func (*MetricsProvider) Shutdown(context.Context) error {
	return nil
}

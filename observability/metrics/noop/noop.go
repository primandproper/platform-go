// Package noop is the metrics.Provider that exports nothing. The instruments it
// builds are real objects with working Add and Record methods; every
// measurement passed to them is dropped, so a caller writes no branch around
// its instrumentation and pays an allocation per instrument for the privilege.
//
// Those instruments come from a locally constructed OTel noop MeterProvider
// rather than from otel.Meter, and that distinction is the whole of it. The
// process-global provider is a noop only until something installs a real one,
// at which point instruments built through it would begin recording and
// exporting — a provider named "noop" that quietly starts emitting metrics
// partway through a process's life. See noopMeter.
//
// It is what constructors resolve to through metrics.EnsureMetricsProvider when
// given none, and what observability/metrics/config builds for the "noop"
// provider name or the empty string.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/observability/metrics"

	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

var _ metrics.Provider = (*MetricsProvider)(nil)

// noopMeter is a genuinely no-op meter. Using otel.Meter (the process-global
// provider) here would make these "noop" instruments record and export real
// metrics once something installs a real global provider — the opposite of noop.
var noopMeter = metricnoop.NewMeterProvider().Meter("noop")

// MetricsProvider is a no-op MetricsProvider.
type MetricsProvider struct{}

// NewMetricsProvider returns a no-op MetricsProvider.
func NewMetricsProvider() metrics.Provider {
	return &MetricsProvider{}
}

// NewFloat64Counter is a no-op.
func (*MetricsProvider) NewFloat64Counter(name string, options ...metric.Float64CounterOption) (metrics.Float64Counter, error) {
	y, err := noopMeter.Float64Counter(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Float64CounterImpl{X: y}, nil
}

// NewFloat64Gauge is a no-op.
func (*MetricsProvider) NewFloat64Gauge(name string, options ...metric.Float64GaugeOption) (metrics.Float64Gauge, error) {
	y, err := noopMeter.Float64Gauge(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Float64GaugeImpl{X: y}, nil
}

// NewFloat64UpDownCounter is a no-op.
func (*MetricsProvider) NewFloat64UpDownCounter(name string, options ...metric.Float64UpDownCounterOption) (metrics.Float64UpDownCounter, error) {
	y, err := noopMeter.Float64UpDownCounter(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Float64UpDownCounterImpl{X: y}, nil
}

// NewFloat64Histogram is a no-op.
func (*MetricsProvider) NewFloat64Histogram(name string, options ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
	y, err := noopMeter.Float64Histogram(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Float64HistogramImpl{X: y}, nil
}

// NewInt64Counter is a no-op.
func (*MetricsProvider) NewInt64Counter(name string, options ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
	y, err := noopMeter.Int64Counter(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Int64CounterImpl{X: y}, nil
}

// NewInt64Gauge is a no-op.
func (*MetricsProvider) NewInt64Gauge(name string, options ...metric.Int64GaugeOption) (metrics.Int64Gauge, error) {
	y, err := noopMeter.Int64Gauge(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Int64GaugeImpl{X: y}, nil
}

// NewInt64UpDownCounter is a no-op.
func (*MetricsProvider) NewInt64UpDownCounter(name string, options ...metric.Int64UpDownCounterOption) (metrics.Int64UpDownCounter, error) {
	y, err := noopMeter.Int64UpDownCounter(name, options...)
	if err != nil {
		return nil, err
	}

	return &metrics.Int64UpDownCounterImpl{X: y}, nil
}

// NewInt64Histogram is a no-op.
func (*MetricsProvider) NewInt64Histogram(name string, options ...metric.Int64HistogramOption) (metrics.Int64Histogram, error) {
	y, err := noopMeter.Int64Histogram(name, options...)
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

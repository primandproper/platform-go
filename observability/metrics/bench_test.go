package metrics

import (
	"testing"

	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// newSDKMeter builds a real SDK meter backed by a manual reader (no exporter), so the
// benchmark measures the instrument record/aggregation path rather than any network I/O.
func newSDKMeter(b *testing.B) metric.Meter {
	b.Helper()

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))
	b.Cleanup(func() {
		must.NoError(b, mp.Shutdown(b.Context()))
	})

	return mp.Meter("bench")
}

func BenchmarkInt64Counter(b *testing.B) {
	ctx := b.Context()
	meter := newSDKMeter(b)

	raw, err := meter.Int64Counter("requests_total")
	must.NoError(b, err)

	counter := &Int64CounterImpl{X: raw}
	attrs := metric.WithAttributes(
		attribute.String("route", "/v1/things"),
		attribute.Int("status", 200),
	)

	b.Run("Add", func(b *testing.B) {
		for b.Loop() {
			counter.Add(ctx, 1)
		}
	})

	b.Run("AddWithAttributes", func(b *testing.B) {
		for b.Loop() {
			counter.Add(ctx, 1, attrs)
		}
	})
}

func BenchmarkFloat64Histogram(b *testing.B) {
	ctx := b.Context()
	meter := newSDKMeter(b)

	raw, err := meter.Float64Histogram("request_duration_seconds")
	must.NoError(b, err)

	histogram := &Float64HistogramImpl{X: raw}
	attrs := metric.WithAttributes(
		attribute.String("route", "/v1/things"),
		attribute.Int("status", 200),
	)

	b.Run("Record", func(b *testing.B) {
		for b.Loop() {
			histogram.Record(ctx, 0.042)
		}
	})

	b.Run("RecordWithAttributes", func(b *testing.B) {
		for b.Loop() {
			histogram.Record(ctx, 0.042, attrs)
		}
	})
}

// BenchmarkNoopProvider establishes a baseline: the cost of the metrics path when no
// real meter provider is configured.
func BenchmarkNoopProvider(b *testing.B) {
	ctx := b.Context()
	provider := EnsureMetricsProvider(nil)

	counter, err := provider.NewInt64Counter("requests_total")
	must.NoError(b, err)

	b.Run("Add", func(b *testing.B) {
		for b.Loop() {
			counter.Add(ctx, 1)
		}
	})
}

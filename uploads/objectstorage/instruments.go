package objectstorage

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The instrument names, and the attributes that tell one recording from another.
//
// These used to be seven instruments whose names embedded the bucket:
// fmt.Sprintf("%s_saves", cfg.BucketName+"_uploader") and six more like it. That
// made a config string part of metric identity, with two consequences. Two
// services writing to the same bucket reported into one series while one service
// writing to two buckets reported into two unrelated ones, neither of which a
// dashboard could aggregate. And a bucket name is not an instrument name:
// OpenTelemetry accepts [A-Za-z][A-Za-z0-9_./-]{0,254}, so a perfectly ordinary
// S3 bucket name beginning with a digit failed instrument construction, which
// this package reports as a startup error.
const (
	operationsCounterName = "uploads.operations"
	errorsCounterName     = "uploads.errors"
	latencyHistogramName  = "uploads.latency_ms"

	bucketAttrKey    = "uploads.bucket"
	operationAttrKey = "uploads.operation"
	reasonAttrKey    = "uploads.error_reason"

	// reasonBackend marks a failure the storage backend reported.
	reasonBackend = "backend"
	// reasonCircuitOpen marks an operation the circuit breaker refused to attempt.
	reasonCircuitOpen = "circuit_open"
)

// The operations, as they appear in the operation attribute.
const (
	opSave       = "save"
	opOpen       = "open"
	opOpenRange  = "open_range"
	opDelete     = "delete"
	opExists     = "exists"
	opAttributes = "attributes"
	opList       = "list"
	opSignedURL  = "signed_url"
)

// instruments is the one instrument set every Uploader method records through.
type instruments struct {
	operations metrics.Int64Counter
	errors     metrics.Int64Counter
	latency    metrics.Float64Histogram
	bucket     attribute.KeyValue
}

// newInstruments builds the instrument set for a bucket.
func newInstruments(metricsProvider metrics.Provider, bucketName string) (*instruments, error) {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	operations, err := mp.NewInt64Counter(operationsCounterName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating operations counter")
	}

	errCounter, err := mp.NewInt64Counter(errorsCounterName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating errors counter")
	}

	latency, err := mp.NewFloat64Histogram(latencyHistogramName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating latency histogram")
	}

	return &instruments{
		operations: operations,
		errors:     errCounter,
		latency:    latency,
		bucket:     attribute.String(bucketAttrKey, bucketName),
	}, nil
}

func (i *instruments) attrs(operation string) metric.MeasurementOption {
	return metric.WithAttributes(i.bucket, attribute.String(operationAttrKey, operation))
}

func (i *instruments) errAttrs(operation, reason string) metric.MeasurementOption {
	return metric.WithAttributes(i.bucket,
		attribute.String(operationAttrKey, operation),
		attribute.String(reasonAttrKey, reason),
	)
}

func millisSince(startedAt time.Time) float64 {
	return float64(time.Since(startedAt).Milliseconds())
}

// succeeded records a completed operation and how long it took.
//
// Latency carries the operation attribute, which the single shared histogram it
// replaces did not: a signed-URL mint and a multi-megabyte save landed in the
// same distribution, so the p99 belonged to no operation in particular.
func (i *instruments) succeeded(ctx context.Context, operation string, startedAt time.Time) {
	attrs := i.attrs(operation)
	i.latency.Record(ctx, millisSince(startedAt), attrs)
	i.operations.Add(ctx, 1, attrs)
}

// failed records an operation the backend rejected, and how long it took to do so.
func (i *instruments) failed(ctx context.Context, operation string, startedAt time.Time) {
	i.latency.Record(ctx, millisSince(startedAt), i.attrs(operation))
	i.errors.Add(ctx, 1, i.errAttrs(operation, reasonBackend))
}

// rejected records an operation the circuit breaker refused to attempt.
//
// There is no latency to record: nothing was attempted. What matters is that this
// counts at all — an open breaker used to return ErrCircuitBroken from all eight
// methods without touching an instrument or the span, so the one condition the
// breaker exists to signal was the one condition invisible to metrics and traces
// alike. A service failing every upload looked, on a dashboard, like a service
// doing no uploads.
func (i *instruments) rejected(ctx context.Context, operation string) {
	i.errors.Add(ctx, 1, i.errAttrs(operation, reasonCircuitOpen))
}

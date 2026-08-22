package stripe

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The instrument names for the Stripe money path, and the attribute that tells
// one operation from another.
//
// This package had spans and op.Error on every path and not one instrument, so
// the rate at which charges were failing was answerable only by counting error
// spans. metering wraps calls into this layer in panic recovery, on the grounds
// that a payment processor is not to be trusted with the caller's goroutine;
// that distrust deserved a number attached to it.
const (
	operationsCounterName = "capitalism.stripe.operations"
	errorsCounterName     = "capitalism.stripe.errors"
	latencyHistogramName  = "capitalism.stripe.latency_ms"

	operationAttrKey = "capitalism.operation"
)

// The operations, as they appear in the operation attribute.
const (
	opHandleWebhook       = "handle_event_webhook"
	opCreateCustomer      = "create_customer"
	opCreatePaymentIntent = "create_payment_intent"
	opCreateSubscription  = "create_subscription"
	opReportUsage         = "report_usage"
)

// instruments is the instrument set the Stripe operations record through.
type instruments struct {
	operations metrics.Int64Counter
	errors     metrics.Int64Counter
	latency    metrics.Float64Histogram
}

// newInstruments builds the Stripe instrument set.
func newInstruments(metricsProvider metrics.Provider) (*instruments, error) {
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

	return &instruments{operations: operations, errors: errCounter, latency: latency}, nil
}

func operationAttrs(operation string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(operationAttrKey, operation))
}

// record closes out one operation: latency either way, then the counter that
// matches the outcome.
//
// It is meant to be called from a closure deferred at the top of a method, over
// a named error return — deferred, so it reads that return after every path out
// of the method has assigned it, including the early input and configuration
// rejections.
func (i *instruments) record(ctx context.Context, operation string, startedAt time.Time, err error) {
	attrs := operationAttrs(operation)
	i.latency.Record(ctx, float64(time.Since(startedAt).Milliseconds()), attrs)

	if err != nil {
		i.errors.Add(ctx, 1, attrs)
		return
	}

	i.operations.Add(ctx, 1, attrs)
}

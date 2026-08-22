package textsearch

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The operations a text search backend performs, and the values the operation
// attribute takes. They are named here rather than per backend so a dashboard
// written against one backend reads the other.
const (
	OperationIndex  = "index"
	OperationDelete = "delete"
	OperationWipe   = "wipe"
	OperationSearch = "search"
)

// operationKey labels a measurement with which of the four it came from.
const operationKey = "search.operation"

// Instruments is what a text search backend records, and lives here rather than
// in each backend because the alternative is two copies of the same six
// registrations that drift the first time one of them gains a seventh.
//
// Three instruments, each meaning one thing, keyed by operation: how many calls
// there were, how many of them failed, and how long they took. A per-operation
// counter apiece would answer the same questions and make "the error rate of
// searches" a division across four names.
type Instruments struct {
	operations metrics.Int64Counter
	failures   metrics.Int64Counter
	latency    metrics.Float64Histogram

	index attribute.KeyValue
}

// NewInstruments builds the instruments for one backend's one index. A nil
// provider records nothing, which is how a caller asks for no metrics.
func NewInstruments(backend, indexName string, metricsProvider metrics.Provider) (*Instruments, error) {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	i := &Instruments{index: attribute.String(keys.IndexNameKey, indexName)}

	var err error
	if i.operations, err = mp.NewInt64Counter(backend + "_operations"); err != nil {
		return nil, platformerrors.Wrap(err, "creating text search operation counter")
	}

	if i.failures, err = mp.NewInt64Counter(backend + "_errors"); err != nil {
		return nil, platformerrors.Wrap(err, "creating text search error counter")
	}

	if i.latency, err = mp.NewFloat64Histogram(backend + "_latency_ms"); err != nil {
		return nil, platformerrors.Wrap(err, "creating text search latency histogram")
	}

	return i, nil
}

// Record notes one completed operation: that it happened, how long it took, and
// whether it failed.
//
// Every operation is counted, failed ones included, so the failure counter is a
// numerator over a denominator that is actually the same population — the ratio
// is the error rate, without a second subtraction.
func (i *Instruments) Record(ctx context.Context, operation string, started time.Time, err error) {
	attrs := metric.WithAttributes(i.index, attribute.String(operationKey, operation))

	i.operations.Add(ctx, 1, attrs)
	i.latency.Record(ctx, float64(time.Since(started).Milliseconds()), attrs)

	if err != nil {
		i.failures.Add(ctx, 1, attrs)
	}
}

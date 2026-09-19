package links

import (
	"context"
	"maps"
	"sync"
	"testing"

	"github.com/primandproper/primitives-go/v2/observability/metrics"
	metricsmock "github.com/primandproper/primitives-go/v2/observability/metrics/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// latencyRecorder captures what was recorded on links_latency_ms, and with
// which attributes.
//
// The instrument is documented as being read per operation, and an unlabeled
// histogram is indistinguishable from a labeled one at the call — the
// difference only shows up in a backend nobody has in a test. So the recording
// is what gets asserted, rather than that the five methods each call the
// helper.
type latencyRecorder struct {
	*metricsmock.ProviderMock

	operations map[string]int
	unlabeled  int
	mu         sync.Mutex
}

func newLatencyRecorder() *latencyRecorder {
	r := &latencyRecorder{operations: map[string]int{}}

	// Delegated to rather than reimplemented, so only the histogram under test
	// is a double.
	noop := metrics.EnsureMetricsProvider(nil)

	r.ProviderMock = &metricsmock.ProviderMock{
		NewInt64CounterFunc: noop.NewInt64Counter,
		NewFloat64HistogramFunc: func(
			name string,
			options ...metric.Float64HistogramOption,
		) (metrics.Float64Histogram, error) {
			if name != serviceName+"_latency_ms" {
				return noop.NewFloat64Histogram(name, options...)
			}

			return histogramFunc(r.record), nil
		},
	}

	return r
}

func (r *latencyRecorder) record(options []metric.RecordOption) {
	r.mu.Lock()
	defer r.mu.Unlock()

	set := metric.NewRecordConfig(options).Attributes()

	value, ok := set.Value(attribute.Key(operationKey))
	if !ok {
		r.unlabeled++

		return
	}

	r.operations[value.AsString()]++
}

// counts hands back a copy, so an assertion reads a stable map.
func (r *latencyRecorder) counts() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make(map[string]int, len(r.operations))
	maps.Copy(out, r.operations)

	return out
}

// histogramFunc adapts a function to metrics.Float64Histogram.
type histogramFunc func(options []metric.RecordOption)

func (f histogramFunc) Record(_ context.Context, _ float64, options ...metric.RecordOption) {
	f(options)
}

func TestMinter_LatencyIsRecordedPerOperation(T *testing.T) {
	T.Parallel()

	recorder := newLatencyRecorder()
	minter := newTestMinter(T, WithMetricsProvider(recorder))
	ctx := T.Context()

	// One of each, plus a second mint so that the link the revocation withdraws
	// is not the one the redemption spent.
	spent, err := minter.Mint(ctx, testAction, testSubject)
	must.NoError(T, err)

	_, err = minter.Inspect(ctx, spent.Token)
	must.NoError(T, err)

	_, err = minter.Redeem(ctx, spent.Token)
	must.NoError(T, err)

	withdrawn, err := minter.Mint(ctx, testAction, testSubject)
	must.NoError(T, err)

	must.NoError(T, minter.Revoke(ctx, withdrawn.ID))

	_, err = minter.RevokeForSubject(ctx, testSubject)
	must.NoError(T, err)

	test.Eq(T, map[string]int{
		operationMint:             2,
		operationInspect:          1,
		operationRedeem:           1,
		operationRevoke:           1,
		operationRevokeForSubject: 1,
	}, recorder.counts())

	// The label is the whole point of the change: a sample without one is a
	// sample that lands in the same bucket as every other operation.
	test.EqOp(T, 0, recorder.unlabeled)
}

func TestMinter_LatencyIsRecordedOnTheFailurePath(T *testing.T) {
	T.Parallel()

	// A refusal takes time too, and a histogram that only sees the successes
	// reports a latency the deployment does not have.
	recorder := newLatencyRecorder()
	minter := newTestMinter(T, WithMetricsProvider(recorder))

	_, err := minter.Redeem(T.Context(), "not a token anybody minted")
	must.ErrorIs(T, err, ErrLinkNotFound)

	test.Eq(T, map[string]int{operationRedeem: 1}, recorder.counts())
	test.EqOp(T, 0, recorder.unlabeled)
}

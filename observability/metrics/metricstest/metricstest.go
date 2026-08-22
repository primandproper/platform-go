/*
Package metricstest provides metric instruments for tests.

It is a separate package rather than a file in observability/metrics because
anything importing "testing" from a non-test file drags the testing package —
and, here, shoenig/test — into every production binary that transitively imports
it. Consumers reach for these from their own _test.go files, so the split costs
them nothing.
*/
package metricstest

import (
	"testing"

	"github.com/primandproper/platform-go/v13/observability/metrics"

	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// Int64Counter builds a counter from the process-global meter provider, failing
// the test if it cannot.
func Int64Counter(t *testing.T, name string) metric.Int64Counter {
	t.Helper()

	x, err := otel.Meter("testing").Int64Counter(name)
	must.NoError(t, err)

	return x
}

// Float64Histogram builds a histogram from the process-global meter provider,
// failing the test if it cannot.
func Float64Histogram(t *testing.T, name string) metric.Float64Histogram {
	t.Helper()

	x, err := otel.Meter("testing").Float64Histogram(name)
	must.NoError(t, err)

	return x
}

// OperationSet builds the request/error/latency trio under name, failing the
// test if it cannot.
//
// It exists because the components that hold a metrics.OperationSet are
// constructed by hand in their own tests — a struct literal with a stubbed
// transport beside the instruments — and three lines of instrument construction
// per literal is the same duplication the set was extracted to remove, moved
// into the test files.
func OperationSet(t *testing.T, name string) *metrics.OperationSet {
	t.Helper()

	set, err := metrics.NewOperationSet(nil, name)
	must.NoError(t, err)

	return set
}

package metrics

import (
	"context"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func TestNewOperationSet(T *testing.T) {
	T.Parallel()

	T.Run("names the trio after the component", func(t *testing.T) {
		t.Parallel()

		var counters, histograms []string

		mp := &providerRecorder{
			Provider:  EnsureMetricsProvider(nil),
			counter:   func(name string) { counters = append(counters, name) },
			histogram: func(name string) { histograms = append(histograms, name) },
		}

		set, err := NewOperationSet(mp, "cache")
		must.NoError(t, err)
		must.NotNil(t, set)

		// The suffixes are the contract: a dashboard charting errors over
		// requests across a deployment joins on exactly these.
		test.Eq(t, []string{"cache_requests", "cache_errors"}, counters)
		test.Eq(t, []string{"cache_latency_ms"}, histograms)
	})

	// A caller that was given no metrics provider gets a working set rather than
	// an error, so an unmetered component needs no branch.
	T.Run("a nil provider yields a recording-nothing set", func(t *testing.T) {
		t.Parallel()

		set, err := NewOperationSet(nil, "cache")
		must.NoError(t, err)
		must.NotNil(t, set)

		set.Attempt(t.Context())
		set.Failed(t.Context())
	})

	T.Run("reports a provider that could not build an instrument", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("meter unavailable")

		for _, failOn := range []string{"cache_requests", "cache_errors", "cache_latency_ms"} {
			mp := &providerRecorder{Provider: EnsureMetricsProvider(nil), failName: failOn, failErr: sentinel}

			set, err := NewOperationSet(mp, "cache")

			test.ErrorIs(t, err, sentinel, test.Sprintf("failing on %q", failOn))
			test.Nil(t, set)
		}
	})
}

func TestOperationSet_counting(T *testing.T) {
	T.Parallel()

	// The zero value has to be usable: a component built without a set must not
	// need a nil check around every measurement.
	T.Run("a zero or nil set records nothing rather than panicking", func(t *testing.T) {
		t.Parallel()

		var set OperationSet

		set.Attempt(t.Context())
		set.Failed(t.Context())

		var absent *OperationSet

		absent.Attempt(t.Context())
		absent.Failed(t.Context())
	})

	// Errors is a subset of Requests, not a series beside it — which is what
	// makes their ratio the error rate rather than a number that exceeds one.
	T.Run("a failure counts an error and not a second attempt", func(t *testing.T) {
		t.Parallel()

		requests, errs := &countingCounter{}, &countingCounter{}
		set := &OperationSet{Requests: requests, Errors: errs}

		set.Attempt(t.Context())
		set.Failed(t.Context())

		test.EqOp(t, int64(1), requests.total)
		test.EqOp(t, int64(1), errs.total)
	})
}

// providerRecorder is a Provider that records the names it was asked for and
// optionally refuses one of them. Everything it does not override is the noop
// provider's, which is why it embeds one.
type providerRecorder struct {
	Provider

	counter   func(string)
	histogram func(string)
	failErr   error
	failName  string
}

var _ Provider = (*providerRecorder)(nil)

func (p *providerRecorder) NewInt64Counter(name string, opts ...metric.Int64CounterOption) (Int64Counter, error) {
	if p.counter != nil {
		p.counter(name)
	}

	if name == p.failName {
		return nil, p.failErr
	}

	return p.Provider.NewInt64Counter(name, opts...)
}

func (p *providerRecorder) NewFloat64Histogram(name string, opts ...metric.Float64HistogramOption) (Float64Histogram, error) {
	if p.histogram != nil {
		p.histogram(name)
	}

	if name == p.failName {
		return nil, p.failErr
	}

	return p.Provider.NewFloat64Histogram(name, opts...)
}

// countingCounter is an Int64Counter that remembers its total.
type countingCounter struct {
	Int64Counter

	total int64
}

func (c *countingCounter) Add(_ context.Context, incr int64, _ ...metric.AddOption) {
	c.total += incr
}

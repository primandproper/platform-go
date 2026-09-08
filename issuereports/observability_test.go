package issuereports

import (
	"context"
	"sync"
	"testing"

	"github.com/primandproper/primitives-go/observability/metrics"
	metricsmock "github.com/primandproper/primitives-go/observability/metrics/mock"
	metricsnoop "github.com/primandproper/primitives-go/observability/metrics/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// recordingCounter is one instrument's worth of what the store added to it.
type recordingCounter struct {
	added []int64
	mu    sync.Mutex
}

var _ metrics.Int64Counter = (*recordingCounter)(nil)

func (c *recordingCounter) Add(_ context.Context, value int64, _ ...metric.AddOption) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.added = append(c.added, value)
}

func (c *recordingCounter) observed() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]int64(nil), c.added...)
}

// TestSQLStore_GuardMissCounter is the one number nothing above this layer can
// see.
//
// A guard miss is not a database error and is not a missing row: it is two
// people deciding the same report at once. From above the store it arrives as an
// occasional error somebody dismisses, so a client retrying a transition it
// already made looks exactly like a queue with two triagers working it.
func TestSQLStore_GuardMissCounter(T *testing.T) {
	T.Parallel()

	newCountingStore := func(t *testing.T, env *storeEnv) (*SQLStore, *recordingCounter) {
		t.Helper()

		counter := &recordingCounter{}
		base := metricsnoop.NewMetricsProvider()

		provider := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(name string, o ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				if name == storeName+"_guard_misses" {
					return counter, nil
				}

				return base.NewInt64Counter(name, o...)
			},
		}

		return env.newStore(t, WithStoreMetricsProvider(provider)), counter
	}

	T.Run("counts a transition that lost the race", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, counter := newCountingStore(t, env)

		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		_, err := env.transition(t, store, testScope, r.ID, StatusOpen, StatusResolved, "fixed")
		must.NoError(t, err)
		test.SliceEmpty(t, counter.observed())

		_, err = env.transition(t, store, testScope, r.ID, StatusOpen, StatusDeclined, "duplicate")
		must.ErrorIs(t, err, ErrStatusConflict)

		test.Eq(t, []int64{1}, counter.observed())
	})

	T.Run("counts nothing for a transition of a report that is not there", func(t *testing.T) {
		t.Parallel()

		// A report that is not there is not two people deciding it at once.
		// Counting it as contention would put an absent-row bug in the series a
		// dashboard reads as a busy queue.
		env := newSQLiteEnv(t)
		store, counter := newCountingStore(t, env)

		_, err := env.transition(t, store, testScope, "nonesuch",
			StatusOpen, StatusResolved, "fixed")
		must.ErrorIs(t, err, ErrReportNotFound)

		test.SliceEmpty(t, counter.observed())
	})

	T.Run("counts nothing for a move the lifecycle refused before the write", func(t *testing.T) {
		t.Parallel()

		// A refused move never reaches a statement, so it is not contention and
		// must not appear in the series a dashboard reads as contention.
		env := newSQLiteEnv(t)
		store, counter := newCountingStore(t, env)

		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		_, err := env.transition(t, store, testScope, r.ID,
			StatusResolved, StatusAcknowledged, "")
		must.ErrorIs(t, err, ErrInvalidStatusTransition)

		test.SliceEmpty(t, counter.observed())
	})

	T.Run("reports an instrument it could not build", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		provider := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return nil, errCounterUnavailable
			},
		}

		_, err := NewSQLStore(env.client, WithStoreMetricsProvider(provider))
		test.ErrorIs(t, err, errCounterUnavailable)
	})
}

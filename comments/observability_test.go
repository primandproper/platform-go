package comments

import (
	"context"
	"sync"
	"testing"

	"github.com/primandproper/primitives-go/v2/observability/metrics"
	metricsmock "github.com/primandproper/primitives-go/v2/observability/metrics/mock"
	metricsnoop "github.com/primandproper/primitives-go/v2/observability/metrics/noop"

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

// TestSQLStore_AbsentTargetCounter is the one number nothing above this layer
// can see.
//
// Each absent target arrives above the store as a single refused write that a
// handler turns into a 404 and somebody dismisses. In aggregate they are this
// package's central hazard arriving as a race rather than as a leftover row: a
// client working from a stale list, or a target being deleted underneath the
// form somebody is typing into.
func TestSQLStore_AbsentTargetCounter(T *testing.T) {
	T.Parallel()

	newCountingStore := func(t *testing.T, env *storeEnv, targets Targets) (*SQLStore, *recordingCounter) {
		t.Helper()

		counter := &recordingCounter{}
		base := metricsnoop.NewMetricsProvider()

		provider := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(name string, o ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				if name == storeName+"_absent_targets" {
					return counter, nil
				}

				return base.NewInt64Counter(name, o...)
			},
		}

		return env.newStore(t, WithTargets(targets), WithStoreMetricsProvider(provider)), counter
	}

	T.Run("counts a target the consumer's check could not find", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		check := newRecordingCheck(false, nil)
		store, counter := newCountingStore(t, env, Targets{
			recipeType: {Description: "a recipe", Exists: check.exists},
		})

		must.ErrorIs(t, env.createErr(t, store, testScope, newComment(testAuthor, "words")),
			ErrTargetNotFound)

		test.Eq(t, []int64{1}, counter.observed())
	})

	T.Run("counts nothing for a target that is there", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		check := newRecordingCheck(true, nil)
		store, counter := newCountingStore(t, env, Targets{
			recipeType: {Description: "a recipe", Exists: check.exists},
		})

		must.NoError(t, env.createErr(t, store, testScope, newComment(testAuthor, "words")))

		test.SliceEmpty(t, counter.observed())
	})

	T.Run("counts nothing for a target type nobody registered", func(t *testing.T) {
		t.Parallel()

		// A type outside the catalog is a misspelling or a deployment that does
		// not have that kind of thing — a build-time mistake arriving at runtime,
		// with nothing to watch. Counting it here would put it in the series an
		// operator reads as "things are being deleted underneath people".
		env := newSQLiteEnv(t)
		store, counter := newCountingStore(t, env, Targets{mealType: {Description: "a meal"}})

		must.ErrorIs(t, env.createErr(t, store, testScope, newComment(testAuthor, "words")),
			ErrUnknownTargetType)

		test.SliceEmpty(t, counter.observed())
	})

	T.Run("counts nothing for a check that failed", func(t *testing.T) {
		t.Parallel()

		// A hook that could not reach its table did not say the target was gone,
		// so it is an error rather than an absence, and it does not belong in the
		// absence series.
		env := newSQLiteEnv(t)
		check := newRecordingCheck(false, errCheckUnavailable)
		store, counter := newCountingStore(t, env, Targets{
			recipeType: {Description: "a recipe", Exists: check.exists},
		})

		must.ErrorIs(t, env.createErr(t, store, testScope, newComment(testAuthor, "words")),
			errCheckUnavailable)

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

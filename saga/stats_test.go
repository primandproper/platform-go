package saga

import (
	"context"
	"testing"
	"time"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/shoenig/test/wait"
)

// stuckDepthInstrument is the suffix the level is recorded under. Spelled once
// here so a test naming it and the worker building it cannot drift.
const stuckDepthInstrument = "_instances_stuck_depth"

// newStatsWorker builds a Worker whose measurements land in into, with a stats
// ticker the caller decides the cadence of.
//
// The registry is empty: nothing here advances a saga, and an instance already
// in StatusStuck is one no claim predicate in this package will return.
func (e *storeEnv) newStatsWorker(
	t *testing.T,
	store Store,
	interval time.Duration,
	into *recordingInstruments,
) *Worker {
	t.Helper()

	cfg := testWorkerConfig()
	cfg.StatsInterval = interval

	worker, err := NewWorker(t.Context(), cfg, e.client, store, NewRegistry(), newScopedLocker(t),
		WithWorkerClock(newStubClock()), WithWorkerMetricsProvider(into.provider()))
	must.NoError(t, err)

	return worker
}

func TestWorker_Stats(T *testing.T) {
	T.Parallel()

	T.Run("reports zero when nothing is stuck", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store := env.newStore(t)
		into := newRecordingInstruments()
		worker := env.newStatsWorker(t, store, time.Hour, into)

		stats, err := worker.Stats(t.Context())
		must.NoError(t, err)

		// Zero rather than nothing at all. The listing's counts ride on its
		// rows, so an empty page carries none — and a gauge that went quiet
		// instead of recording zero would leave the last non-zero reading
		// standing on the dashboard forever.
		test.EqOp(t, int64(0), stats.Stuck)
		test.Eq(t, []int64{0}, into.recorded(stuckDepthInstrument))
	})

	T.Run("counts every stuck instance", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store := env.newStore(t)
		into := newRecordingInstruments()
		worker := env.newStatsWorker(t, store, time.Hour, into)

		for _, id := range []string{"s1", "s2", "s3"} {
			env.stuckRecord(t, store, "orders", id)
		}

		stats, err := worker.Stats(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(3), stats.Stuck)
		test.Eq(t, []int64{3}, into.recorded(stuckDepthInstrument))
	})

	T.Run("counts only the stuck ones", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store := env.newStore(t)
		into := newRecordingInstruments()
		worker := env.newStatsWorker(t, store, time.Hour, into)

		env.stuckRecord(t, store, "orders", "s1")

		// One instance in each of the four statuses that are not a level: a
		// saga in motion and a saga that finished are both a saga nobody has to
		// be woken up for.
		for _, status := range []Status{StatusRunning, StatusCompensating, StatusCompleted, StatusCompensated} {
			inst := newRecord("not-"+string(status), "orders", []string{"one"}, testState{}, baseTime)
			inst.Status = status
			env.saveInstance(t, store, inst, baseTime)
		}

		stats, err := worker.Stats(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(1), stats.Stuck)
	})

	T.Run("falls back to zero once the last one is resumed", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store := env.newStore(t)
		into := newRecordingInstruments()
		worker := env.newStatsWorker(t, store, time.Hour, into)

		env.stuckRecord(t, store, "orders", "s1")

		stats, err := worker.Stats(t.Context())
		must.NoError(t, err)
		must.EqOp(t, int64(1), stats.Stuck)

		_, err = store.Requeue(t.Context(), "s1", []Status{StatusStuck}, StatusCompensating, baseTime)
		must.NoError(t, err)

		stats, err = worker.Stats(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(0), stats.Stuck)

		// The whole point of a level: the gauge follows the backlog down as
		// well as up, which the counter beside it cannot do.
		test.Eq(t, []int64{1, 0}, into.recorded(stuckDepthInstrument))
	})

	T.Run("surfaces the store's error and records nothing", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store := env.newStore(t)
		into := newRecordingInstruments()
		worker := env.newStatsWorker(t, &failingListStore{Store: store}, time.Hour, into)

		stats, err := worker.Stats(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "listing stuck saga instances")
		test.EqOp(t, int64(0), stats.Stuck)

		// A failed read must not record a zero. A gauge told the backlog
		// drained because the replica was unreachable is worse than one that
		// went stale, because staleness is something a monitoring system can
		// see.
		test.SliceEmpty(t, into.recorded(stuckDepthInstrument))
	})
}

func TestWorker_SampleStats(T *testing.T) {
	T.Parallel()

	T.Run("the run loop samples on its own ticker", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store := env.newStore(t)
		into := newRecordingInstruments()
		worker := env.newStatsWorker(t, store, time.Millisecond, into)

		env.stuckRecord(t, store, "orders", "s1")

		go worker.Run()
		t.Cleanup(func() { _ = worker.Close(context.Background()) })

		must.Wait(t, wait.InitialSuccess(
			wait.BoolFunc(func() bool {
				recorded := into.recorded(stuckDepthInstrument)

				return len(recorded) > 0 && recorded[0] == 1
			}),
			wait.Timeout(10*time.Second),
			wait.Gap(5*time.Millisecond),
		))
	})

	T.Run("a failed sample does not stop the loop", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store := env.newStore(t)
		into := newRecordingInstruments()
		worker := env.newStatsWorker(t, &failingListStore{Store: store}, time.Hour, into)

		// Does not panic, and returns nothing to a caller that has nowhere to
		// put it.
		worker.sampleStats(t.Context())

		test.SliceEmpty(t, into.recorded(stuckDepthInstrument))
	})
}

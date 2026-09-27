package series

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/distributedlock"
	"github.com/primandproper/primitives-go/v2/distributedlock/memory"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// fixedClock is the wall clock with Now pinned, which is all a pass reads.
type fixedClock struct {
	clock.WallClock

	now time.Time
}

func (c fixedClock) Now() time.Time { return c.now }

// newLockers is a raw in-memory locker and the scoped one a worker takes over it.
// The raw one is how a test holds the lock the way another replica would.
func newLockers(t *testing.T) (distributedlock.Locker, distributedlock.ScopedLocker) {
	t.Helper()

	raw, err := memory.NewLocker()
	must.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })

	scoped, err := distributedlock.NewScopedLocker(raw)
	must.NoError(t, err)

	return raw, scoped
}

func TestWorkerConfig(T *testing.T) {
	T.Parallel()

	T.Run("defaults are valid", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{}
		cfg.EnsureDefaults()

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
		test.EqOp(t, DefaultHorizon, cfg.Horizon)
		test.EqOp(t, time.Duration(0), cfg.Refill)
		test.EqOp(t, DefaultPollInterval, cfg.PollInterval)
		test.EqOp(t, DefaultBatchSize, cfg.BatchSize)
		test.EqOp(t, DefaultLockKey, cfg.LockKey)
	})

	T.Run("refill must be less than the horizon", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{Horizon: time.Hour, Refill: time.Hour}
		cfg.EnsureDefaults()

		test.Error(t, cfg.ValidateWithContext(t.Context()))

		cfg.Refill = time.Hour - time.Minute
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewWorker_Refusals(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)
	store := env.newStore(T)
	_, locker := newLockers(T)

	cases := map[string]struct {
		build func() (*Worker, error)
		want  error
	}{
		"nil config": {func() (*Worker, error) { return NewWorker(T.Context(), nil, env.client, store, locker) }, ErrNilConfig},
		"nil client": {func() (*Worker, error) { return NewWorker(T.Context(), &WorkerConfig{}, nil, store, locker) }, ErrNilDatabaseClient},
		"nil store":  {func() (*Worker, error) { return NewWorker(T.Context(), &WorkerConfig{}, env.client, nil, locker) }, ErrNilStore},
		"nil locker": {func() (*Worker, error) { return NewWorker(T.Context(), &WorkerConfig{}, env.client, store, nil) }, ErrNilLocker},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.build()
			test.ErrorIs(t, err, tc.want)
		})
	}
}

func TestWorker_RunOnce(T *testing.T) {
	T.Parallel()

	// Now is Monday the day before weeklyUTC's first Tuesday.
	now := tuesday(0).Add(-24 * time.Hour)

	T.Run("writes every tenant's series out to the horizon and no further", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)
		_, locker := newLockers(t)

		mine := env.mustCreate(t, store, testScope, weeklyUTC())
		theirs := env.mustCreate(t, store, otherScope, weeklyUTC())

		worker, err := NewWorker(t.Context(), &WorkerConfig{Horizon: 4 * 7 * 24 * time.Hour}, env.client, store, locker,
			WithWorkerClock(fixedClock{now: now}))
		must.NoError(t, err)

		written, err := worker.RunOnce(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(8), written)

		for _, s := range []struct {
			id    string
			scope tenancy.Scope
		}{{mine.ID, testScope}, {theirs.ID, otherScope}} {
			got := env.occurrences(t, store, s.scope, s.id)
			must.SliceLen(t, 4, got)
			test.EqOp(t, tuesday(3), got[3].ScheduledAt)
		}

		// A second pass at the same instant has nothing to do.
		written, err = worker.RunOnce(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), written)
	})

	T.Run("refill leaves a series alone until it falls that far behind", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)
		_, locker := newLockers(t)

		created := env.mustCreate(t, store, testScope, weeklyUTC())

		cfg := &WorkerConfig{Horizon: 4 * 7 * 24 * time.Hour, Refill: 2 * 7 * 24 * time.Hour}

		first, err := NewWorker(t.Context(), cfg, env.client, store, locker, WithWorkerClock(fixedClock{now: now}))
		must.NoError(t, err)

		written, err := first.RunOnce(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(4), written)

		// A week later the series is a week behind the horizon, inside the
		// refill, and is not touched.
		later, err := NewWorker(t.Context(), cfg, env.client, store, locker, WithWorkerClock(fixedClock{now: now.AddDate(0, 0, 7)}))
		must.NoError(t, err)

		written, err = later.RunOnce(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), written)

		// Two weeks later it is due, and is written out to the full horizon.
		muchLater, err := NewWorker(t.Context(), cfg, env.client, store, locker, WithWorkerClock(fixedClock{now: now.AddDate(0, 0, 14)}))
		must.NoError(t, err)

		written, err = muchLater.RunOnce(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(2), written)
		test.SliceLen(t, 6, env.occurrences(t, store, testScope, created.ID))
	})

	T.Run("a pass another replica is running is zero, not an error", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)
		raw, locker := newLockers(t)

		created := env.mustCreate(t, store, testScope, weeklyUTC())

		held, err := raw.Acquire(t.Context(), DefaultLockKey, time.Minute)
		must.NoError(t, err)
		t.Cleanup(func() { _ = held.Release(context.WithoutCancel(t.Context())) })

		worker, err := NewWorker(t.Context(), &WorkerConfig{}, env.client, store, locker, WithWorkerClock(fixedClock{now: now}))
		must.NoError(t, err)

		written, err := worker.RunOnce(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), written)
		test.SliceEmpty(t, env.occurrences(t, store, testScope, created.ID))
	})

	T.Run("one series failing does not stop the rest", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)
		_, locker := newLockers(t)

		broken := env.mustCreate(t, store, testScope, weeklyUTC())
		healthy := env.mustCreate(t, store, otherScope, weeklyUTC())

		failing := &failingStore{Store: store, failID: broken.ID}

		worker, err := NewWorker(t.Context(), &WorkerConfig{Horizon: 2 * 7 * 24 * time.Hour}, env.client, failing, locker,
			WithWorkerClock(fixedClock{now: now}))
		must.NoError(t, err)

		written, err := worker.RunOnce(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(2), written)
		test.SliceLen(t, 2, env.occurrences(t, store, otherScope, healthy.ID))
		test.SliceEmpty(t, env.occurrences(t, store, testScope, broken.ID))
	})
}

func TestWorker_RunAndClose(T *testing.T) {
	T.Parallel()

	T.Run("close on a worker never started returns at once", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		_, locker := newLockers(t)

		worker, err := NewWorker(t.Context(), &WorkerConfig{}, env.client, env.newStore(t), locker)
		must.NoError(t, err)

		test.NoError(t, worker.Close(t.Context()))
		test.NoError(t, worker.Close(t.Context()))
	})

	T.Run("close stops a running loop", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		_, locker := newLockers(t)

		worker, err := NewWorker(t.Context(), &WorkerConfig{PollInterval: time.Second}, env.client, env.newStore(t), locker)
		must.NoError(t, err)

		go worker.Run()

		for !worker.started.Load() {
			time.Sleep(time.Millisecond)
		}

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		test.NoError(t, worker.Close(ctx))
	})
}

// failingStore fails Materialize for one series and passes everything else
// through.
type failingStore struct {
	Store

	failID string
}

var errMaterialize = errors.New("materialize failed")

func (f *failingStore) Materialize(ctx context.Context, tx database.Tx, scope tenancy.Scope, seriesID string, through time.Time) (int64, error) {
	if seriesID == f.failID {
		return 0, errMaterialize
	}

	return f.Store.Materialize(ctx, tx, scope, seriesID, through)
}

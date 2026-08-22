package saga

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/retry"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The doubles below each break exactly one Store method, so the path under test
// is the only thing that is not the real implementation. Every one of these is
// a database that has stopped answering partway through a saga, which is the
// class of failure this package is most obliged to survive without losing track
// of what has already happened.

// failingSaveStore fails every insert.
type failingSaveStore struct {
	Store
}

func (s *failingSaveStore) Save(context.Context, database.SQLQueryExecutor, *Record, time.Time) error {
	return platformerrors.New("the write replica is unreachable")
}

// failingRequeueStore fails every requeue.
type failingRequeueStore struct {
	Store
}

func (s *failingRequeueStore) Requeue(context.Context, string, []Status, Status, time.Time) (*Record, error) {
	return nil, platformerrors.New("the write replica is unreachable")
}

func TestRunner_Degraded(T *testing.T) {
	T.Parallel()

	T.Run("reports a store that cannot insert", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		runner := newTestRunner(t, &failingSaveStore{Store: store}, registry)

		_, err := runner.Start(t.Context(), "orders", testState{})
		test.Error(t, err)
	})

	T.Run("StartInTransaction reports an unknown definition", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		runner := newTestRunner(t, store, NewRegistry())

		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			_, err := runner.StartInTransaction(t.Context(), q, "nope", testState{})
			test.ErrorIs(t, err, ErrUnknownDefinition)

			return nil
		}))
	})

	T.Run("reports a store that cannot requeue", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))

		inst := saveInstance(t, store, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)
		inst.Status = StatusStuck
		inst.ResumeStatus = StatusRunning
		inst.UpdatedAt = baseTime
		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.Advance(t.Context(), q, inst, baseTime)
		}))

		runner := newTestRunner(t, &failingRequeueStore{Store: store}, registry)

		_, err := runner.Resume(t.Context(), "i1")
		test.Error(t, err)
	})

	T.Run("reports a resumed instance whose state will not decode", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))

		inst := saveInstance(t, store, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)
		inst.Status = StatusStuck
		inst.ResumeStatus = StatusRunning
		inst.UpdatedAt = baseTime
		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.Advance(t.Context(), q, inst, baseTime)
		}))

		concrete, ok := store.(*SQLStore)
		must.True(t, ok)

		_, err := env.client.Writer().ExecContext(t.Context(),
			"UPDATE "+concrete.tables.instances+" SET state = 'not json' WHERE id = 'i1'")
		must.NoError(t, err)

		runner := newTestRunner(t, store, registry)

		_, err = runner.Resume(t.Context(), "i1")
		test.Error(t, err)
	})
}

func TestWorker_Degraded(T *testing.T) {
	T.Parallel()

	T.Run("a store that cannot record the compensation decision leaves the cursor alone", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", Step[testState]{
			Name: "fail",
			Do:   func(context.Context, *testState) error { return retry.Unretryable(platformerrors.New("no")) },
			Undo: func(context.Context, *testState) error { return nil },
		})

		worker := newWorker(t, &failingAdvanceStore{Store: store}, registry, newStubClock())

		startedRecord(t, store, registry, "orders", "i1")

		drainOnce(t, worker)

		// Still running at step zero: the step is retried, and its idempotency
		// key is what stops the retry doing the work twice.
		inst, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusRunning, inst.Status)
		test.EqOp(t, 0, inst.CurrentStep)
	})

	T.Run("a store that cannot record a completed compensation leaves the cursor alone", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))

		inst := saveInstance(t, store, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)
		inst.Status = StatusCompensating
		inst.CurrentStep = -1

		worker := newWorker(t, &failingAdvanceStore{Store: store}, registry, newStubClock())

		_, err := worker.step(t.Context(), mustLookup(t, registry, "orders"), inst)
		test.Error(t, err)
	})

	T.Run("a compensating instance already past its first step finishes", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		worker := newWorker(t, store, registry, newStubClock())

		inst := saveInstance(t, store, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)
		inst.Status = StatusCompensating
		inst.CurrentStep = -1

		more, err := worker.step(t.Context(), mustLookup(t, registry, "orders"), inst)
		must.NoError(t, err)
		test.False(t, more)

		got, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusCompensated, got.Status)
	})

	T.Run("a terminal instance is a no-op for step and for drive", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		worker := newWorker(t, store, registry, newStubClock())

		for _, status := range []Status{StatusCompleted, StatusCompensated, StatusStuck} {
			inst := newRecord("i1", "orders", []string{"one"}, testState{}, baseTime)
			inst.Status = status

			more, err := worker.step(t.Context(), mustLookup(t, registry, "orders"), inst)
			must.NoError(t, err, must.Sprintf("status %q", status))
			test.False(t, more)

			_, op := worker.o11y.Begin(t.Context())
			must.NoError(t, worker.drive(t.Context(), op, inst), must.Sprintf("status %q", status))
			op.End()
		}
	})
}

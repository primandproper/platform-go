package saga

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/pointer"
	"github.com/primandproper/platform-go/v13/retry"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newTestRunner builds a Runner over a fresh store and the given registry.
func newTestRunner(t *testing.T, store Store, registry *Registry, opts ...RunnerOption) Runner[testState] {
	t.Helper()

	runner, err := NewRunner[testState](store, registry,
		append([]RunnerOption{WithRunnerClock(newStubClock())}, opts...)...)
	must.NoError(t, err)

	return runner
}

func TestNewRunner(T *testing.T) {
	T.Parallel()

	T.Run("rejects missing dependencies", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		_, err := NewRunner[testState](nil, NewRegistry())
		test.ErrorIs(t, err, ErrNilStore)

		_, err = NewRunner[testState](store, nil)
		test.ErrorIs(t, err, ErrNilRegistry)
	})

	T.Run("ignores nil options", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		runner, err := NewRunner[testState](store, NewRegistry(),
			nil,
			WithRunnerClock(nil),
			WithRunnerEventPublisher(nil),
			WithRunnerLogger(nil),
			WithRunnerTracerProvider(nil),
			WithRunnerMetricsProvider(nil),
		)
		must.NoError(t, err)
		must.NotNil(t, runner)
	})
}

func TestRunner_Start(T *testing.T) {
	T.Parallel()

	T.Run("writes a running instance at step zero", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"), noopStep("two"))
		runner := newTestRunner(t, store, registry)

		inst, err := runner.Start(t.Context(), "orders", testState{Amount: 12})
		must.NoError(t, err)

		test.NotEq(t, "", inst.ID)
		test.EqOp(t, "orders", inst.Definition)
		test.EqOp(t, StatusRunning, inst.Status)
		test.EqOp(t, 0, inst.CurrentStep)
		test.EqOp(t, 12, inst.State.Amount)
		test.Eq(t, []string{"one", "two"}, inst.StepNames)
		test.EqOp(t, baseTime, inst.StartedAt)

		stored, err := store.Get(t.Context(), inst.ID)
		must.NoError(t, err)
		test.EqOp(t, inst.ID, stored.ID)
	})

	T.Run("reports an unknown definition", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		runner := newTestRunner(t, store, NewRegistry())

		_, err := runner.Start(t.Context(), "nope", testState{})
		test.ErrorIs(t, err, ErrUnknownDefinition)
	})

	T.Run("reports a state type that is not the definition's", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[otherState]{
			Name: "orders",
			Steps: []Step[otherState]{{
				Name: "one",
				Do:   func(context.Context, *otherState) error { return nil },
			}},
		}))

		runner := newTestRunner(t, store, registry)

		_, err := runner.Start(t.Context(), "orders", testState{})
		test.ErrorIs(t, err, ErrStateTypeMismatch)
	})

	T.Run("reports state that will not encode", func(t *testing.T) {
		t.Parallel()

		type unencodable struct {
			Fn func() `json:"fn"`
		}

		store := newSQLiteEnv(t).newStore(t)

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[unencodable]{
			Name: "orders",
			Steps: []Step[unencodable]{{
				Name: "one",
				Do:   func(context.Context, *unencodable) error { return nil },
			}},
		}))

		runner, err := NewRunner[unencodable](store, registry)
		must.NoError(t, err)

		_, err = runner.Start(t.Context(), "orders", unencodable{Fn: func() {}})
		test.Error(t, err)
	})

	T.Run("honors the first step's delay", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		delayed := noopStep("later")
		delayed.Delay = time.Hour

		registry := registryWith(t, "orders", delayed)
		runner := newTestRunner(t, store, registry)

		inst, err := runner.Start(t.Context(), "orders", testState{})
		must.NoError(t, err)

		early, err := store.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Minute))
		must.NoError(t, err)
		test.SliceEmpty(t, early)

		due, err := store.Claim(t.Context(), baseTime.Add(time.Hour), 10, baseTime.Add(2*time.Hour))
		must.NoError(t, err)
		must.SliceLen(t, 1, due)
		test.EqOp(t, inst.ID, due[0].ID)
	})

	T.Run("StartInTransaction commits with the caller's writes", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		runner := newTestRunner(t, store, registry)

		var id string

		// The caller's transaction rolls back, so the saga must not exist.
		err := store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			inst, startErr := runner.StartInTransaction(t.Context(), q, "orders", testState{})
			if startErr != nil {
				return startErr
			}

			id = inst.ID

			return platformerrors.New("the caller changed its mind")
		})
		test.Error(t, err)
		must.NotEq(t, "", id)

		_, err = store.Get(t.Context(), id)
		test.ErrorIs(t, err, ErrInstanceNotFound)
	})

	T.Run("StartInTransaction refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		runner := newTestRunner(t, store, registry)

		_, err := runner.StartInTransaction(t.Context(), nil, "orders", testState{})
		test.ErrorIs(t, err, ErrNilExecutor)
	})

	T.Run("publishes a started event in the same transaction", func(t *testing.T) {
		t.Parallel()

		var seen []Event

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		runner := newTestRunner(t, store, registry, WithRunnerEventPublisher(
			EventPublisherFunc(func(_ context.Context, _ database.SQLQueryExecutor, events ...Event) error {
				seen = append(seen, events...)

				return nil
			}),
		))

		inst, err := runner.Start(t.Context(), "orders", testState{})
		must.NoError(t, err)

		must.SliceLen(t, 1, seen)
		test.EqOp(t, EventStarted, seen[0].Type)
		test.EqOp(t, inst.ID, seen[0].InstanceID)
		test.EqOp(t, "one", seen[0].Step)
		test.EqOp(t, StatusRunning, seen[0].Status)
	})

	T.Run("a failing publisher fails the start", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		runner := newTestRunner(t, store, registry, WithRunnerEventPublisher(
			EventPublisherFunc(func(context.Context, database.SQLQueryExecutor, ...Event) error {
				return platformerrors.New("the outbox table is missing")
			}),
		))

		_, err := runner.Start(t.Context(), "orders", testState{})
		test.Error(t, err)
	})
}

func TestRunner_Get(T *testing.T) {
	T.Parallel()

	T.Run("decodes the state", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		runner := newTestRunner(t, store, registry)

		started, err := runner.Start(t.Context(), "orders", testState{Amount: 5, Trail: []string{"x"}})
		must.NoError(t, err)

		got, err := runner.Get(t.Context(), started.ID)
		must.NoError(t, err)
		test.EqOp(t, 5, got.State.Amount)
		test.Eq(t, []string{"x"}, got.State.Trail)
	})

	T.Run("reports a missing instance", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		runner := newTestRunner(t, store, NewRegistry())

		_, err := runner.Get(t.Context(), "nope")
		test.ErrorIs(t, err, ErrInstanceNotFound)
	})

	T.Run("reports a state type that is not the definition's", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[otherState]{
			Name: "orders",
			Steps: []Step[otherState]{{
				Name: "one",
				Do:   func(context.Context, *otherState) error { return nil },
			}},
		}))

		saveInstance(t, store, newRecord("i1", "orders", []string{"one"}, otherState{Name: "x"}, baseTime), baseTime)

		runner := newTestRunner(t, store, registry)

		_, err := runner.Get(t.Context(), "i1")
		test.ErrorIs(t, err, ErrStateTypeMismatch)
	})

	T.Run("reads an instance whose definition this process does not register", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		saveInstance(t, store, newRecord("i1", "elsewhere", []string{"one"}, testState{Amount: 9}, baseTime), baseTime)

		// A support tool has the store but not the code that runs the sagas.
		runner := newTestRunner(t, store, NewRegistry())

		got, err := runner.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, 9, got.State.Amount)
	})

	T.Run("reports state that will not decode into T", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)
		saveInstance(t, store, newRecord("i1", "elsewhere", []string{"one"}, testState{}, baseTime), baseTime)

		concrete, ok := store.(*SQLStore)
		must.True(t, ok)

		_, err := env.client.Writer().ExecContext(t.Context(),
			"UPDATE "+concrete.tables.instances+" SET state = 'not json' WHERE id = 'i1'")
		must.NoError(t, err)

		runner := newTestRunner(t, store, NewRegistry())

		_, err = runner.Get(t.Context(), "i1")
		test.Error(t, err)
	})
}

func TestRunner_List(T *testing.T) {
	T.Parallel()

	T.Run("lists and decodes", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		runner := newTestRunner(t, store, registry)

		for i := range 3 {
			_, err := runner.Start(t.Context(), "orders", testState{Amount: i})
			must.NoError(t, err)
		}

		result, err := runner.List(t.Context(), nil, nil)
		must.NoError(t, err)
		must.SliceLen(t, 3, result.Data)
		test.EqOp(t, uint64(3), result.TotalCount)

		narrowed, err := runner.List(t.Context(), &ListScope{Statuses: []Status{StatusStuck}}, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, narrowed.Data)
	})

	T.Run("carries the pagination through", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		runner := newTestRunner(t, store, registry)

		for range 3 {
			_, err := runner.Start(t.Context(), "orders", testState{})
			must.NoError(t, err)
		}

		filter := filtering.DefaultQueryFilter()
		filter.MaxResponseSize = pointer.To(uint16(2))

		page, err := runner.List(t.Context(), nil, filter)
		must.NoError(t, err)
		must.SliceLen(t, 2, page.Data)
		test.EqOp(t, uint64(3), page.TotalCount)
		test.EqOp(t, page.Data[1].ID, page.Cursor)
	})

	T.Run("reports an instance it cannot decode", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[otherState]{
			Name: "orders",
			Steps: []Step[otherState]{{
				Name: "one",
				Do:   func(context.Context, *otherState) error { return nil },
			}},
		}))

		saveInstance(t, store, newRecord("i1", "orders", []string{"one"}, otherState{}, baseTime), baseTime)

		runner := newTestRunner(t, store, registry)

		_, err := runner.List(t.Context(), nil, nil)
		test.ErrorIs(t, err, ErrStateTypeMismatch)
	})

	T.Run("reports a store that cannot be read", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client, WithTablePrefix("absent"))
		must.NoError(t, err)

		runner := newTestRunner(t, store, NewRegistry())

		_, err = runner.List(t.Context(), nil, nil)
		test.Error(t, err)
	})
}

func TestRunner_Resume(T *testing.T) {
	T.Parallel()

	// stickInstance drives a saga to StatusStuck by giving it a compensation
	// that never succeeds, then returns the instance and the registry it ran
	// under.
	stickInstance := func(t *testing.T, store Store) (*Registry, string) {
		t.Helper()

		registry := registryWith(t, "orders",
			Step[testState]{
				Name: "charge",
				Do:   func(context.Context, *testState) error { return nil },
				Undo: func(context.Context, *testState) error { return platformerrors.New("the refund API is down") },
			},
			Step[testState]{
				Name: "fail",
				Do:   func(context.Context, *testState) error { return retry.Unretryable(platformerrors.New("no")) },
			},
		)

		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 15)
		must.EqOp(t, StatusStuck, inst.Status)

		return registry, inst.ID
	}

	T.Run("returns a stuck instance to the phase it broke in", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry, id := stickInstance(t, store)

		runner := newTestRunner(t, store, registry)

		resumed, err := runner.Resume(t.Context(), id)
		must.NoError(t, err)
		test.EqOp(t, StatusCompensating, resumed.Status)
		test.EqOp(t, Status(""), resumed.ResumeStatus)
		test.EqOp(t, 0, resumed.Attempts)
	})

	T.Run("a resumed saga finishes unwinding once the cause is fixed", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		var refundWorks bool

		registry := registryWith(t, "orders",
			Step[testState]{
				Name: "charge",
				Do:   func(context.Context, *testState) error { return nil },
				Undo: func(context.Context, *testState) error {
					if !refundWorks {
						return platformerrors.New("the refund API is down")
					}

					return nil
				},
			},
			Step[testState]{
				Name: "fail",
				Do:   func(context.Context, *testState) error { return retry.Unretryable(platformerrors.New("no")) },
			},
		)

		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		stuck := drain(t, worker, store, clk, "i1", 15)
		must.EqOp(t, StatusStuck, stuck.Status)

		refundWorks = true

		runner, err := NewRunner[testState](store, registry, WithRunnerClock(clk))
		must.NoError(t, err)

		_, err = runner.Resume(t.Context(), "i1")
		must.NoError(t, err)

		final := drain(t, worker, store, clk, "i1", 5)
		test.EqOp(t, StatusCompensated, final.Status)
	})

	T.Run("refuses an instance that is not stuck", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		runner := newTestRunner(t, store, registry)

		started, err := runner.Start(t.Context(), "orders", testState{})
		must.NoError(t, err)

		_, err = runner.Resume(t.Context(), started.ID)
		test.ErrorIs(t, err, ErrNotResumable)
	})

	T.Run("reports a missing instance", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		runner := newTestRunner(t, store, NewRegistry())

		_, err := runner.Resume(t.Context(), "nope")
		test.ErrorIs(t, err, ErrInstanceNotFound)
	})

	T.Run("leaves an instance stuck when its definition is not registered", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		_, id := stickInstance(t, store)

		runner := newTestRunner(t, store, NewRegistry())

		_, err := runner.Resume(t.Context(), id)
		test.ErrorIs(t, err, ErrUnknownDefinition)

		still, err := store.Get(t.Context(), id)
		must.NoError(t, err)
		test.EqOp(t, StatusStuck, still.Status)
	})

	T.Run("leaves an instance stuck when its definition drifted", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		_, id := stickInstance(t, store)

		// A build whose definition gained a step.
		changed := registryWith(t, "orders", noopStep("charge"), noopStep("fail"), noopStep("audit"))
		runner := newTestRunner(t, store, changed)

		_, err := runner.Resume(t.Context(), id)
		test.ErrorIs(t, err, ErrDefinitionDrift)

		still, err := store.Get(t.Context(), id)
		must.NoError(t, err)
		test.EqOp(t, StatusStuck, still.Status)
	})

	T.Run("falls back to compensating for an instance with no resume status", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))

		inst := saveInstance(t, store, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)
		inst.Status = StatusStuck
		inst.UpdatedAt = baseTime
		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.Advance(t.Context(), q, inst, baseTime)
		}))

		runner := newTestRunner(t, store, registry)

		resumed, err := runner.Resume(t.Context(), "i1")
		must.NoError(t, err)

		// Unwinding a saga that had not started unwinding costs a set of no-op
		// Undo calls; running one that had costs the effects it was taking back.
		test.EqOp(t, StatusCompensating, resumed.Status)
	})

	T.Run("reports a requeue that matched nothing", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)
		registry, id := stickInstance(t, store)

		runner := newTestRunner(t, store, registry)

		_, err := runner.Resume(t.Context(), id)
		must.NoError(t, err)

		// Already resumed: the guard in the predicate refuses the second one.
		_, err = runner.Resume(t.Context(), id)
		test.ErrorIs(t, err, ErrNotResumable)
	})
}

func TestDecodeInstance(T *testing.T) {
	T.Parallel()

	T.Run("copies every field and clones the step names", func(t *testing.T) {
		t.Parallel()

		rec := newRecord("i1", "orders", []string{"a", "b"}, testState{Amount: 4}, baseTime)
		rec.Status = StatusStuck
		rec.ResumeStatus = StatusCompensating
		rec.LastError = "boom"
		rec.CurrentStep = 1
		rec.Attempts = 3

		inst, err := decodeInstance[testState](rec)
		must.NoError(t, err)

		test.EqOp(t, "i1", inst.ID)
		test.EqOp(t, StatusStuck, inst.Status)
		test.EqOp(t, StatusCompensating, inst.ResumeStatus)
		test.EqOp(t, "boom", inst.LastError)
		test.EqOp(t, 1, inst.CurrentStep)
		test.EqOp(t, 3, inst.Attempts)
		test.EqOp(t, 4, inst.State.Amount)

		inst.StepNames[0] = "tampered"
		test.EqOp(t, "a", rec.StepNames[0])
	})

	T.Run("an absent state decodes to the zero value", func(t *testing.T) {
		t.Parallel()

		rec := newRecord("i1", "orders", []string{"a"}, testState{}, baseTime)
		rec.State = nil

		inst, err := decodeInstance[testState](rec)
		must.NoError(t, err)
		test.EqOp(t, 0, inst.State.Amount)
	})
}

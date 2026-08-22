package saga

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/retry"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/shoenig/test/wait"
)

// registryWith builds a registry holding one definition of testState.
func registryWith(t *testing.T, name string, steps ...Step[testState]) *Registry {
	t.Helper()

	registry := NewRegistry()
	must.NoError(t, Register(registry, Definition[testState]{Name: name, Steps: steps}))

	return registry
}

// startedRecord saves a running instance whose step names match the registry's.
func startedRecord(t *testing.T, store Store, registry *Registry, definitionName, id string) *Record {
	t.Helper()

	names, ok := registry.StepNames(definitionName)
	must.True(t, ok)

	return saveInstance(t, store, newRecord(id, definitionName, names, testState{}, baseTime), baseTime)
}

// finalState decodes an instance's stored state.
func finalState(t *testing.T, inst *Record) testState {
	t.Helper()

	var s testState
	must.NoError(t, json.Unmarshal(inst.State, &s))

	return s
}

func TestWorker_HappyPath(T *testing.T) {
	T.Parallel()

	T.Run("runs every step in order and completes", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}
		registry := registryWith(t, "orders",
			trailStep(rec, "charge", nil, nil),
			trailStep(rec, "reserve", nil, nil),
			trailStep(rec, "notify", nil, nil),
		)

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 5)

		test.EqOp(t, StatusCompleted, inst.Status)
		test.EqOp(t, 3, inst.CurrentStep)
		test.EqOp(t, "", inst.LastError)
		test.Eq(t, []string{"do:charge", "do:reserve", "do:notify"}, rec.seen())
		test.Eq(t, []string{"do:charge", "do:reserve", "do:notify"}, finalState(t, inst).Trail)
	})

	T.Run("runs the whole saga in a single pass", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}
		registry := registryWith(t, "orders",
			trailStep(rec, "one", nil, nil),
			trailStep(rec, "two", nil, nil),
			trailStep(rec, "three", nil, nil),
		)

		store := newSQLiteEnv(t).newStore(t)
		worker := newWorker(t, store, registry, newStubClock())

		startedRecord(t, store, registry, "orders", "i1")

		// One cycle, not three: the pass carries on until the saga rests.
		drainOnce(t, worker)

		inst, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusCompleted, inst.Status)
		test.SliceLen(t, 3, rec.seen())
	})

	T.Run("persists state mutations between steps", func(t *testing.T) {
		t.Parallel()

		registry := registryWith(t, "orders",
			Step[testState]{
				Name: "add",
				Do: func(_ context.Context, s *testState) error {
					s.Amount += 10

					return nil
				},
			},
			Step[testState]{
				Name: "double",
				Do: func(_ context.Context, s *testState) error {
					// Only correct if the previous step's mutation survived the
					// round trip through the row.
					s.Amount *= 2

					return nil
				},
			},
		)

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 5)
		test.EqOp(t, StatusCompleted, inst.Status)
		test.EqOp(t, 20, finalState(t, inst).Amount)
	})
}

func TestWorker_Compensation(T *testing.T) {
	T.Parallel()

	T.Run("compensates in reverse order, including the step that failed", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}
		registry := registryWith(t, "orders",
			trailStep(rec, "charge", nil, nil),
			trailStep(rec, "reserve", nil, nil),
			trailStep(rec, "notify", platformerrors.New("the partner is down"), nil),
		)

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 10)

		test.EqOp(t, StatusCompensated, inst.Status)
		test.EqOp(t, -1, inst.CurrentStep)
		test.StrContains(t, inst.LastError, "the partner is down")

		// The failed step is compensated too: its Do may have applied half its
		// effect before returning.
		test.Eq(t, []string{
			"do:charge", "do:reserve", "do:notify",
			"do:notify", // the retry
			"undo:notify", "undo:reserve", "undo:charge",
		}, rec.seen())
	})

	T.Run("skips steps that declare no compensation", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}
		registry := registryWith(t, "orders",
			trailStep(rec, "charge", nil, nil),
			Step[testState]{
				Name: "read_only",
				Do: func(_ context.Context, s *testState) error {
					rec.record("do:read_only")

					return nil
				},
			},
			trailStep(rec, "notify", retry.Unretryable(platformerrors.New("rejected")), nil),
		)

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 10)

		test.EqOp(t, StatusCompensated, inst.Status)
		test.Eq(t, []string{
			"do:charge", "do:read_only", "do:notify",
			"undo:notify", "undo:charge",
		}, rec.seen())
	})

	T.Run("compensates when the very first step fails", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}
		registry := registryWith(t, "orders",
			trailStep(rec, "charge", retry.Unretryable(platformerrors.New("declined")), nil),
			trailStep(rec, "reserve", nil, nil),
		)

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 10)

		test.EqOp(t, StatusCompensated, inst.Status)
		test.Eq(t, []string{"do:charge", "undo:charge"}, rec.seen())
	})

	T.Run("a single-step saga with no Undo compensates to done", func(t *testing.T) {
		t.Parallel()

		registry := registryWith(t, "orders", Step[testState]{
			Name: "only",
			Do:   func(context.Context, *testState) error { return retry.Unretryable(platformerrors.New("no")) },
		})

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 10)
		test.EqOp(t, StatusCompensated, inst.Status)
	})

	T.Run("compensation persists the state its Undo mutated", func(t *testing.T) {
		t.Parallel()

		registry := registryWith(t, "orders",
			Step[testState]{
				Name: "add",
				Do: func(_ context.Context, s *testState) error {
					s.Amount = 7

					return nil
				},
				Undo: func(_ context.Context, s *testState) error {
					s.Amount = 0

					return nil
				},
			},
			Step[testState]{
				Name: "fail",
				Do:   func(context.Context, *testState) error { return retry.Unretryable(platformerrors.New("no")) },
			},
		)

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 10)
		test.EqOp(t, StatusCompensated, inst.Status)
		test.EqOp(t, 0, finalState(t, inst).Amount)
	})
}

func TestWorker_Retries(T *testing.T) {
	T.Parallel()

	T.Run("retries a failing step until its budget runs out", func(t *testing.T) {
		t.Parallel()

		var attempts atomic.Int64

		registry := registryWith(t, "orders", Step[testState]{
			Name: "flaky",
			Do: func(context.Context, *testState) error {
				attempts.Add(1)

				return platformerrors.New("transient")
			},
			Undo: func(context.Context, *testState) error { return nil },
		})

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 10)

		// MaxAttempts is two in the test config, so two Dos and then compensation.
		test.EqOp(t, int64(2), attempts.Load())
		test.EqOp(t, StatusCompensated, inst.Status)
	})

	T.Run("a step that eventually succeeds carries on", func(t *testing.T) {
		t.Parallel()

		var attempts atomic.Int64

		registry := registryWith(t, "orders",
			Step[testState]{
				Name: "flaky",
				Do: func(context.Context, *testState) error {
					if attempts.Add(1) == 1 {
						return platformerrors.New("transient")
					}

					return nil
				},
			},
			noopStep("after"),
		)

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 10)
		test.EqOp(t, StatusCompleted, inst.Status)
		test.EqOp(t, int64(2), attempts.Load())
	})

	T.Run("an unretryable error skips the remaining attempts", func(t *testing.T) {
		t.Parallel()

		var attempts atomic.Int64

		registry := registryWith(t, "orders", Step[testState]{
			Name: "rejected",
			Do: func(context.Context, *testState) error {
				attempts.Add(1)

				return retry.Unretryable(platformerrors.New("the account is closed"))
			},
			Undo: func(context.Context, *testState) error { return nil },
		})

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 10)
		test.EqOp(t, int64(1), attempts.Load())
		test.EqOp(t, StatusCompensated, inst.Status)
	})

	T.Run("records the rendered failure between attempts", func(t *testing.T) {
		t.Parallel()

		registry := registryWith(t, "orders", Step[testState]{
			Name: "flaky",
			Do:   func(context.Context, *testState) error { return platformerrors.New("the gateway timed out") },
			Undo: func(context.Context, *testState) error { return nil },
		})

		store := newSQLiteEnv(t).newStore(t)
		worker := newWorker(t, store, registry, newStubClock())

		startedRecord(t, store, registry, "orders", "i1")

		drainOnce(t, worker)

		inst, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusRunning, inst.Status)
		test.EqOp(t, 1, inst.Attempts)
		test.StrContains(t, inst.LastError, "the gateway timed out")
	})
}

func TestWorker_Stuck(T *testing.T) {
	T.Parallel()

	T.Run("a compensation that exhausts its budget marks the instance stuck", func(t *testing.T) {
		t.Parallel()

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

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 15)

		test.EqOp(t, StatusStuck, inst.Status)
		test.EqOp(t, StatusCompensating, inst.ResumeStatus)
		test.StrContains(t, inst.LastError, "the refund API is down")
	})

	T.Run("an unknown definition marks the instance stuck", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		saveInstance(t, store, newRecord("i1", "refunds", []string{"one"}, testState{}, baseTime), baseTime)

		drainOnce(t, worker)

		inst, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusStuck, inst.Status)
		test.EqOp(t, StatusRunning, inst.ResumeStatus)
		test.StrContains(t, inst.LastError, "not registered")
	})

	T.Run("a definition whose steps changed marks the instance stuck", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"), noopStep("two"))
		worker := newWorker(t, store, registry, newStubClock())

		// Started under a one-step definition; this build has two.
		saveInstance(t, store, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)

		drainOnce(t, worker)

		inst, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusStuck, inst.Status)
		test.StrContains(t, inst.LastError, "started with steps")
		test.StrContains(t, inst.LastError, "two")
	})

	T.Run("a status this build does not know marks the instance stuck", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		worker := newWorker(t, store, registry, newStubClock())

		inst := saveInstance(t, store, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)

		// Written by a future build that knew a sixth status. The claim
		// predicate would not return it, so it is fed to step directly.
		inst.Status = Status("paused")

		_, err := worker.step(t.Context(), mustLookup(t, registry, "orders"), inst)
		must.NoError(t, err)

		got, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusStuck, got.Status)
	})

	T.Run("a cursor outside the step list marks a compensating instance stuck", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		worker := newWorker(t, store, registry, newStubClock())

		inst := saveInstance(t, store, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)
		inst.Status = StatusCompensating
		inst.CurrentStep = 9

		_, err := worker.step(t.Context(), mustLookup(t, registry, "orders"), inst)
		must.NoError(t, err)

		got, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusStuck, got.Status)
		test.StrContains(t, got.LastError, "outside the")
	})

	T.Run("a running cursor past the step list completes rather than sticking", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		worker := newWorker(t, store, registry, newStubClock())

		inst := saveInstance(t, store, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)
		inst.CurrentStep = 9

		_, err := worker.step(t.Context(), mustLookup(t, registry, "orders"), inst)
		must.NoError(t, err)

		got, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusCompleted, got.Status)
	})
}

func mustLookup(t *testing.T, registry *Registry, name string) *definition {
	t.Helper()

	def, ok := registry.lookup(name)
	must.True(t, ok)

	return def
}

func TestWorker_Panics(T *testing.T) {
	T.Parallel()

	T.Run("a panicking Do is contained and compensated", func(t *testing.T) {
		t.Parallel()

		registry := registryWith(t, "orders", Step[testState]{
			Name: "boom",
			Do: func(context.Context, *testState) error {
				panic("a nil map in somebody's payment client")
			},
			Undo: func(context.Context, *testState) error { return nil },
		})

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 10)

		test.EqOp(t, StatusCompensated, inst.Status)
	})

	T.Run("a panicking Undo is contained and sticks the instance", func(t *testing.T) {
		t.Parallel()

		registry := registryWith(t, "orders",
			Step[testState]{
				Name: "charge",
				Do:   func(context.Context, *testState) error { return nil },
				Undo: func(context.Context, *testState) error { panic("refund exploded") },
			},
			Step[testState]{
				Name: "fail",
				Do:   func(context.Context, *testState) error { return retry.Unretryable(platformerrors.New("no")) },
			},
		)

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 15)

		test.EqOp(t, StatusStuck, inst.Status)
		test.StrContains(t, inst.LastError, "refund exploded")
	})
}

func TestWorker_Delays(T *testing.T) {
	T.Parallel()

	T.Run("a delayed step waits for the clock", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}

		second := trailStep(rec, "later", nil, nil)
		second.Delay = time.Hour

		registry := registryWith(t, "orders", trailStep(rec, "now", nil, nil), second)

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		drainOnce(t, worker)

		inst, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusRunning, inst.Status)
		test.EqOp(t, 1, inst.CurrentStep)
		test.Eq(t, []string{"do:now"}, rec.seen())

		// Not yet.
		clk.advance(30 * time.Minute)
		drainOnce(t, worker)

		test.Eq(t, []string{"do:now"}, rec.seen())

		// Now.
		clk.advance(31 * time.Minute)
		drainOnce(t, worker)

		inst, err = store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusCompleted, inst.Status)
		test.Eq(t, []string{"do:now", "do:later"}, rec.seen())
	})

	T.Run("compensation does not honor delays", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}

		first := trailStep(rec, "one", nil, nil)
		second := trailStep(rec, "two", retry.Unretryable(platformerrors.New("no")), nil)
		second.Delay = 0

		registry := registryWith(t, "orders", first, second)

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		// One pass unwinds the whole thing, with no clock advance at all.
		drainOnce(t, worker)

		inst, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusCompensated, inst.Status)
		test.Eq(t, []string{"do:one", "do:two", "undo:two", "undo:one"}, rec.seen())
	})
}

func TestWorker_Idempotency(T *testing.T) {
	T.Parallel()

	T.Run("replays a recorded step instead of re-running it", func(t *testing.T) {
		t.Parallel()

		var runs atomic.Int64

		registry := registryWith(t, "orders", Step[testState]{
			Name: "charge",
			Do: func(_ context.Context, s *testState) error {
				runs.Add(1)
				s.Amount = 42

				return nil
			},
		})

		env := newSQLiteEnv(t)
		store := env.newStore(t)
		clk := newStubClock()
		manager := newIdempotencyManager(t)

		// A store whose Advance always fails, so the step succeeds and its
		// progress is never recorded — the crash-between-effect-and-record case.
		failing := &failingAdvanceStore{Store: store}
		worker := newWorker(t, failing, registry, clk, WithWorkerIdempotency(manager))

		startedRecord(t, store, registry, "orders", "i1")

		drainOnce(t, worker)
		test.EqOp(t, int64(1), runs.Load())

		// A working store now, and past the first worker's lease. The step is
		// replayed from the idempotency record rather than executed again.
		clk.advance(10 * time.Minute)

		healthy := newWorker(t, store, registry, clk, WithWorkerIdempotency(manager))
		drainOnce(t, healthy)

		test.EqOp(t, int64(1), runs.Load())

		inst, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusCompleted, inst.Status)
		test.EqOp(t, 42, finalState(t, inst).Amount)
	})

	T.Run("a failed step is not pinned by its key", func(t *testing.T) {
		t.Parallel()

		var runs atomic.Int64

		registry := registryWith(t, "orders", Step[testState]{
			Name: "flaky",
			Do: func(context.Context, *testState) error {
				if runs.Add(1) == 1 {
					return platformerrors.New("transient")
				}

				return nil
			},
		})

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk, WithWorkerIdempotency(newIdempotencyManager(t)))

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 10)

		test.EqOp(t, StatusCompleted, inst.Status)
		test.EqOp(t, int64(2), runs.Load())
	})

	T.Run("Do and Undo do not share a key", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}
		registry := registryWith(t, "orders",
			trailStep(rec, "charge", nil, nil),
			trailStep(rec, "fail", retry.Unretryable(platformerrors.New("no")), nil),
		)

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk, WithWorkerIdempotency(newIdempotencyManager(t)))

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 10)

		test.EqOp(t, StatusCompensated, inst.Status)
		test.Eq(t, []string{"do:charge", "do:fail", "undo:fail", "undo:charge"}, rec.seen())
	})

	T.Run("the key names the instance, the phase, and the step, and not the attempt", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("charge"))
		worker := newWorker(t, store, registry, newStubClock())

		test.EqOp(t, "saga:i1:do:charge", string(worker.stepKey("i1", phaseDo, "charge")))
		test.EqOp(t, "saga:i1:undo:charge", string(worker.stepKey("i1", phaseUndo, "charge")))

		inst := newRecord("i1", "orders", []string{"charge"}, testState{}, baseTime)
		test.EqOp(t, "orders:do:charge", string(worker.stepFingerprint(inst, phaseDo, "charge")))

		// The fingerprint must not move as the state does, or every resumption
		// would report a mismatch.
		inst.State = []byte(`{"amount":99}`)
		inst.Attempts = 7
		test.EqOp(t, "orders:do:charge", string(worker.stepFingerprint(inst, phaseDo, "charge")))
	})
}

func TestWorker_Locking(T *testing.T) {
	T.Parallel()

	T.Run("a contended instance is handed straight back", func(t *testing.T) {
		t.Parallel()

		var runs atomic.Int64

		registry := registryWith(t, "orders", Step[testState]{
			Name: "one",
			Do: func(context.Context, *testState) error {
				runs.Add(1)

				return nil
			},
		})

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()

		worker, err := NewWorker(t.Context(), testWorkerConfig(), store, registry, heldLocker{},
			WithWorkerClock(clk))
		must.NoError(t, err)

		startedRecord(t, store, registry, "orders", "i1")

		drainOnce(t, worker)

		test.EqOp(t, int64(0), runs.Load())

		// The lease was released, so the next cycle can claim it again.
		claimed, claimErr := store.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Hour))
		must.NoError(t, claimErr)
		test.SliceLen(t, 1, claimed)
	})

	T.Run("a failing release is logged rather than propagated", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))

		worker, err := NewWorker(t.Context(), testWorkerConfig(),
			&failingReleaseStore{Store: store}, registry, heldLocker{}, WithWorkerClock(newStubClock()))
		must.NoError(t, err)

		startedRecord(t, store, registry, "orders", "i1")

		// Does not panic, does not fail the cycle.
		drainOnce(t, worker)
	})
}

func TestWorker_Lifecycle(T *testing.T) {
	T.Parallel()

	T.Run("Run stops on Close", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		worker := newWorker(t, store, registry, newStubClock())

		go worker.Run()

		must.NoError(t, worker.Close(t.Context()))
		// Idempotent.
		must.NoError(t, worker.Close(t.Context()))
	})

	T.Run("Close reports a context that expired first", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		worker := newWorker(t, store, registry, newStubClock())

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		// Run was never started, so done never closes.
		test.Error(t, worker.Close(ctx))
	})

	T.Run("Run advances what it claims", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))

		cfg := testWorkerConfig()
		cfg.PollInterval = time.Millisecond

		worker, err := NewWorker(t.Context(), cfg, store, registry, newScopedLocker(t))
		must.NoError(t, err)

		// Stamped from the wall clock, because this is the one test that runs
		// the real loop rather than driving cycles by hand.
		now := time.Now().UTC()
		names, ok := registry.StepNames("orders")
		must.True(t, ok)
		saveInstance(t, store, newRecord("i1", "orders", names, testState{}, now), now)

		go worker.Run()
		t.Cleanup(func() { _ = worker.Close(context.Background()) })

		must.Wait(t, wait.InitialSuccess(
			wait.BoolFunc(func() bool {
				inst, getErr := store.Get(t.Context(), "i1")

				return getErr == nil && inst.Status == StatusCompleted
			}),
			wait.Timeout(10*time.Second),
			wait.Gap(5*time.Millisecond),
		))
	})

	T.Run("a claim failure is counted and the cycle carries on", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))

		worker, err := NewWorker(t.Context(), testWorkerConfig(),
			&failingClaimStore{Store: store}, registry, newScopedLocker(t), WithWorkerClock(newStubClock()))
		must.NoError(t, err)

		// Does not panic.
		drainOnce(t, worker)
	})
}

func TestNewWorker(T *testing.T) {
	T.Parallel()

	T.Run("rejects missing dependencies", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))
		locker := newScopedLocker(t)

		_, err := NewWorker(t.Context(), nil, store, registry, locker)
		test.Error(t, err)

		_, err = NewWorker(t.Context(), testWorkerConfig(), nil, registry, locker)
		test.ErrorIs(t, err, ErrNilStore)

		_, err = NewWorker(t.Context(), testWorkerConfig(), store, nil, locker)
		test.ErrorIs(t, err, ErrNilRegistry)

		_, err = NewWorker(t.Context(), testWorkerConfig(), store, registry, nil)
		test.ErrorIs(t, err, ErrNilLocker)
	})

	T.Run("rejects a config that cannot be satisfied", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))

		cfg := testWorkerConfig()
		cfg.LeaseDuration = time.Second

		_, err := NewWorker(t.Context(), cfg, store, registry, newScopedLocker(t))
		test.Error(t, err)
	})

	T.Run("ignores nil options", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		registry := registryWith(t, "orders", noopStep("one"))

		worker, err := NewWorker(t.Context(), testWorkerConfig(), store, registry, newScopedLocker(t),
			nil,
			WithWorkerClock(nil),
			WithWorkerLogger(nil),
			WithWorkerTracerProvider(nil),
			WithWorkerMetricsProvider(nil),
			WithWorkerIdempotency(nil),
			WithWorkerEventPublisher(nil),
		)
		must.NoError(t, err)
		must.NotNil(t, worker)
	})
}

func TestWorker_AdvanceBudget(T *testing.T) {
	T.Parallel()

	T.Run("a pass that runs out of budget hands the instance back mid-saga", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}

		store := newSQLiteEnv(t).newStore(t)
		clk := newStubClock()

		// Every step burns the whole pass budget, so drive stops before the
		// second one and releases rather than holding the lease.
		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[testState]{
			Name: "orders",
			Steps: []Step[testState]{
				{
					Name: "one",
					Do: func(_ context.Context, s *testState) error {
						rec.record("do:one")
						clk.advance(time.Hour)

						return nil
					},
				},
				{
					Name: "two",
					Do: func(_ context.Context, s *testState) error {
						rec.record("do:two")

						return nil
					},
				},
			},
		}))

		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		drainOnce(t, worker)

		test.Eq(t, []string{"do:one"}, rec.seen())

		inst, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusRunning, inst.Status)
		test.EqOp(t, 1, inst.CurrentStep)

		// Released rather than held: claimable again immediately. The lease
		// taken here expires at once, so the assertion does not steal the
		// instance from the pass below.
		claimed, claimErr := store.Claim(t.Context(), clk.read(), 10, clk.read())
		must.NoError(t, claimErr)
		test.SliceLen(t, 1, claimed)

		// And the next pass finishes it.
		drainOnce(t, worker)

		inst, err = store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusCompleted, inst.Status)
		test.Eq(t, []string{"do:one", "do:two"}, rec.seen())
	})
}

func TestTruncate(T *testing.T) {
	T.Parallel()

	T.Run("leaves a short error alone", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "short", truncateError(platformerrors.New("short")))
	})

	T.Run("cuts a long error without splitting a rune", func(t *testing.T) {
		t.Parallel()

		// A truncated error still has to be valid UTF-8 or the row will not store.
		long := platformerrors.New(strings.Repeat("é", maxStoredErrorLength))
		got := truncateError(long)

		test.True(t, len(got) <= maxStoredErrorLength)
		test.EqOp(t, strings.Repeat("é", maxStoredErrorLength/2), got)
	})

	T.Run("renders a nil error as empty", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", truncateError(nil))
	})

	T.Run("bounds a stored error", func(t *testing.T) {
		t.Parallel()

		rendered := truncateError(platformerrors.New(strings.Repeat("x", maxStoredErrorLength*2)))
		test.EqOp(t, maxStoredErrorLength, len(rendered))
	})
}

// runWorkerSuite drives a whole saga end to end against whichever dialect the
// environment carries.
//
// It is short on purpose. The exhaustive behavioral cases run against SQLite,
// where they are fast; what a real server adds is the guarded advance, the
// claim predicate, and the timestamp round trip, and one saga of each shape
// exercises all three.
func runWorkerSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a saga runs to completion", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}
		registry := registryWith(t, "orders",
			trailStep(rec, "charge", nil, nil),
			trailStep(rec, "reserve", nil, nil),
		)

		store := env.newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 5)

		test.EqOp(t, StatusCompleted, inst.Status)
		test.Eq(t, []string{"do:charge", "do:reserve"}, rec.seen())
		test.Eq(t, []string{"do:charge", "do:reserve"}, finalState(t, inst).Trail)
	})

	t.Run("a failing saga unwinds", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}
		registry := registryWith(t, "orders",
			trailStep(rec, "charge", nil, nil),
			trailStep(rec, "notify", retry.Unretryable(platformerrors.New("the partner is down")), nil),
		)

		store := env.newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		inst := drain(t, worker, store, clk, "i1", 10)

		test.EqOp(t, StatusCompensated, inst.Status)
		test.EqOp(t, -1, inst.CurrentStep)
		test.StrContains(t, inst.LastError, "the partner is down")
		test.Eq(t, []string{"do:charge", "do:notify", "undo:notify", "undo:charge"}, rec.seen())
	})

	t.Run("a failing compensation sticks and then resumes", func(t *testing.T) {
		t.Parallel()

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

		store := env.newStore(t)
		clk := newStubClock()
		worker := newWorker(t, store, registry, clk)

		startedRecord(t, store, registry, "orders", "i1")

		stuck := drain(t, worker, store, clk, "i1", 15)
		must.EqOp(t, StatusStuck, stuck.Status)
		test.EqOp(t, StatusCompensating, stuck.ResumeStatus)

		refundWorks = true

		runner, err := NewRunner[testState](store, registry, WithRunnerClock(clk))
		must.NoError(t, err)

		_, err = runner.Resume(t.Context(), "i1")
		must.NoError(t, err)

		final := drain(t, worker, store, clk, "i1", 5)
		test.EqOp(t, StatusCompensated, final.Status)
	})
}

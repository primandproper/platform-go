package operations

import (
	"context"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/panicking"
	"github.com/primandproper/platform-go/v13/workqueue"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewWorker(T *testing.T) {
	T.Parallel()

	T.Run("rejects what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := NewWorker(t.Context(), nil, newFakeStore(), nil, NewRegistry())
		test.ErrorIs(t, err, ErrNilConfig)

		_, err = NewWorker(t.Context(), &WorkerConfig{}, nil, nil, NewRegistry())
		test.ErrorIs(t, err, ErrNilStore)

		// A worker with no queue would claim nothing forever, which looks
		// exactly like a fleet with nothing to do.
		_, err = NewWorker(t.Context(), &WorkerConfig{}, newFakeStore(), nil, NewRegistry())
		test.ErrorIs(t, err, ErrNilQueue)
	})

	T.Run("rejects a config it cannot honor", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{Lease: time.Second, ProgressInterval: time.Second}

		_, err := NewWorker(t.Context(), cfg, newFakeStore(), nil, NewRegistry())

		test.Error(t, err)
	})
}

func TestClassify(T *testing.T) {
	T.Parallel()

	T.Run("an unclassified error is internal and retryable", func(t *testing.T) {
		t.Parallel()

		opErr := classify(platformerrors.New("the upstream is down"))

		test.EqOp(t, CodeInternal, opErr.Code)
		test.EqOp(t, "the upstream is down", opErr.Message)
		test.True(t, opErr.Retryable)
	})

	T.Run("a Runner's code survives", func(t *testing.T) {
		t.Parallel()

		opErr := classify(Fail("no_such_subject", "no subject %q", "s1"))

		test.EqOp(t, "no_such_subject", opErr.Code)
		test.True(t, opErr.Retryable)
	})

	T.Run("Unretryable is honored", func(t *testing.T) {
		t.Parallel()

		opErr := classify(Unretryable(Fail("no_such_subject", "no such subject")))

		test.EqOp(t, "no_such_subject", opErr.Code)
		test.False(t, opErr.Retryable)
	})

	// A panic and a dependency having a bad day want different responses, so
	// they must not collapse into one code. A panic is also never retried:
	// a nil map dereference is deterministic.
	T.Run("a panic is its own code and is not retried", func(t *testing.T) {
		t.Parallel()

		err := panicking.Contain(func() error { panic("a nil map") })

		opErr := classify(err)

		test.EqOp(t, CodePanic, opErr.Code)
		test.False(t, opErr.Retryable)
		test.StrContains(t, opErr.Message, "a nil map")
	})

	// The message reaches API clients, so the stack must not be in it. It goes
	// on the span, where diagnostics are read.
	T.Run("a panic message carries no stack", func(t *testing.T) {
		t.Parallel()

		err := panicking.Contain(func() error { panic("a nil map") })

		test.StrNotContains(t, classify(err).Message, "goroutine")
		test.StrNotContains(t, classify(err).Message, ".go:")
	})
}

func TestWorker_maxAttempts(T *testing.T) {
	T.Parallel()

	w := &Worker{cfg: WorkerConfig{MaxAttempts: 5}}

	test.EqOp(T, 5, w.maxAttempts(&runner{}))

	// A reindex that takes an hour and a webhook replay that takes a second do
	// not want the same budget, and the difference belongs next to the work.
	test.EqOp(T, 2, w.maxAttempts(&runner{maxAttempts: 2}))
}

// The Runner is handed the attempt the worker is on, and Final agrees with the
// budget the retry decision below uses — the two reading the same number is the
// whole point, since a Runner that reported "we have given up" on an attempt
// that then retried would tell somebody the wrong thing.
func TestWorker_runHandsTheRunnerItsAttempt(T *testing.T) {
	T.Parallel()

	for _, tc := range []struct {
		name     string
		attempts int
		final    bool
	}{
		{name: "first of three", attempts: 1, final: false},
		{name: "last of three", attempts: 3, final: true},
	} {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var seen Attempt

			registry := NewRegistry()
			must.NoError(t, Register(registry, Definition[exportRequest]{
				Kind: "export",
				Run: func(_ context.Context, _ exportRequest, rep Reporter) (*Result, error) {
					seen = rep.Attempt()

					return nil, nil
				},
			}))

			op := &Operation{ID: "op1", Kind: "export", State: StateRunning}
			w := newTestWorker(t, newFakeStore(op), registry)

			runOnce(t, w, op, tc.attempts)

			test.EqOp(t, "op1", seen.ID)
			test.EqOp(t, tc.attempts, seen.Number)
			test.EqOp(t, tc.final, seen.Final)
		})
	}

	// The kind's own ceiling wins, so a Runner registered with a shorter budget
	// than the worker's learns it is final sooner.
	T.Run("the kind's own ceiling decides", func(t *testing.T) {
		t.Parallel()

		var seen Attempt

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[exportRequest]{
			Kind:        "export",
			MaxAttempts: 1,
			Run: func(_ context.Context, _ exportRequest, rep Reporter) (*Result, error) {
				seen = rep.Attempt()

				return nil, nil
			},
		}))

		op := &Operation{ID: "op1", Kind: "export", State: StateRunning}
		w := newTestWorker(t, newFakeStore(op), registry)

		runOnce(t, w, op, 1)

		test.True(t, seen.Final)
	})
}

// newTestWorker builds a worker with no queue, for the tests that exercise run
// rather than the claim loop.
func newTestWorker(t *testing.T, store Store, registry *Registry) *Worker {
	t.Helper()

	cfg := &WorkerConfig{MaxAttempts: 3}
	cfg.EnsureDefaults()

	w := &Worker{
		cfg:      *cfg,
		store:    store,
		registry: registry,
		o11y:     observability.NewObserverForTest("operations_worker_test"),
	}
	must.NoError(t, w.buildInstruments(metrics.EnsureMetricsProvider(nil)))

	return w
}

// runOnce drives one operation through the worker's run step.
func runOnce(t *testing.T, w *Worker, op *Operation, attempts int) outcome {
	t.Helper()

	_, span := w.o11y.Begin(t.Context())
	defer span.End()

	return w.run(t.Context(), t.Context(), span, op, workqueue.Item[string]{Key: op.ID, Attempts: attempts})
}

func TestWorker_run(T *testing.T) {
	T.Parallel()

	T.Run("a Runner that returns cleanly succeeds", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[exportRequest]{
			Kind: "export",
			Run: func(context.Context, exportRequest, Reporter) (*Result, error) {
				return &Result{URI: "s3://bundle"}, nil
			},
		}))

		op := &Operation{ID: "op1", Kind: "export", State: StateRunning}
		w := newTestWorker(t, newFakeStore(op), registry)

		result := runOnce(t, w, op, 1)

		test.EqOp(t, StateSucceeded, result.state)
		must.NotNil(t, result.result)
		test.EqOp(t, "s3://bundle", result.result.URI)
	})

	T.Run("a retryable failure with budget left retries", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[exportRequest]{
			Kind: "export",
			Run: func(context.Context, exportRequest, Reporter) (*Result, error) {
				return nil, platformerrors.New("the upstream is down")
			},
		}))

		op := &Operation{ID: "op1", Kind: "export", State: StateRunning}
		w := newTestWorker(t, newFakeStore(op), registry)

		result := runOnce(t, w, op, 1)

		test.True(t, result.retry)
		must.NotNil(t, result.opErr)
		test.EqOp(t, CodeInternal, result.opErr.Code)
	})

	// The promise: an operation always reaches a terminal state. Out of budget
	// is where that promise is actually kept.
	T.Run("a retryable failure out of budget fails", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[exportRequest]{
			Kind: "export",
			Run: func(context.Context, exportRequest, Reporter) (*Result, error) {
				return nil, platformerrors.New("the upstream is still down")
			},
		}))

		op := &Operation{ID: "op1", Kind: "export", State: StateRunning}
		w := newTestWorker(t, newFakeStore(op), registry)

		result := runOnce(t, w, op, 3)

		test.False(t, result.retry)
		test.EqOp(t, StateFailed, result.state)
		must.NotNil(t, result.opErr)

		// The code says why it stopped rather than repeating the last symptom
		// as though it were the reason; the message keeps the symptom.
		test.EqOp(t, CodeAttemptsExhausted, result.opErr.Code)
		test.StrContains(t, result.opErr.Message, "still down")
	})

	T.Run("an unretryable failure fails at once", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[exportRequest]{
			Kind: "export",
			Run: func(context.Context, exportRequest, Reporter) (*Result, error) {
				return nil, Unretryable(Fail("no_such_subject", "no such subject"))
			},
		}))

		op := &Operation{ID: "op1", Kind: "export", State: StateRunning}
		w := newTestWorker(t, newFakeStore(op), registry)

		result := runOnce(t, w, op, 1)

		test.False(t, result.retry)
		test.EqOp(t, StateFailed, result.state)
		test.EqOp(t, "no_such_subject", result.opErr.Code)
	})

	// Somebody else's code running in our goroutine should cost its own
	// operation, not the worker loop keeping the rest of the fleet moving.
	T.Run("a panicking Runner fails its own operation", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[exportRequest]{
			Kind: "export",
			Run: func(context.Context, exportRequest, Reporter) (*Result, error) {
				panic("a nil map")
			},
		}))

		op := &Operation{ID: "op1", Kind: "export", State: StateRunning}
		w := newTestWorker(t, newFakeStore(op), registry)

		result := runOnce(t, w, op, 1)

		test.EqOp(t, StateFailed, result.state)
		test.EqOp(t, CodePanic, result.opErr.Code)
	})

	// A kind vanishes from a build because somebody deleted or renamed it.
	// Retrying a name nothing will ever answer to burns the whole budget
	// arriving at the same place a good deal later.
	T.Run("an unregistered kind fails without retrying", func(t *testing.T) {
		t.Parallel()

		op := &Operation{ID: "op1", Kind: "gone", State: StateRunning}
		w := newTestWorker(t, newFakeStore(op), NewRegistry())

		result := runOnce(t, w, op, 1)

		test.False(t, result.retry)
		test.EqOp(t, StateFailed, result.state)
		test.EqOp(t, CodeUnknownKind, result.opErr.Code)
	})

	// Cancellation beats both success and failure: a Runner that stopped early
	// because it was asked to may return either, and recording it at face value
	// would report a partial export as complete.
	T.Run("a cancelled operation is cancelled, not succeeded", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[exportRequest]{
			Kind: "export",
			Run: func(_ context.Context, _ exportRequest, rep Reporter) (*Result, error) {
				<-rep.Cancelled()

				return &Result{URI: "s3://partial"}, nil
			},
		}))

		op := &Operation{ID: "op1", Kind: "export", State: StateRunning, CancelRequested: true}
		w := newTestWorker(t, newFakeStore(op), registry)

		result := runOnce(t, w, op, 1)

		test.EqOp(t, StateCancelled, result.state)
		test.Nil(t, result.opErr)
	})

	T.Run("a cancelled operation that errored records why", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[exportRequest]{
			Kind: "export",
			Run: func(_ context.Context, _ exportRequest, rep Reporter) (*Result, error) {
				<-rep.Cancelled()

				return nil, platformerrors.New("stopped mid-domain")
			},
		}))

		op := &Operation{ID: "op1", Kind: "export", State: StateRunning, CancelRequested: true}
		w := newTestWorker(t, newFakeStore(op), registry)

		result := runOnce(t, w, op, 1)

		test.EqOp(t, StateCancelled, result.state)
		must.NotNil(t, result.opErr)
		test.EqOp(t, CodeCancelled, result.opErr.Code)
	})

	// A worker whose lease lapsed mid-operation must record nothing: the row has
	// moved on, and whoever owns it now will produce their own outcome.
	T.Run("a lost lease abandons rather than finishing", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[exportRequest]{
			Kind: "export",
			Run: func(context.Context, exportRequest, Reporter) (*Result, error) {
				return &Result{URI: "s3://bundle"}, nil
			},
		}))

		store := newFakeStore(&Operation{ID: "op1", Kind: "export", State: StateRunning})
		store.progressFunc = func(string, Progress) (Ack, error) { return Ack{Held: false}, nil }

		w := newTestWorker(t, store, registry)

		result := runOnce(t, w, &Operation{ID: "op1", Kind: "export", State: StateRunning}, 1)

		test.True(t, result.abandon)
		test.SliceEmpty(t, store.finishes())
	})

	// The two tiers reach the row through the reporter the worker built, which
	// is the only path they have.
	T.Run("reported progress reaches the store", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		must.NoError(t, Register(registry, Definition[exportRequest]{
			Kind:       "export",
			CountLabel: "records",
			Run: func(_ context.Context, _ exportRequest, rep Reporter) (*Result, error) {
				rep.SetUnits(2)

				for _, unit := range []string{"identity", "webhooks"} {
					rep.StartUnit(unit)
					rep.Advance(150)
					rep.FinishUnit()
				}

				rep.Sayf("finished")

				return nil, nil
			},
		}))

		store := newFakeStore(&Operation{ID: "op1", Kind: "export", State: StateRunning})
		w := newTestWorker(t, store, registry)

		result := runOnce(t, w, &Operation{ID: "op1", Kind: "export", State: StateRunning}, 1)

		test.EqOp(t, StateSucceeded, result.state)

		final := store.lastProgress()
		test.EqOp(t, 2, final.UnitsDone)
		must.NotNil(t, final.UnitsTotal)
		test.EqOp(t, 2, *final.UnitsTotal)
		test.EqOp(t, int64(300), final.Count)
		test.EqOp(t, "finished", final.Message)
	})
}

func TestErrorOf(T *testing.T) {
	T.Parallel()

	test.Nil(T, errorOf(nil))

	err := errorOf(&Error{Code: "boom", Message: "went wrong"})
	must.Error(T, err)
	test.EqOp(T, "boom: went wrong", err.Error())
}

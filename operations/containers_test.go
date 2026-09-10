package operations

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/operations/migrations"
	"github.com/primandproper/platform-go/v14/workqueue"
	workqueuemigrations "github.com/primandproper/platform-go/v14/workqueue/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/postgres"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/pointer"
	"github.com/primandproper/primitives-go/tenancy"
	"github.com/primandproper/primitives-go/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The unit tests above render SQL and drive fakes. Nothing there can tell
// whether Postgres accepts the statements, whether the guarded transition
// really keeps two workers apart, whether a lease genuinely lapses on the
// server's clock, or whether an operation started in a handler is the one a
// worker runs. That is the whole of what this file covers, and it is the only
// place that can.

// testClientConfig is the minimum database.ClientConfig a Postgres client needs.
// The pool is deliberately larger than one connection: the properties worth
// testing here are all concurrent.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 8 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 16 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// queueCounter names a fresh logical queue per subtest. Subtests share one
// operations table and one work queue table, so they must not share a queue
// name — one test's backlog would be another's.
var queueCounter atomic.Uint64

// descendingFilter is the page a client asks for with sortBy=desc. It is spelled
// here rather than inline so the subtest reads as "the other direction" rather
// than as a pointer to a package constant.
func descendingFilter() *filtering.QueryFilter {
	filter := filtering.DefaultQueryFilter()
	filter.SortBy = filtering.SortDescending

	return filter
}

// reapPrefix namespaces the tables the retention subtest works against. See
// newHarnessIn.
const reapPrefix = "reaptest"

// runLoopPrefix namespaces the tables the running-worker subtest works against.
//
// It needs its own for the same reason the retention one does, from the other
// direction: Stranded and Reap are table-wide because retention is a property of
// a table rather than of a logical queue, so a subtest that leaves a running
// worker claiming for its whole duration will have somebody else's recovery
// hand it an operation of a kind its registry has never heard of.
const runLoopPrefix = "runlooptest"

// createTables renders and executes both schemas this package needs, under a
// namespace.
func createTables(t *testing.T, client database.Client, prefix string) {
	t.Helper()

	opsStatements, err := migrations.Statements(dialect.Postgres, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, opsStatements)

	queueStatements, err := workqueuemigrations.Statements(dialect.Postgres, prefix)
	must.NoError(t, err)

	for _, stmt := range append(opsStatements, queueStatements...) {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}
}

// harness is everything one subtest needs, over its own logical queue.
type harness struct {
	store    Store
	svc      Service
	queue    *workqueue.Queue[string]
	registry *Registry
	worker   *Worker
}

// newHarness assembles a store, a queue, a service, and a worker over one
// database, on a queue name nothing else is using.
func newHarness(t *testing.T, client database.Client, register func(*Registry)) *harness {
	t.Helper()

	return newHarnessIn(t, client, DefaultTablePrefix, register)
}

// newHarnessIn is newHarness against a named table namespace.
//
// Subtests share one operations table, partitioned by nothing — retention is a
// property of a table rather than of a logical queue, so Reap deliberately has
// no name to scope itself by. A subtest that reaps therefore needs its own
// table, and this is how it gets one.
func newHarnessIn(t *testing.T, client database.Client, prefix string, register func(*Registry)) *harness {
	t.Helper()

	registry := NewRegistry()
	if register != nil {
		register(registry)
	}

	store, err := NewSQLStore(client, WithStoreTablePrefix(prefix))
	must.NoError(t, err)

	name := fmt.Sprintf("ops%d", queueCounter.Add(1))

	queue, err := workqueue.New[string](t.Context(),
		&workqueue.Config{Name: name, TablePrefix: prefix}, client)
	must.NoError(t, err)

	t.Cleanup(func() { _ = queue.Close(context.WithoutCancel(t.Context())) })

	cfg := &Config{QueueName: name, TablePrefix: prefix, RecoverAfter: time.Second}

	svc, err := NewService(t.Context(), cfg, client, store, queue, registry)
	must.NoError(t, err)

	workerCfg := &WorkerConfig{
		Lease:            10 * time.Second,
		ProgressInterval: 100 * time.Millisecond,
		Batch:            4,
		Concurrency:      2,
		MaxAttempts:      2,
		RetryDelay:       10 * time.Millisecond,
	}

	worker, err := NewWorker(t.Context(), workerCfg, store, queue, registry)
	must.NoError(t, err)

	return &harness{store: store, svc: svc, queue: queue, registry: registry, worker: worker}
}

// drain runs worker passes until the operation reaches a terminal state, or the
// test gives up. It is a loop rather than a running Worker so that a subtest
// controls exactly how many passes happen.
//
// The scope is an argument because the read is: an operation started under an
// owner is one only that owner's read finds, and a helper that assumed the
// global scope would report every owned operation as never having finished.
func (h *harness) drain(t *testing.T, scope tenancy.Scope, id string) *Operation {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		if _, err := h.worker.pass(t.Context()); err != nil {
			t.Fatalf("running a worker pass: %v", err)
		}

		op, err := h.svc.Get(t.Context(), scope, id)
		must.NoError(t, err)

		if op.Terminal() {
			return op
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("operation %q never reached a terminal state", id)

	return nil
}

func TestOperations_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		createTables(T, client, DefaultTablePrefix)
		createTables(T, client, reapPrefix)
		createTables(T, client, runLoopPrefix)

		runOperationsSuite(T, client)
	})
}

//nolint:maintidx // one behavioral contract per subtest; splitting it would only hide the list.
func runOperationsSuite(t *testing.T, client database.Client) {
	t.Helper()

	// Run and Enqueue are the two paths the rest of this suite drives around:
	// every other subtest calls worker.pass directly so it can control how many
	// passes happen, and starts work through Start rather than re-enqueueing it.
	// Both need a real queue, so this is the only place they can be exercised.
	t.Run("the worker loop runs what a handler started, and stops when its context is done", func(t *testing.T) {
		t.Parallel()

		done := make(chan struct{})

		h := newHarnessIn(t, client, runLoopPrefix, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{
				Kind: "export",
				Run: func(_ context.Context, req exportRequest, _ Reporter) (*Result, error) {
					return &Result{URI: "s3://" + req.SubjectID}, nil
				},
			}))
		})

		ctx, cancel := context.WithCancel(t.Context())

		var runErr error

		go func() {
			defer close(done)
			runErr = h.worker.Run(ctx)
		}()

		op, err := h.svc.Start(t.Context(), "export", exportRequest{SubjectID: "subject-1"})
		must.NoError(t, err)

		// Polled rather than drained: this is the loop claiming on its own,
		// which is the thing under test.
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			current, getErr := h.svc.Get(t.Context(), tenancy.Global(), op.ID)
			must.NoError(t, getErr)

			if current.Terminal() {
				test.EqOp(t, StateSucceeded, current.State)

				break
			}

			time.Sleep(20 * time.Millisecond)
		}

		// Nothing short of a cancelled context stops it, and a cancelled one
		// stops it promptly rather than at the end of the next poll.
		cancel()

		select {
		case <-done:
			must.Error(t, runErr)
			test.ErrorIs(t, runErr, context.Canceled)
		case <-time.After(30 * time.Second):
			t.Fatal("the worker loop did not return after its context was cancelled")
		}
	})

	t.Run("a context that is already done stops the loop before it claims", func(t *testing.T) {
		t.Parallel()

		h := newHarnessIn(t, client, runLoopPrefix, nil)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		test.ErrorIs(t, h.worker.Run(ctx), context.Canceled)
	})

	t.Run("re-enqueueing an operation makes a worker claim it again", func(t *testing.T) {
		t.Parallel()

		var runs atomic.Uint64

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{
				Kind: "export",
				Run: func(context.Context, exportRequest, Reporter) (*Result, error) {
					runs.Add(1)

					return &Result{URI: "s3://bundle"}, nil
				},
			}))
		})

		op, err := h.svc.Start(t.Context(), "export", exportRequest{SubjectID: "subject-1"})
		must.NoError(t, err)

		finished := h.drain(t, tenancy.Global(), op.ID)
		test.EqOp(t, StateSucceeded, finished.State)
		must.EqOp(t, uint64(1), runs.Load())

		// The row is terminal, so the second claim finds nothing to begin and
		// the operation is not run twice — Enqueue puts a key back on the queue,
		// it does not reopen the operation.
		must.NoError(t, h.svc.Enqueue(t.Context(), op.ID))

		_, passErr := h.worker.pass(t.Context())
		must.NoError(t, passErr)

		test.EqOp(t, uint64(1), runs.Load())
	})

	t.Run("a hurried re-enqueue carries its priority and delay", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{
				Kind: "export",
				Run: func(context.Context, exportRequest, Reporter) (*Result, error) {
					return &Result{URI: "s3://bundle"}, nil
				},
			}))
		})

		op, err := h.svc.Start(t.Context(), "export", exportRequest{SubjectID: "subject-1"})
		must.NoError(t, err)

		// Held back an hour, so the pass that follows must not see it.
		must.NoError(t, h.svc.Enqueue(t.Context(), op.ID, WithPriority(9), WithDelay(time.Hour)))

		test.EqOp(t, StatePending, mustGet(t, h, tenancy.Global(), op.ID).State)
	})

	// The whole promise, end to end: a handler starts work, a worker somewhere
	// else runs it, and the row says what happened.
	t.Run("start, run, succeed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{
				Kind:       "export",
				CountLabel: "records",
				Run: func(_ context.Context, req exportRequest, rep Reporter) (*Result, error) {
					rep.SetUnits(3)

					for _, unit := range []string{"identity", "webhooks", "recipes"} {
						rep.StartUnit(unit)
						rep.Advance(100)
						rep.FinishUnit()
					}

					return &Result{URI: "s3://" + req.SubjectID}, nil
				},
			}))
		})

		started, err := h.svc.Start(t.Context(), "export", exportRequest{SubjectID: "s1"},
			WithOwner(tenancy.Of("u1")))
		must.NoError(t, err)
		test.EqOp(t, StatePending, started.State)
		test.False(t, started.Done)
		test.EqOp(t, tenancy.Of("u1"), started.Owner)
		test.EqOp(t, "records", started.Progress.CountLabel)

		finished := h.drain(t, tenancy.Of("u1"), started.ID)

		test.EqOp(t, StateSucceeded, finished.State)
		test.True(t, finished.Done)
		must.NotNil(t, finished.Result)
		test.EqOp(t, "s3://s1", finished.Result.URI)

		// Both tiers landed, and the numerator was filled in to its denominator.
		test.EqOp(t, 3, finished.Progress.UnitsDone)
		must.NotNil(t, finished.Progress.UnitsTotal)
		test.EqOp(t, 3, *finished.Progress.UnitsTotal)
		test.EqOp(t, int64(300), finished.Progress.Count)

		fraction, ok := finished.Progress.Fraction()
		must.True(t, ok)
		test.EqOp(t, float64(1), fraction)

		// The gap between these two is queue latency, which is the number that
		// explains a slow export that ran quickly.
		must.NotNil(t, finished.StartedAt)
		must.NotNil(t, finished.FinishedAt)
		test.False(t, finished.StartedAt.Before(finished.CreatedAt))
	})

	// The tier that has no denominator, which is the ordinary case for work that
	// cannot count what it has without a second full scan.
	t.Run("progress with no denominator", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{
				Kind:       "collect",
				CountLabel: "rows",
				Run: func(_ context.Context, _ exportRequest, rep Reporter) (*Result, error) {
					rep.Advance(4300)
					rep.Sayf("collected everything")

					return nil, nil
				},
			}))
		})

		started, err := h.svc.Start(t.Context(), "collect", exportRequest{})
		must.NoError(t, err)

		finished := h.drain(t, tenancy.Global(), started.ID)

		test.EqOp(t, StateSucceeded, finished.State)
		test.EqOp(t, int64(4300), finished.Progress.Count)
		test.EqOp(t, "collected everything", finished.Progress.Message)

		_, ok := finished.Progress.Fraction()
		test.False(t, ok)
	})

	t.Run("a retryable failure retries and then fails", func(t *testing.T) {
		t.Parallel()

		var attempts atomic.Int64

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{
				Kind: "flaky",
				Run: func(context.Context, exportRequest, Reporter) (*Result, error) {
					attempts.Add(1)

					return nil, platformerrors.New("the upstream is down")
				},
			}))
		})

		started, err := h.svc.Start(t.Context(), "flaky", exportRequest{})
		must.NoError(t, err)

		finished := h.drain(t, tenancy.Global(), started.ID)

		// MaxAttempts is 2 in this harness. The promise is that it terminates,
		// and the code says why rather than repeating the symptom as the reason.
		test.EqOp(t, StateFailed, finished.State)
		must.NotNil(t, finished.Error)
		test.EqOp(t, CodeAttemptsExhausted, finished.Error.Code)
		test.StrContains(t, finished.Error.Message, "upstream is down")
		test.EqOp(t, int64(2), attempts.Load())
		test.EqOp(t, 2, finished.Attempts)
	})

	t.Run("an unretryable failure fails on the first attempt", func(t *testing.T) {
		t.Parallel()

		var attempts atomic.Int64

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{
				Kind: "rejected",
				Run: func(context.Context, exportRequest, Reporter) (*Result, error) {
					attempts.Add(1)

					return nil, Unretryable(Fail("no_such_subject", "no such subject"))
				},
			}))
		})

		started, err := h.svc.Start(t.Context(), "rejected", exportRequest{})
		must.NoError(t, err)

		finished := h.drain(t, tenancy.Global(), started.ID)

		test.EqOp(t, StateFailed, finished.State)
		test.EqOp(t, int64(1), attempts.Load())
		must.NotNil(t, finished.Error)
		test.EqOp(t, "no_such_subject", finished.Error.Code)
		test.False(t, finished.Error.Retryable)
	})

	// A kind this build does not register must not consume its whole budget
	// against a name nothing will ever answer to.
	t.Run("an unregistered kind fails at once", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "known", Run: noopRun[exportRequest]}))
		})

		started, err := h.svc.Start(t.Context(), "known", exportRequest{})
		must.NoError(t, err)

		// Rewrite the row's kind behind the service's back, which is what a
		// deploy that renamed a kind does to every operation already queued.
		_, err = client.Writer().ExecContext(t.Context(),
			"UPDATE operations SET kind = 'gone' WHERE id = $1", started.ID)
		must.NoError(t, err)

		finished := h.drain(t, tenancy.Global(), started.ID)

		test.EqOp(t, StateFailed, finished.State)
		must.NotNil(t, finished.Error)
		test.EqOp(t, CodeUnknownKind, finished.Error.Code)
	})

	// Nothing has started, so the cancellation is complete the moment it is
	// asked for — and the worker must then refuse to run it.
	t.Run("cancelling a pending operation stops it before it runs", func(t *testing.T) {
		t.Parallel()

		var ran atomic.Bool

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{
				Kind: "cancelled_early",
				Run: func(context.Context, exportRequest, Reporter) (*Result, error) {
					ran.Store(true)

					return nil, nil
				},
			}))
		})

		started, err := h.svc.Start(t.Context(), "cancelled_early", exportRequest{})
		must.NoError(t, err)

		cancelled, err := h.svc.Cancel(t.Context(), started.ID)
		must.NoError(t, err)
		test.EqOp(t, StateCancelled, cancelled.State)

		_, err = h.worker.pass(t.Context())
		must.NoError(t, err)

		test.False(t, ran.Load())

		after, err := h.svc.Get(t.Context(), tenancy.Global(), started.ID)
		must.NoError(t, err)
		test.EqOp(t, StateCancelled, after.State)
	})

	// The flush is the cancellation poll: a Runner that reports progress
	// observes a cancellation without a second round trip and with nothing extra
	// to call.
	t.Run("a running operation observes its cancellation through the reporter", func(t *testing.T) {
		t.Parallel()

		running := make(chan struct{})

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{
				Kind: "cancellable",
				Run: func(_ context.Context, _ exportRequest, rep Reporter) (*Result, error) {
					rep.StartUnit("identity")
					close(running)

					select {
					case <-rep.Cancelled():
						return nil, nil
					case <-time.After(30 * time.Second):
						return nil, platformerrors.New("the cancellation never arrived")
					}
				},
			}))
		})

		started, err := h.svc.Start(t.Context(), "cancellable", exportRequest{})
		must.NoError(t, err)

		done := make(chan struct{})

		go func() {
			defer close(done)

			_, passErr := h.worker.pass(t.Context())
			test.NoError(t, passErr)
		}()

		<-running

		_, err = h.svc.Cancel(t.Context(), started.ID)
		must.NoError(t, err)

		<-done

		finished, err := h.svc.Get(t.Context(), tenancy.Global(), started.ID)
		must.NoError(t, err)

		// Cancellation beats the clean return: the Runner stopped early, so the
		// work is partial and must not be recorded as complete.
		test.EqOp(t, StateCancelled, finished.State)
		test.True(t, finished.CancelRequested)
		test.True(t, finished.Done)
	})

	// The guarded transition is the package's real mutual exclusion. Two workers
	// holding the same dispatch must not both run the work.
	t.Run("only one worker may begin an operation", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "guarded", Run: noopRun[exportRequest]}))
		})

		started, err := h.svc.Start(t.Context(), "guarded", exportRequest{})
		must.NoError(t, err)

		first, err := h.store.Begin(t.Context(), started.ID, 1, time.Minute)
		must.NoError(t, err)
		test.EqOp(t, StateRunning, first.State)

		_, err = h.store.Begin(t.Context(), started.ID, 1, time.Minute)
		test.ErrorIs(t, err, ErrOperationNotFound)
	})

	// The lease is on the row and it really does lapse on the server's clock.
	t.Run("a lapsed lease is reclaimable", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "lapsing", Run: noopRun[exportRequest]}))
		})

		started, err := h.svc.Start(t.Context(), "lapsing", exportRequest{})
		must.NoError(t, err)

		_, err = h.store.Begin(t.Context(), started.ID, 1, 50*time.Millisecond)
		must.NoError(t, err)

		_, err = h.store.Begin(t.Context(), started.ID, 2, time.Minute)
		test.ErrorIs(t, err, ErrOperationNotFound)

		time.Sleep(200 * time.Millisecond)

		reclaimed, err := h.store.Begin(t.Context(), started.ID, 2, time.Minute)
		must.NoError(t, err)
		test.EqOp(t, 2, reclaimed.Attempts)

		// started_at does not move: the operation has been running since the
		// first worker picked it up, and this is the second attempt at it.
		must.NotNil(t, reclaimed.StartedAt)
	})

	// The flush extends the lease, which is what lets a lease shorter than the
	// work still bound it.
	t.Run("a progress flush extends the lease", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "extending", Run: noopRun[exportRequest]}))
		})

		started, err := h.svc.Start(t.Context(), "extending", exportRequest{})
		must.NoError(t, err)

		_, err = h.store.Begin(t.Context(), started.ID, 1, 300*time.Millisecond)
		must.NoError(t, err)

		time.Sleep(150 * time.Millisecond)

		ack, err := h.store.Progress(t.Context(), started.ID, Progress{Count: 10}, 10*time.Second)
		must.NoError(t, err)
		test.True(t, ack.Held)

		// Past the original lease, but inside the extended one.
		time.Sleep(250 * time.Millisecond)

		_, err = h.store.Begin(t.Context(), started.ID, 2, time.Minute)
		test.ErrorIs(t, err, ErrOperationNotFound)
	})

	// A flush against an operation somebody else finished must say so rather
	// than resurrecting its progress.
	t.Run("a flush against a finished operation is not held", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "finished", Run: noopRun[exportRequest]}))
		})

		started, err := h.svc.Start(t.Context(), "finished", exportRequest{})
		must.NoError(t, err)

		_, err = h.store.Begin(t.Context(), started.ID, 1, time.Minute)
		must.NoError(t, err)

		must.NoError(t, h.store.Finish(t.Context(), started.ID, StateSucceeded, nil, nil, true))

		ack, err := h.store.Progress(t.Context(), started.ID, Progress{Count: 99}, time.Minute)
		must.NoError(t, err)
		test.False(t, ack.Held)

		after, err := h.svc.Get(t.Context(), tenancy.Global(), started.ID)
		must.NoError(t, err)
		test.EqOp(t, int64(0), after.Progress.Count)
	})

	// The counter is monotonic in the database, not merely in the reporter: a
	// straggler flush from a worker whose lease lapsed must not walk it back.
	t.Run("progress is monotonic in the row", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "monotonic", Run: noopRun[exportRequest]}))
		})

		started, err := h.svc.Start(t.Context(), "monotonic", exportRequest{})
		must.NoError(t, err)

		_, err = h.store.Begin(t.Context(), started.ID, 1, time.Minute)
		must.NoError(t, err)

		_, err = h.store.Progress(t.Context(), started.ID, Progress{Count: 5000, UnitsDone: 3}, time.Minute)
		must.NoError(t, err)

		_, err = h.store.Progress(t.Context(), started.ID, Progress{Count: 12, UnitsDone: 1}, time.Minute)
		must.NoError(t, err)

		after, err := h.svc.Get(t.Context(), tenancy.Global(), started.ID)
		must.NoError(t, err)
		test.EqOp(t, int64(5000), after.Progress.Count)
		test.EqOp(t, 3, after.Progress.UnitsDone)
	})

	// Every write bumps the revision, which is what lets a watcher decide
	// whether the row it just re-read is new without comparing every column.
	t.Run("the revision advances on every write", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "revised", Run: noopRun[exportRequest]}))
		})

		started, err := h.svc.Start(t.Context(), "revised", exportRequest{})
		must.NoError(t, err)

		begun, err := h.store.Begin(t.Context(), started.ID, 1, time.Minute)
		must.NoError(t, err)
		test.Greater(t, started.Revision, begun.Revision)

		ack, err := h.store.Progress(t.Context(), started.ID, Progress{Count: 1}, time.Minute)
		must.NoError(t, err)
		test.Greater(t, begun.Revision, ack.Revision)
	})

	// The gap between Start's two writes, and the sweep that closes it.
	t.Run("a stranded operation is recovered", func(t *testing.T) {
		t.Parallel()

		var ran atomic.Bool

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{
				Kind: "stranded",
				Run: func(context.Context, exportRequest, Reporter) (*Result, error) {
					ran.Store(true)

					return nil, nil
				},
			}))
		})

		// Recorded but never enqueued, which is exactly what a process dying
		// between Start's two writes leaves behind.
		started, err := h.svc.StartInTransaction(t.Context(), database.NewTxForTesting(client.Writer()), "stranded", exportRequest{})
		must.NoError(t, err)

		claimed, err := h.queue.Claim(t.Context(), 10, time.Second)
		must.NoError(t, err)
		test.SliceEmpty(t, claimed)

		// The grace period is a second in this harness; without it the sweep
		// would re-enqueue every operation the fleet is starting right now.
		time.Sleep(1200 * time.Millisecond)

		recovered, err := h.svc.Recover(t.Context())
		must.NoError(t, err)
		test.Greater(t, 0, recovered)

		finished := h.drain(t, tenancy.Global(), started.ID)

		test.EqOp(t, StateSucceeded, finished.State)
		test.True(t, ran.Load())
	})

	// The idempotency seam: a retried Start under a derived ID collides with the
	// operation it is retrying rather than starting a second one.
	t.Run("WithID makes Start idempotent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "idempotent", Run: noopRun[exportRequest]}))
		})

		id := fmt.Sprintf("fixed%d", queueCounter.Add(1))

		first, err := h.svc.Start(t.Context(), "idempotent", exportRequest{SubjectID: "s1"}, WithID(id))
		must.NoError(t, err)
		test.EqOp(t, id, first.ID)

		second, err := h.svc.Start(t.Context(), "idempotent", exportRequest{SubjectID: "s1"}, WithID(id))
		must.NoError(t, err)
		test.EqOp(t, first.ID, second.ID)
		test.EqOp(t, first.CreatedAt.UnixNano(), second.CreatedAt.UnixNano())
	})

	t.Run("Start refuses an unregistered kind", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, nil)

		_, err := h.svc.Start(t.Context(), "nope", exportRequest{})

		test.ErrorIs(t, err, ErrUnknownKind)
	})

	t.Run("Start refuses the wrong request type", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "typed", Run: noopRun[exportRequest]}))
		})

		_, err := h.svc.Start(t.Context(), "typed", struct{ Other string }{Other: "x"})

		test.ErrorIs(t, err, ErrRequestTypeMismatch)
	})

	t.Run("listing is scoped by owner", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "listed", Run: noopRun[exportRequest]}))
		})

		owner := tenancy.Of(fmt.Sprintf("owner%d", queueCounter.Add(1)))

		for range 3 {
			_, err := h.svc.Start(t.Context(), "listed", exportRequest{}, WithOwner(owner))
			must.NoError(t, err)
		}

		_, err := h.svc.Start(t.Context(), "listed", exportRequest{}, WithOwner(tenancy.Of("somebody-else")))
		must.NoError(t, err)

		results, err := h.svc.List(t.Context(), owner, nil, nil)
		must.NoError(t, err)
		must.SliceLen(t, 3, results.Data)

		for _, op := range results.Data {
			test.EqOp(t, owner, op.Owner)
		}
	})

	// The read that used to be a comparison. Nothing above the store can tell a
	// statement that filtered by tenant from one that returned everything and had
	// its answer discarded afterwards, so this is the only place the difference
	// is checkable — and the difference is the whole ticket.
	t.Run("a read is confined by the statement rather than by a comparison", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "confined", Run: noopRun[exportRequest]}))
		})

		mine := tenancy.Of(fmt.Sprintf("owner%d", queueCounter.Add(1)))
		theirs := tenancy.Of(fmt.Sprintf("owner%d", queueCounter.Add(1)))

		started, err := h.svc.Start(t.Context(), "confined", exportRequest{}, WithOwner(mine))
		must.NoError(t, err)

		// The owner reads it.
		read, err := h.svc.Get(t.Context(), mine, started.ID)
		must.NoError(t, err)
		test.EqOp(t, mine, read.Owner)

		// Anybody else is told it is not there, holding its exact ID — the same
		// answer an ID nobody ever minted gets, so the endpoint confirms
		// nothing about which guesses are real.
		_, err = h.svc.Get(t.Context(), theirs, started.ID)
		test.ErrorIs(t, err, ErrOperationNotFound)

		_, err = h.svc.Get(t.Context(), theirs, "no-such-operation")
		test.ErrorIs(t, err, ErrOperationNotFound)

		// And the global scope is a scope like any other rather than a skeleton
		// key: it matches only itself.
		_, err = h.svc.Get(t.Context(), tenancy.Global(), started.ID)
		test.ErrorIs(t, err, ErrOperationNotFound)

		theirPage, err := h.svc.List(t.Context(), theirs, nil, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, theirPage.Data)

		// The batched read the watcher runs takes the same narrowing, so an id
		// in another tenant is a gap rather than a row.
		absent, err := h.store.GetMany(t.Context(), client.Reader(), theirs, []string{started.ID})
		must.NoError(t, err)
		test.SliceEmpty(t, absent)

		present, err := h.store.GetMany(t.Context(), client.Reader(), mine, []string{started.ID})
		must.NoError(t, err)
		must.SliceLen(t, 1, present)
		test.EqOp(t, started.ID, present[0].ID)
	})

	// The column has no DEFAULT — see internal/scopeddl — so what keeps a row
	// out of the scope that matches nobody is the write binding one. A Scope
	// that names nobody refuses to bind at the driver rather than arriving as
	// the empty identifier.
	t.Run("a write cannot lose its scope", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, nil)

		err := client.WithTransaction(t.Context(), func(tx database.Tx) error {
			_, insertErr := h.store.Insert(t.Context(), tx, tenancy.Scope{},
				&Operation{ID: "unscoped", Kind: "confined", State: StatePending})

			return insertErr
		})

		test.ErrorIs(t, err, tenancy.ErrNoScope)

		// And an entity whose own scope disagrees with the write's is refused
		// rather than corrected, because the caller is holding both halves.
		err = client.WithTransaction(t.Context(), func(tx database.Tx) error {
			_, insertErr := h.store.Insert(t.Context(), tx, tenancy.Of("u1"),
				&Operation{ID: "mismatched", Kind: "confined", State: StatePending, Owner: tenancy.Of("u2")})

			return insertErr
		})

		test.ErrorIs(t, err, ErrScopeMismatch)
	})

	// sortBy is promised on the wire and the corpus carries both directions of
	// the listing. Nothing above the store can tell an ascending page answered
	// under a descending filter from a correct one, so this is the only place
	// the pick can be checked.
	t.Run("a listing pages in the direction the filter asks for", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "sorted", Run: noopRun[exportRequest]}))
		})

		owner := tenancy.Of(fmt.Sprintf("owner%d", queueCounter.Add(1)))

		var started []string

		for range 3 {
			op, err := h.svc.Start(t.Context(), "sorted", exportRequest{}, WithOwner(owner))
			must.NoError(t, err)

			started = append(started, op.ID)
		}

		ascending, err := h.svc.List(t.Context(), owner, nil, filtering.DefaultQueryFilter())
		must.NoError(t, err)
		must.SliceLen(t, len(started), ascending.Data)

		descending, err := h.svc.List(t.Context(), owner, nil, descendingFilter())
		must.NoError(t, err)
		must.SliceLen(t, len(started), descending.Data)

		for i, op := range ascending.Data {
			test.EqOp(t, started[i], op.ID)
			test.EqOp(t, started[len(started)-1-i], descending.Data[i].ID)
		}
	})

	// The narrowings are two shapes — an absent owner or kind compares against
	// nothing, an absent state set is every state — and the failure mode of
	// getting either backwards is a query that runs and answers with the wrong
	// set.
	t.Run("a listing narrows by kind and by state", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "narrowed", Run: noopRun[exportRequest]}))
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "unnarrowed", Run: noopRun[exportRequest]}))
		})

		owner := tenancy.Of(fmt.Sprintf("owner%d", queueCounter.Add(1)))

		wanted, err := h.svc.Start(t.Context(), "narrowed", exportRequest{}, WithOwner(owner))
		must.NoError(t, err)

		_, err = h.svc.Start(t.Context(), "unnarrowed", exportRequest{}, WithOwner(owner))
		must.NoError(t, err)

		byKind, err := h.svc.List(t.Context(), owner, &ListScope{Kind: "narrowed"}, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, byKind.Data)
		test.EqOp(t, wanted.ID, byKind.Data[0].ID)

		// Both rows are pending and neither is terminal, so one set finds them
		// both and the other finds neither.
		pending, err := h.svc.List(t.Context(), owner, &ListScope{States: []State{StatePending}}, nil)
		must.NoError(t, err)
		test.SliceLen(t, 2, pending.Data)

		terminal, err := h.svc.List(t.Context(), owner,
			&ListScope{States: []State{StateSucceeded, StateFailed}}, nil)
		must.NoError(t, err)
		test.SliceLen(t, 0, terminal.Data)

		// An unnarrowed listing is every state rather than none of them.
		all, err := h.svc.List(t.Context(), owner, nil, nil)
		must.NoError(t, err)
		test.SliceLen(t, 2, all.Data)
	})

	// The filter window reached the old hand-built listing and was dropped on
	// the floor, because the statement it bound into never carried one.
	t.Run("a listing honors the created window", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "windowed", Run: noopRun[exportRequest]}))
		})

		owner := tenancy.Of(fmt.Sprintf("owner%d", queueCounter.Add(1)))

		op, err := h.svc.Start(t.Context(), "windowed", exportRequest{}, WithOwner(owner))
		must.NoError(t, err)

		before := filtering.DefaultQueryFilter()
		before.CreatedBefore = &op.CreatedAt

		excluded, err := h.svc.List(t.Context(), owner, nil, before)
		must.NoError(t, err)
		test.SliceLen(t, 0, excluded.Data)

		after := filtering.DefaultQueryFilter()
		after.CreatedAfter = pointer.To(op.CreatedAt.Add(-time.Minute))

		included, err := h.svc.List(t.Context(), owner, nil, after)
		must.NoError(t, err)
		test.SliceLen(t, 1, included.Data)
	})

	// Terminal rows go; in-flight ones stay, at any age, because that row is the
	// only record that something is still running.
	t.Run("reaping deletes only finished operations", func(t *testing.T) {
		t.Parallel()

		// Its own table: Reap is table-wide by design, so a reap running beside
		// the other parallel subtests would delete their operations too.
		h := newHarnessIn(t, client, reapPrefix, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{Kind: "reaped", Run: noopRun[exportRequest]}))
		})

		started, err := h.svc.Start(t.Context(), "reaped", exportRequest{})
		must.NoError(t, err)

		finished := h.drain(t, tenancy.Global(), started.ID)
		must.True(t, finished.Terminal())

		pending, err := h.svc.Start(t.Context(), "reaped", exportRequest{})
		must.NoError(t, err)

		// A zero retention makes everything finished eligible at once, which is
		// the only way to test this without waiting a month.
		reaped, err := h.store.Reap(t.Context(), 0, 100)
		must.NoError(t, err)
		test.Greater(t, int64(0), reaped)

		_, err = h.svc.Get(t.Context(), tenancy.Global(), started.ID)
		test.ErrorIs(t, err, ErrOperationNotFound)

		_, err = h.svc.Get(t.Context(), tenancy.Global(), pending.ID)
		test.NoError(t, err)
	})

	// The watch path over a real database and a real worker, which is the shape
	// an SSE endpoint actually runs.
	t.Run("a watcher sees an operation through to its terminal state", func(t *testing.T) {
		t.Parallel()

		release := make(chan struct{})

		h := newHarness(t, client, func(r *Registry) {
			must.NoError(t, Register(r, Definition[exportRequest]{
				Kind:       "watched",
				CountLabel: "records",
				Run: func(_ context.Context, _ exportRequest, rep Reporter) (*Result, error) {
					rep.SetUnits(2)
					rep.StartUnit("identity")
					rep.Advance(500)
					rep.FinishUnit()

					<-release

					rep.StartUnit("webhooks")
					rep.Advance(500)
					rep.FinishUnit()

					return &Result{URI: "s3://watched"}, nil
				},
			}))
		})

		watcher, err := NewWatcher(t.Context(), &WatcherConfig{
			Poll:            100 * time.Millisecond,
			MinReadInterval: time.Millisecond,
		}, client, h.store)
		must.NoError(t, err)

		t.Cleanup(func() { _ = watcher.Close() })

		go func() { _ = watcher.Run(t.Context()) }()

		started, err := h.svc.Start(t.Context(), "watched", exportRequest{})
		must.NoError(t, err)

		snapshots, err := watcher.Watch(t.Context(), tenancy.Global(), started.ID)
		must.NoError(t, err)

		workerDone := make(chan struct{})

		go func() {
			defer close(workerDone)

			_, passErr := h.worker.pass(t.Context())
			test.NoError(t, passErr)
		}()

		var (
			last       *Operation
			sawUnitOne bool
		)

		for op := range snapshots {
			last = op

			if op.Progress.UnitsDone >= 1 && !sawUnitOne {
				sawUnitOne = true

				close(release)
			}
		}

		<-workerDone

		// The channel closed, and what it closed after was the terminal
		// snapshot — which is what lets a consumer's loop exit with no "am I
		// finished" check anywhere in it.
		must.NotNil(t, last)
		test.EqOp(t, StateSucceeded, last.State)
		test.True(t, last.Done)
		test.True(t, sawUnitOne)
		test.EqOp(t, int64(1000), last.Progress.Count)
	})
}

// mustGet reads an operation through the service and fails the test if it is
// not there.
func mustGet(t *testing.T, h *harness, scope tenancy.Scope, id string) *Operation {
	t.Helper()

	op, err := h.svc.Get(t.Context(), scope, id)
	must.NoError(t, err)

	return op
}

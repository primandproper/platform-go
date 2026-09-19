package workqueue

import (
	"context"
	stderrors "errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/workqueue/internal/workqueuedb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runnerConfig is a config whose timings are short enough to run a pass inside a
// test and still satisfy the cross-field rule between Lease and ExtendInterval.
func runnerConfig() *RunnerConfig {
	return &RunnerConfig{
		Poll:           10 * time.Millisecond,
		Lease:          time.Second,
		ExtendInterval: 100 * time.Millisecond,
		RetryDelay:     time.Minute,
		Batch:          10,
		Concurrency:    4,
	}
}

// newTestRunner builds a runner over a stubbed queue.
func newTestRunner(t *testing.T, mutate func(*RunnerConfig), handler Handler[string]) (*Runner[string], *stubQuerier) {
	t.Helper()

	queue, stub := stubbedQueue(t)

	cfg := runnerConfig()
	if mutate != nil {
		mutate(cfg)
	}

	r, err := NewRunner(t.Context(), cfg, queue, handler)
	must.NoError(t, err)

	return r, stub
}

func TestNewRunner(T *testing.T) {
	T.Parallel()

	T.Run("builds a runner and defaults its knobs", func(t *testing.T) {
		t.Parallel()

		queue, _ := stubbedQueue(t)

		r, err := NewRunner(t.Context(), &RunnerConfig{}, queue,
			func(context.Context, Item[string]) error { return nil })
		must.NoError(t, err)

		test.EqOp(t, DefaultRunnerPoll, r.cfg.Poll)
		test.EqOp(t, DefaultRunnerLease, r.cfg.Lease)
		test.EqOp(t, DefaultRunnerBatch, r.cfg.Batch)
		test.EqOp(t, DefaultRunnerConcurrency, r.cfg.Concurrency)

		// Derived from the lease rather than taken from a constant, so a
		// shortened lease shortens the heartbeat that keeps it alive.
		test.EqOp(t, DefaultRunnerLease/DefaultRunnerExtendDivisor, r.cfg.ExtendInterval)
	})

	// A runner with nothing to call would claim every item and complete it,
	// which looks exactly like a working deployment right up until somebody asks
	// why none of the work was done.
	T.Run("rejects the inputs it cannot work without", func(t *testing.T) {
		t.Parallel()

		queue, _ := stubbedQueue(t)
		handler := func(context.Context, Item[string]) error { return nil }

		_, err := NewRunner(t.Context(), nil, queue, handler)
		test.ErrorIs(t, err, ErrNilConfig)

		_, err = NewRunner[string](t.Context(), &RunnerConfig{}, nil, handler)
		test.ErrorIs(t, err, ErrNilQueue)

		_, err = NewRunner[string](t.Context(), &RunnerConfig{}, queue, nil)
		test.ErrorIs(t, err, ErrNilHandler)
	})

	// A runner asking for more than the queue will ever hand it is a supported
	// configuration — Claim clamps rather than rejects — and it is the one where
	// the loop's own arithmetic goes wrong quietly. Against the declared number
	// no pass is ever full, so the fast path never fires and a backlog is paced
	// at one ceiling per Poll with nothing reporting it.
	T.Run("lowers a batch above the queue's claim ceiling", func(t *testing.T) {
		t.Parallel()

		queue, _ := stubbedQueue(t)
		queue.cfg.MaxClaimBatch = 3

		cfg := runnerConfig()
		cfg.Batch = 10

		r, err := NewRunner(t.Context(), cfg, queue,
			func(context.Context, Item[string]) error { return nil })
		must.NoError(t, err)

		// The declared number is kept as declared — it is the operator's
		// statement of intent, and a config a constructor rewrote is a config
		// nobody can read back.
		test.EqOp(t, 10, r.cfg.Batch)

		// What the loop reasons about is what it will be handed.
		test.EqOp(t, 3, r.batch)
	})

	T.Run("leaves a batch inside the ceiling alone", func(t *testing.T) {
		t.Parallel()

		queue, _ := stubbedQueue(t)
		queue.cfg.MaxClaimBatch = 50

		cfg := runnerConfig()
		cfg.Batch = 10

		r, err := NewRunner(t.Context(), cfg, queue,
			func(context.Context, Item[string]) error { return nil })
		must.NoError(t, err)

		test.EqOp(t, 10, r.batch)
	})

	// A full pass is what sends the loop straight round again, so the comparison
	// that decides it has to be against the number the queue will actually
	// produce. This is that property end to end: a ceiling below the declared
	// batch, a Poll long enough that taking it would be indistinguishable from
	// hanging, and a second claim arriving anyway.
	T.Run("a pass full at the ceiling goes straight round again", func(t *testing.T) {
		t.Parallel()

		queue, stub := stubbedQueue(t)
		queue.cfg.MaxClaimBatch = 2

		cfg := runnerConfig()
		cfg.Batch = 10
		// An hour, so that a Poll between passes cannot be mistaken for the fast
		// path. Against the declared batch of ten, a pass of two reads as short,
		// this Poll is taken, and the second claim never arrives inside the
		// deadline below.
		cfg.Poll = time.Hour

		var claims atomic.Int64

		stub.claim = func(workqueuedb.ClaimDueItemsParams) ([]workqueuedb.ClaimDueItemsRow, error) {
			if claims.Add(1) == 1 {
				return claimRows("first", "second"), nil
			}

			return nil, nil
		}

		r, err := NewRunner(t.Context(), cfg, queue,
			func(context.Context, Item[string]) error { return nil })
		must.NoError(t, err)

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		test.ErrorIs(t, r.Run(ctx), context.DeadlineExceeded)

		// Two: the full one, and the empty one the fast path went straight round
		// to. The third never comes, because that one did take the Poll.
		test.EqOp(t, int64(2), claims.Load())
	})

	// The extension is the only thing keeping a long handler's claim alive, so an
	// interval that does not fit inside the lease twice means every slow item is
	// reclaimed between ticks — the precise failure the extension removes.
	T.Run("rejects an extend interval the lease cannot cover", func(t *testing.T) {
		t.Parallel()

		queue, _ := stubbedQueue(t)

		cfg := runnerConfig()
		cfg.Lease = time.Second
		cfg.ExtendInterval = 900 * time.Millisecond

		_, err := NewRunner(t.Context(), cfg, queue,
			func(context.Context, Item[string]) error { return nil })
		test.Error(t, err)
	})
}

func TestRunner_Pass(T *testing.T) {
	T.Parallel()

	T.Run("completes what the handler handled, in one statement", func(t *testing.T) {
		t.Parallel()

		var handled atomic.Int64

		r, stub := newTestRunner(t, nil, func(context.Context, Item[string]) error {
			handled.Add(1)

			return nil
		})

		stub.claim = func(workqueuedb.ClaimDueItemsParams) ([]workqueuedb.ClaimDueItemsRow, error) {
			return claimRows("a", "b", "c"), nil
		}

		claimed, err := r.pass(t.Context())
		must.NoError(t, err)
		test.EqOp(t, 3, claimed)
		test.EqOp(t, int64(3), handled.Load())

		completed, released, _ := stub.calls()
		must.SliceLen(t, 1, completed)
		test.Eq(t, []string{"a", "b", "c"}, completed[0].ItemKeys)
		test.SliceEmpty(t, released)
	})

	// The common failure is one dependency being down and taking the whole batch
	// with it, so a batch of identical failures costs one Release rather than
	// one per item — and two distinct failures still get their own reasons.
	T.Run("groups failures by what went wrong", func(t *testing.T) {
		t.Parallel()

		r, stub := newTestRunner(t, nil, func(_ context.Context, item Item[string]) error {
			if item.Key == "c" {
				return stderrors.New("disk full")
			}

			return stderrors.New("connection refused")
		})

		stub.claim = func(workqueuedb.ClaimDueItemsParams) ([]workqueuedb.ClaimDueItemsRow, error) {
			return claimRows("a", "b", "c"), nil
		}

		_, err := r.pass(t.Context())
		must.NoError(t, err)

		completed, released, _ := stub.calls()
		test.SliceEmpty(t, completed)
		must.SliceLen(t, 2, released)

		keysFor := func(cause string) []string {
			for i := range released {
				must.NotNil(t, released[i].LastError)

				if strings.Contains(*released[i].LastError, cause) {
					return released[i].ItemKeys
				}
			}

			return nil
		}

		test.Eq(t, []string{"a", "b"}, keysFor("connection refused"))
		test.Eq(t, []string{"c"}, keysFor("disk full"))

		// Held back rather than handed straight back: without the delay a failing
		// item comes right back and spins against whatever it failed on.
		for i := range released {
			test.EqOp(t, r.cfg.RetryDelay.Microseconds(), released[i].DelayMicroseconds)
		}
	})

	// One poisonous key must not take the loop down with it, and the row should
	// say what happened rather than only that something did.
	T.Run("contains a panicking handler and records it", func(t *testing.T) {
		t.Parallel()

		r, stub := newTestRunner(t, nil, func(_ context.Context, item Item[string]) error {
			if item.Key == "poison" {
				panic("nil map write")
			}

			return nil
		})

		stub.claim = func(workqueuedb.ClaimDueItemsParams) ([]workqueuedb.ClaimDueItemsRow, error) {
			return claimRows("fine", "poison"), nil
		}

		_, err := r.pass(t.Context())
		must.NoError(t, err)

		completed, released, _ := stub.calls()
		must.SliceLen(t, 1, completed)
		test.Eq(t, []string{"fine"}, completed[0].ItemKeys)

		must.SliceLen(t, 1, released)
		test.Eq(t, []string{"poison"}, released[0].ItemKeys)
		must.NotNil(t, released[0].LastError)
		test.StrContains(t, *released[0].LastError, ErrHandlerPanicked.Error())
		test.StrContains(t, *released[0].LastError, "nil map write")
	})

	T.Run("runs no more than Concurrency handlers at once", func(t *testing.T) {
		t.Parallel()

		var (
			mu      sync.Mutex
			running int
			highest int
		)

		r, stub := newTestRunner(t, func(cfg *RunnerConfig) { cfg.Concurrency = 2 },
			func(context.Context, Item[string]) error {
				mu.Lock()
				running++
				highest = max(highest, running)
				mu.Unlock()

				time.Sleep(10 * time.Millisecond)

				mu.Lock()
				running--
				mu.Unlock()

				return nil
			})

		stub.claim = func(workqueuedb.ClaimDueItemsParams) ([]workqueuedb.ClaimDueItemsRow, error) {
			return claimRows("a", "b", "c", "d", "e", "f"), nil
		}

		_, err := r.pass(t.Context())
		must.NoError(t, err)

		mu.Lock()
		defer mu.Unlock()

		test.LessEq(t, 2, highest)
	})

	// A claim this pass cannot even make is surfaced, so Run can log it and
	// sleep rather than spin.
	T.Run("surfaces what it could not claim", func(t *testing.T) {
		t.Parallel()

		sentinel := stderrors.New("connection refused by the test")

		r, stub := newTestRunner(t, nil, func(context.Context, Item[string]) error { return nil })

		stub.claim = func(workqueuedb.ClaimDueItemsParams) ([]workqueuedb.ClaimDueItemsRow, error) {
			return nil, sentinel
		}

		claimed, err := r.pass(t.Context())
		test.EqOp(t, 0, claimed)
		test.ErrorIs(t, err, sentinel)
	})
}

// A shutdown drains the batch it claimed rather than abandoning it: what was
// running is waited for and recorded, and what was never started is handed
// straight back so the next worker gets it now instead of after the lease.
func TestRunner_Pass_DrainsOnShutdown(T *testing.T) {
	T.Parallel()

	ctx, cancel := context.WithCancel(T.Context())
	defer cancel()

	var handled atomic.Int64

	r, stub := newTestRunner(T, func(cfg *RunnerConfig) { cfg.Concurrency = 1 },
		func(context.Context, Item[string]) error {
			handled.Add(1)
			cancel()

			return nil
		})

	stub.claim = func(workqueuedb.ClaimDueItemsParams) ([]workqueuedb.ClaimDueItemsRow, error) {
		return claimRows("first", "second", "third"), nil
	}

	claimed, err := r.pass(ctx)
	must.NoError(T, err)
	test.EqOp(T, 3, claimed)

	// The one that started ran to completion, and its outcome was recorded on a
	// context the cancellation does not reach.
	test.EqOp(T, int64(1), handled.Load())

	completed, released, _ := stub.calls()
	must.SliceLen(T, 1, completed)
	test.Eq(T, []string{"first"}, completed[0].ItemKeys)

	must.SliceLen(T, 1, released)
	test.Eq(T, []string{"second", "third"}, released[0].ItemKeys)

	// No delay: they were claimed and never started, so there is nothing to wait
	// out. The cause says so, because the claim already spent an attempt on them.
	test.EqOp(T, int64(0), released[0].DelayMicroseconds)
	must.NotNil(T, released[0].LastError)
	test.StrContains(T, *released[0].LastError, ErrRunnerStopped.Error())
}

// The acceptance criterion in memory: a handler slower than the lease keeps its
// claim, because the runner pushes the horizon out while it runs. This is the
// Go half — that the statement is issued, for the running items, with the
// configured lease — and the container suite is the half that proves the row
// actually moves.
func TestRunner_ExtendsWhileAHandlerRuns(T *testing.T) {
	T.Parallel()

	extended := make(chan struct{}, 1)

	r, stub := newTestRunner(T, func(cfg *RunnerConfig) {
		cfg.Lease = time.Second
		cfg.ExtendInterval = 100 * time.Millisecond
	}, func(ctx context.Context, _ Item[string]) error {
		// Held open until the heartbeat has fired at least once, rather than for
		// a fixed stretch: the property is that an extension arrives while the
		// handler is running, not that it arrives inside some sleep.
		select {
		case <-extended:
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}

		return nil
	})

	stub.claim = func(workqueuedb.ClaimDueItemsParams) ([]workqueuedb.ClaimDueItemsRow, error) {
		return claimRows("slow"), nil
	}

	stub.extend = func(arg workqueuedb.ExtendItemsParams) (int64, error) {
		select {
		case extended <- struct{}{}:
		default:
		}

		return int64(len(arg.ItemKeys)), nil
	}

	_, err := r.pass(T.Context())
	must.NoError(T, err)

	completed, released, extensions := stub.calls()
	must.Greater(T, 0, len(extensions))
	test.Eq(T, []string{"slow"}, extensions[0].ItemKeys)
	test.EqOp(T, r.cfg.Lease.Microseconds(), extensions[0].LeaseMicroseconds)

	// The claim's own name, so an extension from a worker that lost the item
	// matches nothing rather than pinning it to a claim nobody is working under.
	must.SliceLen(T, 1, extensions[0].LeasedBys)
	test.NotEqOp(T, "", extensions[0].LeasedBys[0])

	// And the item is still completed normally afterwards, with the heartbeat
	// stopped before the completion rather than racing it.
	must.SliceLen(T, 1, completed)
	test.SliceEmpty(T, released)
}

// An item whose handler has returned is out of the set before the next tick, so
// a finished item is never extended — which is what keeps the lost-lease counter
// meaningful instead of counting the queue's own completions.
func TestRunner_StopsExtendingAFinishedItem(T *testing.T) {
	T.Parallel()

	r, stub := newTestRunner(T, func(cfg *RunnerConfig) {
		cfg.Lease = time.Second
		cfg.ExtendInterval = 100 * time.Millisecond
	}, func(_ context.Context, item Item[string]) error {
		if item.Key == "quick" {
			return nil
		}

		time.Sleep(250 * time.Millisecond)

		return nil
	})

	stub.claim = func(workqueuedb.ClaimDueItemsParams) ([]workqueuedb.ClaimDueItemsRow, error) {
		return claimRows("quick", "slow"), nil
	}

	_, err := r.pass(T.Context())
	must.NoError(T, err)

	_, _, extensions := stub.calls()
	must.Greater(T, 0, len(extensions))

	for i := range extensions {
		test.StrNotContains(T, strings.Join(extensions[i].ItemKeys, ","), "quick")
	}
}

func TestRunner_Run(T *testing.T) {
	T.Parallel()

	T.Run("returns before claiming anything when the context is already done", func(t *testing.T) {
		t.Parallel()

		var handled atomic.Int64

		r, stub := newTestRunner(t, nil, func(context.Context, Item[string]) error {
			handled.Add(1)

			return nil
		})

		stub.claim = func(workqueuedb.ClaimDueItemsParams) ([]workqueuedb.ClaimDueItemsRow, error) {
			return claimRows("a"), nil
		}

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := r.Run(ctx)

		test.ErrorIs(t, err, context.Canceled)
		test.EqOp(t, int64(0), handled.Load())
	})

	// A database that is briefly unreachable is an outage to ride out, not a
	// reason for a fleet to stop draining a queue when it comes back.
	T.Run("keeps going through a failing claim and stops only on the context", func(t *testing.T) {
		t.Parallel()

		var claims atomic.Int64

		r, stub := newTestRunner(t, nil, func(context.Context, Item[string]) error { return nil })

		stub.claim = func(workqueuedb.ClaimDueItemsParams) ([]workqueuedb.ClaimDueItemsRow, error) {
			claims.Add(1)

			return nil, stderrors.New("connection refused by the test")
		}

		ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		defer cancel()

		err := r.Run(ctx)

		test.ErrorIs(t, err, context.DeadlineExceeded)
		test.Greater(t, int64(1), claims.Load())
	})
}

package operations

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newRecordingStore is a fakeStore whose Progress always answers the same way,
// for the tests that care about what the reporter buffered rather than about
// what the row did with it.
func newRecordingStore(ack Ack, err error) *fakeStore {
	s := newFakeStore()
	s.progressFunc = func(string, Progress) (Ack, error) { return ack, err }

	return s
}

// newTestReporter builds a reporter with a long interval, so nothing flushes
// except the boundaries and the closes the test drives itself.
func newTestReporter(store Store, op *Operation) *reporter {
	if op == nil {
		op = &Operation{ID: "op1"}
	}

	return newReporter(store, logging.EnsureLogger(nil), op, time.Minute, time.Hour,
		Attempt{ID: op.ID, Number: 1})
}

func TestReporter_attemptIsWhatTheWorkerHandedIt(t *testing.T) {
	t.Parallel()

	attempt := Attempt{ID: "op1", Number: 3, Final: true}

	rep := newReporter(newFakeStore(), logging.EnsureLogger(nil), &Operation{ID: "op1"},
		time.Minute, time.Hour, attempt)

	test.EqOp(t, attempt, rep.Attempt())

	// Fixed for the reporter's life: a flush moves progress, not this.
	rep.Advance(10)
	rep.flush(t.Context())

	test.EqOp(t, attempt, rep.Attempt())
}

func TestReporter_units(T *testing.T) {
	T.Parallel()

	T.Run("the tiers move independently", func(t *testing.T) {
		t.Parallel()

		store := newRecordingStore(Ack{Held: true}, nil)
		rep := newTestReporter(store, nil)

		rep.SetUnits(3)

		for _, unit := range []string{"identity", "webhooks", "mealplanning"} {
			rep.StartUnit(unit)
			rep.Advance(100)
			rep.FinishUnit()
		}

		rep.close(t.Context())

		final := store.lastProgress()
		test.EqOp(t, 3, final.UnitsDone)
		must.NotNil(t, final.UnitsTotal)
		test.EqOp(t, 3, *final.UnitsTotal)

		// The count does not reset at a unit boundary. See Progress.Count: a
		// client showing 300 that suddenly showed 100 would read it as a fault.
		test.EqOp(t, int64(300), final.Count)

		test.EqOp(t, "", final.Unit)
	})

	// A unit boundary is the one moment a watching client's view is worth being
	// exactly right about, and there are only as many of them as there are
	// units.
	T.Run("boundaries flush", func(t *testing.T) {
		t.Parallel()

		store := newRecordingStore(Ack{Held: true}, nil)
		rep := newTestReporter(store, nil)

		rep.StartUnit("identity")
		rep.FinishUnit()

		test.SliceLen(t, 2, store.recordedProgress())
	})

	// The numerator has to survive overlapping units, because fanning out over
	// them is a shape this package invites. A flag would have the first
	// FinishUnit of an overlapping pair clear it and the second count nothing.
	T.Run("overlapping units all count", func(t *testing.T) {
		t.Parallel()

		store := newRecordingStore(Ack{Held: true}, nil)
		rep := newTestReporter(store, nil)

		rep.SetUnits(4)

		// Interleaved rather than nested, which is what a bounded worker pool
		// running four domains at a time actually produces.
		rep.StartUnit("identity")
		rep.StartUnit("webhooks")
		rep.FinishUnit()
		rep.StartUnit("billing")

		// A unit is still open, so the name is not blanked out from under it.
		test.NotEq(t, "", store.lastProgress().Unit)

		rep.FinishUnit()
		rep.FinishUnit()

		rep.close(t.Context())

		final := store.lastProgress()
		test.EqOp(t, 3, final.UnitsDone)
		test.EqOp(t, "", final.Unit)
	})

	T.Run("a unit left open is not counted", func(t *testing.T) {
		t.Parallel()

		store := newRecordingStore(Ack{Held: true}, nil)
		rep := newTestReporter(store, nil)

		rep.StartUnit("identity")
		rep.StartUnit("webhooks")
		rep.FinishUnit()

		rep.close(t.Context())

		// A Runner that abandoned a unit rather than finishing it must not have
		// it counted, however many others it opened.
		test.EqOp(t, 1, store.lastProgress().UnitsDone)
	})

	T.Run("FinishUnit without a StartUnit counts nothing", func(t *testing.T) {
		t.Parallel()

		store := newRecordingStore(Ack{Held: true}, nil)
		rep := newTestReporter(store, nil)

		rep.FinishUnit()
		rep.FinishUnit()

		test.EqOp(t, 0, store.lastProgress().UnitsDone)
	})

	T.Run("a double FinishUnit counts once", func(t *testing.T) {
		t.Parallel()

		store := newRecordingStore(Ack{Held: true}, nil)
		rep := newTestReporter(store, nil)

		rep.StartUnit("identity")
		rep.FinishUnit()
		rep.FinishUnit()

		test.EqOp(t, 1, store.lastProgress().UnitsDone)
	})

	T.Run("a negative unit total is ignored", func(t *testing.T) {
		t.Parallel()

		store := newRecordingStore(Ack{Held: true}, nil)
		rep := newTestReporter(store, nil)

		rep.SetUnits(-1)
		rep.close(t.Context())

		test.Nil(t, store.lastProgress().UnitsTotal)
	})
}

func TestReporter_Advance(T *testing.T) {
	T.Parallel()

	T.Run("accumulates", func(t *testing.T) {
		t.Parallel()

		store := newRecordingStore(Ack{Held: true}, nil)
		rep := newTestReporter(store, nil)

		rep.Advance(4000)
		rep.Advance(300)
		rep.close(t.Context())

		test.EqOp(t, int64(4300), store.lastProgress().Count)
	})

	// The counter is monotonic by contract, and a client watching a number go
	// backwards has no way to read it as anything but a fault.
	T.Run("a non-positive advance is ignored", func(t *testing.T) {
		t.Parallel()

		store := newRecordingStore(Ack{Held: true}, nil)
		rep := newTestReporter(store, nil)

		rep.Advance(100)
		rep.Advance(-50)
		rep.Advance(0)
		rep.close(t.Context())

		test.EqOp(t, int64(100), store.lastProgress().Count)
	})

	// A reclaimed operation has already reported some of its work. A reporter
	// starting from zero would flush a count the GREATEST in the write discards,
	// freezing the client's number for the rest of the run.
	T.Run("resumes from the row's progress", func(t *testing.T) {
		t.Parallel()

		store := newRecordingStore(Ack{Held: true}, nil)
		rep := newTestReporter(store, &Operation{
			ID: "op1",
			Progress: Progress{
				Count:      4300,
				UnitsDone:  2,
				UnitsTotal: pointer.To(9),
			},
		})

		rep.Advance(700)
		rep.close(t.Context())

		final := store.lastProgress()
		test.EqOp(t, int64(5000), final.Count)
		test.EqOp(t, 2, final.UnitsDone)
	})
}

func TestReporter_Sayf(T *testing.T) {
	T.Parallel()

	store := newRecordingStore(Ack{Held: true}, nil)
	rep := newTestReporter(store, nil)

	rep.Sayf("collecting %d of %d", 3, 9)
	rep.close(T.Context())

	test.EqOp(T, "collecting 3 of 9", store.lastProgress().Message)
}

func TestReporter_Cancelled(T *testing.T) {
	T.Parallel()

	T.Run("a flush that sees the flag closes the channel", func(t *testing.T) {
		t.Parallel()

		rep := newTestReporter(newRecordingStore(Ack{Held: true, CancelRequested: true}, nil), nil)

		test.False(t, isCancelled(rep))

		rep.close(t.Context())

		test.True(t, isCancelled(rep))
	})

	// A lost lease reaches the Runner through the same channel as a
	// cancellation, and for the same reason: both mean "stop, somebody else owns
	// this now", and a Runner should not have to learn two concepts to handle
	// one situation.
	T.Run("a lost lease also cancels", func(t *testing.T) {
		t.Parallel()

		rep := newTestReporter(newRecordingStore(Ack{Held: false}, nil), nil)

		rep.close(t.Context())

		test.True(t, isCancelled(rep))
		test.True(t, rep.lostLease())
	})

	T.Run("closing twice does not panic", func(t *testing.T) {
		t.Parallel()

		rep := newTestReporter(newRecordingStore(Ack{Held: true, CancelRequested: true}, nil), nil)

		rep.close(t.Context())
		rep.close(t.Context())

		test.True(t, isCancelled(rep))
	})
}

// Progress is advisory: a store that cannot record it must not take the Runner
// down with it, because losing an update costs a watching client a couple of
// seconds and costs the work nothing.
func TestReporter_flushFailureIsSurvivable(T *testing.T) {
	T.Parallel()

	rep := newTestReporter(newRecordingStore(Ack{}, context.DeadlineExceeded), nil)

	rep.Advance(10)
	rep.close(T.Context())

	test.False(T, isCancelled(rep))
	test.False(T, rep.lostLease())
}

// A Runner fanning out over units concurrently is exactly the shape this package
// is for, so the buffer and the flushes have to survive it.
func TestReporter_concurrentReporting(T *testing.T) {
	T.Parallel()

	store := newRecordingStore(Ack{Held: true}, nil)
	rep := newTestReporter(store, nil)

	var wg sync.WaitGroup

	for i := range 8 {
		wg.Add(1)

		go func(n int) {
			defer wg.Done()

			rep.StartUnit("unit")

			for range 50 {
				rep.Advance(1)
			}

			rep.Sayf("worker %d", n)
			rep.FinishUnit()
		}(i)
	}

	wg.Wait()
	rep.close(T.Context())

	test.EqOp(T, int64(400), store.lastProgress().Count)
}

// The loop ticks whether or not anything changed, because the flush is also the
// lease extension: an operation whose Runner has gone quiet is still working.
func TestReporter_runFlushesWithNothingNewToSay(t *testing.T) {
	t.Parallel()

	store := newRecordingStore(Ack{Held: true}, nil)
	rep := newReporter(store, logging.EnsureLogger(nil), &Operation{ID: "op1"}, time.Minute, 5*time.Millisecond,
		Attempt{ID: "op1", Number: 1})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go rep.run(ctx)

	deadline := time.After(2 * time.Second)

	for len(store.recordedProgress()) < 2 {
		select {
		case <-deadline:
			t.Fatal("the reporter stopped flushing while its Runner was quiet")
		case <-time.After(time.Millisecond):
		}
	}

	rep.close(t.Context())
}

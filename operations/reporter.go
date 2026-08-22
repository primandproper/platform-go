package operations

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/observability/logging"
)

// Reporter is how a Runner says where it has got to.
//
// Every method is buffered, in-memory, and cheap enough to call in a tight loop:
// a Runner calling Advance once per row is doing an integer add, not a database
// write. The buffer is flushed on WorkerConfig.ProgressInterval, at every unit
// boundary, and once more when the Runner returns.
//
// That is why nothing here returns an error. Progress is advisory — an update
// that does not land costs a watching client a couple of seconds of staleness
// and costs the work nothing — and a Runner forced to handle an error from
// something that cannot meaningfully fail ends up ignoring it, which is worse
// than the library ignoring it on the Runner's behalf and counting it.
//
// The one thing that is not advisory is Cancelled, and it is not advisory
// precisely because it rides on the same flush. See its documentation.
type Reporter interface {
	// SetUnits declares the denominator: how many units this operation will fan
	// out over. It is what turns a spinner into "3 of 9".
	//
	// Call it as soon as the number is known, which is usually after the first
	// query that enumerates the work. A Runner that never calls it reports
	// progress with no total, which is a supported outcome rather than a
	// degraded one — see Progress.
	SetUnits(total int)

	// StartUnit names the unit now in progress. It does not imply the previous
	// one finished; FinishUnit is what says that, because a unit that was
	// abandoned rather than completed must not be counted.
	StartUnit(name string)

	// FinishUnit records that the unit named by the last StartUnit is complete,
	// raising the numerator.
	FinishUnit()

	// Advance adds n to the operation's monotonic count — rows collected,
	// records indexed, files written.
	//
	// It is the tier for work that cannot say how much there is without a
	// counting pass first, which is most work. A negative n is ignored rather
	// than subtracted: the count is monotonic by contract, and a client watching
	// a number go backwards has no way to read that as anything but a fault.
	Advance(n int64)

	// Sayf sets the human-readable note attached to the operation. It is never
	// parsed and nothing branches on it.
	Sayf(format string, args ...any)

	// Attempt describes the execution this Runner is in: which operation, which
	// attempt, and whether it is the last one. It is fixed for the life of the
	// Runner.
	//
	// It is on this interface rather than a fourth parameter to Run because it
	// is the same kind of thing as Cancelled — a fact about the execution rather
	// than about the work — and because a Runner that does not care about
	// retries should not have to name it in its signature.
	Attempt() Attempt

	// Cancelled closes when somebody has asked this operation to stop.
	//
	// A Runner is under no obligation to consult it, and one that does not
	// simply runs to completion — which is honest, because the work was in fact
	// done. A Runner that does consult it should stop at a point it can describe:
	// between units, not between two halves of a write.
	//
	// The channel is fed by the same flush that writes progress, so a Runner
	// that never reports progress will never observe a cancellation either.
	// There is no separate poll, and there deliberately is not one: a background
	// query per running operation, on the chance somebody might cancel, is a
	// steady cost paid for a rare event.
	Cancelled() <-chan struct{}
}

// reporter is the Reporter implementation, and the flush loop behind it.
//
// One is built per claimed operation and lives exactly as long as the Runner
// does. It owns a goroutine, which the worker starts with run and stops with
// close — a Runner never sees either.
type reporter struct {
	store  Store
	logger logging.Logger

	// cancelled is closed once, by the flush that first observes the flag.
	cancelled chan struct{}

	done chan struct{}

	id string

	// attempt is fixed at construction and read without a lock: nothing writes
	// it after the reporter exists.
	attempt Attempt

	// progress is the buffer. Guarded by mu because a Runner may report from
	// several goroutines — fanning out over units concurrently is exactly the
	// shape this package is for — while the flush loop reads it.
	progress Progress

	lease    time.Duration
	interval time.Duration

	closeOnce  sync.Once
	cancelOnce sync.Once

	// flushMu serializes the writes themselves, which mu cannot: a unit boundary
	// flushes from the Runner's goroutine while the ticker flushes from this
	// package's, and two overlapping statements would decide the row's unit name
	// and message by whichever commit was slower. The counters survive that
	// through GREATEST; the strings would not.
	flushMu sync.Mutex

	mu sync.Mutex

	// lost records that a flush found the row no longer ours. It is read by the
	// worker after the Runner returns, to decide whether recording an outcome is
	// still its business.
	lost bool

	// unitsOpen counts the units currently started and not yet finished, so
	// FinishUnit called twice, or without a StartUnit, does not inflate the
	// numerator.
	//
	// It is a count rather than a flag because a Runner may have several units
	// open at once — fanning out over them concurrently is a shape this package
	// invites, and dataprivacy's collectors are the first thing to do it. With a
	// flag, the first FinishUnit of an overlapping pair cleared it and the second
	// counted nothing, so a nine-domain export with four running at a time
	// reported about a third of the units it finished.
	unitsOpen int
}

var _ Reporter = (*reporter)(nil)

// newReporter builds a reporter over an operation the worker has just begun.
//
// It is seeded with the row's current progress rather than starting at zero.
// A reclaimed operation has already reported some of its work, and a reporter
// that started from nothing would flush a count lower than the one the row
// holds — which the GREATEST in the write would then discard, leaving the count
// frozen for the rest of the run.
func newReporter(
	store Store,
	logger logging.Logger,
	op *Operation,
	lease, interval time.Duration,
	attempt Attempt,
) *reporter {
	return &reporter{
		store:     store,
		logger:    logger,
		id:        op.ID,
		attempt:   attempt,
		progress:  op.Progress,
		lease:     lease,
		interval:  interval,
		cancelled: make(chan struct{}),
		done:      make(chan struct{}),
	}
}

func (r *reporter) Attempt() Attempt {
	return r.attempt
}

func (r *reporter) SetUnits(total int) {
	if total < 0 {
		return
	}

	r.mu.Lock()
	r.progress.UnitsTotal = &total
	r.mu.Unlock()
}

// StartUnit names the unit now in progress.
//
// Under a concurrent fan-out several units are open at once and Progress.Unit
// names whichever started most recently. The numerator is still exact — see
// unitsOpen — and the flapping name is the honest rendering of work that
// genuinely has four domains in flight.
func (r *reporter) StartUnit(name string) {
	r.mu.Lock()
	r.progress.Unit = name
	r.unitsOpen++
	r.mu.Unlock()

	// Flushed rather than merely marked dirty. A unit boundary is the one moment
	// a watching client's view is worth being exactly right about — it is the
	// tier a progress bar is drawn from — and there are as many of them as there
	// are units, which is a number the work already told us is small.
	r.flush(context.Background())
}

func (r *reporter) FinishUnit() {
	r.mu.Lock()
	if r.unitsOpen > 0 {
		r.progress.UnitsDone++
		r.unitsOpen--
	}

	// Cleared only once nothing is left open. Under a concurrent fan-out the
	// first unit to finish must not blank a name three others are still working
	// under.
	if r.unitsOpen == 0 {
		r.progress.Unit = ""
	}
	r.mu.Unlock()

	r.flush(context.Background())
}

func (r *reporter) Advance(n int64) {
	if n <= 0 {
		return
	}

	r.mu.Lock()
	r.progress.Count += n
	r.mu.Unlock()
}

func (r *reporter) Sayf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)

	r.mu.Lock()
	r.progress.Message = message
	r.mu.Unlock()
}

func (r *reporter) Cancelled() <-chan struct{} {
	return r.cancelled
}

// snapshot copies the buffer under the lock.
func (r *reporter) snapshot() Progress {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.progress
}

// lostLease reports whether a flush found the operation no longer ours.
func (r *reporter) lostLease() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.lost
}

// run flushes on a ticker until close is called.
//
// It ticks unconditionally rather than waiting for the buffer to change, and
// that is the whole reason there is no dirty flag: the flush is also what
// extends the lease and polls for cancellation. An operation whose Runner has
// gone quiet for a minute is still working, and stopping the writes because the
// numbers stopped moving would hand it to somebody else.
func (r *reporter) run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.flush(ctx)
		}
	}
}

// flush writes the buffer, extends the lease, and acts on what comes back.
//
// The context is detached for its deadline only: a shutdown arriving mid-flush
// should still record where the Runner got to, since that is what the next
// attempt resumes from reading.
func (r *reporter) flush(ctx context.Context) {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.interval+r.lease)
	defer cancel()

	ack, err := r.store.Progress(writeCtx, r.id, r.snapshot(), r.lease)
	if err != nil {
		// Logged, not propagated. See the Reporter documentation on why progress
		// is advisory; the store has already counted this.
		r.logger.WithValue(operationIDKey, r.id).Error("flushing operation progress", err)

		return
	}

	if !ack.Held {
		r.mu.Lock()
		r.lost = true
		r.mu.Unlock()

		// A lost lease is reported to the Runner through the same channel as a
		// cancellation, and for the same reason: both mean "stop, somebody else
		// owns this now". A Runner that stops on it does the right thing without
		// having to learn a second concept, and a Runner that ignores it is in
		// the duplicate-execution case the package documentation describes.
		r.markCancelled()

		return
	}

	if ack.CancelRequested {
		r.markCancelled()
	}
}

// markCancelled closes the cancellation channel, at most once.
func (r *reporter) markCancelled() {
	r.cancelOnce.Do(func() { close(r.cancelled) })
}

// close stops the flush loop and writes the buffer one final time, so the last
// thing a Runner said before returning is on the row before the outcome is.
func (r *reporter) close(ctx context.Context) {
	r.closeOnce.Do(func() {
		close(r.done)
		r.flush(ctx)
	})
}

// Cancelled reports whether somebody has asked the operation to stop, without
// waiting for it.
//
// It is exported because it is what every long-running step does between units
// of work, and it is one line of select away from being written wrong: a
// receive without a default blocks until cancellation arrives, which turns a
// progress check into a deadlock in the one case where the operation is fine.
// A Runner that cannot see Cancelled's channel — every one outside this
// package — would otherwise write that select itself.
func Cancelled(rep Reporter) bool {
	select {
	case <-rep.Cancelled():
		return true
	default:
		return false
	}
}

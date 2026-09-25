package timers

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v14/timers/internal/timersdb"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
)

// encodedTimer is one scheduled timer reduced to what the statement binds, with
// the key already rendered to its stored form.
type encodedTimer struct {
	runAt   time.Time
	key     string
	payload []byte
}

// Schedule writes timers, and returns once they are durably scheduled.
//
// This is the whole of the package's durability claim, and it is why an
// in-process time.AfterFunc is not an implementation of it: the schedule is a
// row before this returns, so it survives the process, the deploy, and the
// machine. A wakeup is only ever the news that a row exists.
//
// Scheduling a key that already has a timer moves that timer rather than adding
// a second one, and the new instant wins outright — later as readily as earlier.
// That is deliberately not a work queue's merge rule: enqueuing twice means "at
// least this soon", but rescheduling means "actually, then", and a trial
// extended by a week has to be expressible. The payload is replaced with it, and
// the attempt count and last error reset, because this is a new schedule rather
// than a retry of the old one.
//
// Rescheduling a timer that is being fired right now is safe and does what you
// would want: the firing worker's Complete carries the instant it was handed,
// which no longer matches the row, so it marks nothing and the new schedule
// stands. The lease is dropped with it, so the new schedule does not have to
// wait out a lease nothing can still discharge. The handler already running is
// not interrupted — nothing can do that — so a timer moved during its own firing
// may still have fired once.
//
// Rescheduling to the instant a timer already has is not a move, and leaves an
// outstanding lease alone. That is the shape an at-least-once upstream produces
// when it redelivers "start trial": treating it as a move would free a row
// somebody is firing and let a second worker fire it too.
//
// The whole batch is one statement on Postgres and one transaction elsewhere,
// so either every timer in it is scheduled or none is. Unlike a work queue's enqueue there is no group commit across
// concurrent callers: scheduling is not a per-request write path — one row is
// created when a trial starts, not on every read of it — so the contention that
// makes merging worth its complexity does not arise. If you find yourself
// scheduling on every request, you want a work queue.
//
// What that statement does not join is the caller's transaction. Schedule writes
// on this set's own handle and there is no variant taking a database.Tx. Unlike
// a work queue's enqueue, where a batch shared between callers makes one
// impossible, that is a choice with a reason rather than a constraint: the write
// runs under pgretry, which re-runs it when Postgres reports one of the two
// class 40 conditions it resolves by asking for the statement to be run again.
// Inside somebody else's transaction there is nothing to re-run — the failure
// has already aborted that transaction, and only its owner can open another — so
// a transactional Schedule would hand a deadlock back as an error on a table
// whose ordered locking exists precisely because concurrent writers meet there.
//
// A choice costs something, and the cost belongs in the open. A trial and the
// timer that expires it commit separately, in both directions: a schedule
// written inside client.WithTransaction outlives that transaction's rollback and
// fires for a subject that was never created, and a schedule that fails after
// the subject's transaction committed leaves a trial nothing will ever expire.
// The second is the expensive direction, because a firing that never comes is
// not an error anybody is holding — it is silence.
//
// So where the schedule must not be lost, write the fact into the transaction
// that created the subject — an outbox message is the shape — and Schedule from
// whatever consumes it: the message lives or dies with the row, and the consumer
// retries until the schedule lands. Otherwise schedule after the commit, and
// keep the handler tolerant of a key whose subject is gone, which it has to be
// regardless: Cancel and the subject's own deletion are two writes, so a firing
// that finds nothing to do is done rather than failed.
func (t *Timers[K]) Schedule(ctx context.Context, scheduled ...Timer[K]) error {
	ctx, op := t.o11y.Begin(ctx, observability.WithValue(timerCountKey, len(scheduled)))
	defer op.End()

	if len(scheduled) == 0 {
		return nil
	}

	rows := make([]encodedTimer, 0, len(scheduled))

	for i := range scheduled {
		if scheduled[i].RunAt.IsZero() {
			return op.Error(ErrZeroRunAt, "scheduling timers")
		}

		if len(scheduled[i].Payload) > MaxPayloadSize {
			return op.Error(platformerrors.Wrapf(ErrPayloadTooLarge,
				"payload is %d bytes, over the %d-byte limit", len(scheduled[i].Payload), MaxPayloadSize),
				"scheduling timers")
		}

		key, err := encodeKey(t.codec, scheduled[i].Key)
		if err != nil {
			return op.Error(err, "encoding timer key")
		}

		rows = append(rows, encodedTimer{
			key:     key,
			runAt:   roundUpToMicrosecond(scheduled[i].RunAt.UTC()),
			payload: scheduled[i].Payload,
		})
	}

	rows = sortAndDedupeTimers(rows)

	// MySQL and SQLite have no array to bind a column of, so the batch is a
	// statement per timer there instead, in one transaction. Nothing is
	// notified: New has refused a channel on a dialect with no NOTIFY.
	if t.split != nil {
		if err := t.retrier.Do(ctx, "schedule", func() error {
			return t.scheduleSplit(ctx, rows)
		}); err != nil {
			return op.Error(err, "scheduling timers")
		}

		t.scheduledCounter.Add(ctx, int64(len(rows)), t.attrs)

		return nil
	}

	// Three parallel arrays rather than a tuple per row: the statement is one
	// fixed text however large the batch is, and the nth element of each is one
	// timer. See timers/internal/queries on the ordinality join that puts them
	// back together.
	keys := make([]string, 0, len(rows))
	instants := make([]time.Time, 0, len(rows))
	payloads := make([][]byte, 0, len(rows))

	for i := range rows {
		keys = append(keys, rows[i].key)
		instants = append(instants, rows[i].runAt)
		payloads = append(payloads, rows[i].payload)
	}

	if err := t.retrier.Do(ctx, "schedule", func() error {
		if execErr := t.q.ScheduleTimers(ctx, t.client.Writer(), timersdb.ScheduleTimersParams{
			TimerSet:  t.cfg.Name,
			TimerKeys: keys,
			RunAts:    instants,
			Payloads:  payloads,
		}); execErr != nil {
			return platformerrors.Wrap(execErr, "writing timers")
		}

		return nil
	}); err != nil {
		return op.Error(err, "scheduling timers")
	}

	t.scheduledCounter.Add(ctx, int64(len(rows)), t.attrs)

	t.notify(ctx)

	return nil
}

// ScheduleAt is Schedule for one timer named by an absolute instant, which is
// the shape most callers have: the deadline came from a subscription, a contract,
// or a policy, and is already a time.Time.
func (t *Timers[K]) ScheduleAt(ctx context.Context, key K, runAt time.Time, payload []byte) error {
	return t.Schedule(ctx, Timer[K]{Key: key, RunAt: runAt, Payload: payload})
}

// ScheduleIn is Schedule for one timer named by a delay from now — "remind them
// in three days" — resolving it against this set's clock.
//
// It is the only place a delay becomes an instant, and the instant is what is
// stored. Two processes with skewed clocks that say "in three days" a moment
// apart therefore schedule for slightly different instants, which is exactly
// what they asked for; what they cannot disagree about is whether a stored
// instant has arrived, because the database answers that.
//
// A non-positive delay schedules for now, which is due immediately. That is
// allowed — a timer fired as soon as a worker gets to it is a meaningful request
// — where a zero RunAt is not, because a zero RunAt is what a forgotten
// assignment looks like.
func (t *Timers[K]) ScheduleIn(ctx context.Context, key K, delay time.Duration, payload []byte) error {
	return t.Schedule(ctx, Timer[K]{Key: key, RunAt: t.clock.Now().Add(delay), Payload: payload})
}

// notify wakes whoever is listening, after the rows are committed and never
// before — a poller woken early would re-read a next-due time that has not
// changed yet and go back to sleep for however long the old one said, which is
// precisely the latency this exists to remove.
//
// A failure here is logged rather than returned. The timers are already durably
// scheduled; reporting an error would tell the caller its schedule failed when it
// did not, and the only consequence of a missing notification is that a poller
// finds the row on its next poll — exactly what happens when a listener is
// reconnecting.
func (t *Timers[K]) notify(ctx context.Context) {
	if t.cfg.NotifyChannel == "" {
		return
	}

	if _, err := t.client.Writer().ExecContext(ctx, dialect.PostgresNotifyStatement, t.cfg.NotifyChannel); err != nil {
		t.o11y.Logger().WithValue(notifyChannelKey, t.cfg.NotifyChannel).Error("notifying timer channel", err)
	}
}

// roundUpToMicrosecond moves an instant to the next whole microsecond, and
// leaves one already on it alone.
//
// Microseconds are the finest instant Postgres and MySQL store, and a stored
// instant rounded to the nearest one — which is what Postgres does with the
// nanoseconds a time.Time carries — is half the time an instant earlier than
// the one the caller named. Up is the only direction a timer may move. SQLite
// stores milliseconds, and its statement rounds this up again to one of those.
func roundUpToMicrosecond(at time.Time) time.Time {
	if truncated := at.Truncate(time.Microsecond); !truncated.Equal(at) {
		return truncated.Add(time.Microsecond)
	}

	return at
}

// sortAndDedupeTimers puts a batch into primary-key order and collapses repeats
// of a key onto its last occurrence.
//
// The sort is the lock ordering the whole design rests on; see the schedule
// statement in timers/internal/queries.
// The dedupe is not optional either — ON CONFLICT DO UPDATE refuses to touch the
// same row twice in one statement — and last-wins is the only rule consistent
// with what a second Schedule of the same key means everywhere else: the latest
// word about a timer is the one that holds.
func sortAndDedupeTimers(rows []encodedTimer) []encodedTimer {
	byKey := make(map[string]encodedTimer, len(rows))
	for i := range rows {
		byKey[rows[i].key] = rows[i]
	}

	out := make([]encodedTimer, 0, len(byKey))
	for key := range byKey {
		out = append(out, byKey[key])
	}

	slices.SortFunc(out, func(a, b encodedTimer) int {
		return strings.Compare(a.key, b.key)
	})

	return out
}

// sortAndDedupeFirings puts a batch of firings into primary-key order and
// removes exact repeats, for the writers that bind them directly.
//
// Two firings of one key differing in either fence are not repeats and both
// survive: at most one of them can match the row, and which one is the question
// the fences exist to answer.
func sortAndDedupeFirings(rows []firingRef) []firingRef {
	slices.SortFunc(rows, func(a, b firingRef) int {
		if byKey := strings.Compare(a.key, b.key); byKey != 0 {
			return byKey
		}

		if byInstant := a.runAt.Compare(b.runAt); byInstant != 0 {
			return byInstant
		}

		return strings.Compare(a.leasedBy, b.leasedBy)
	})

	return slices.CompactFunc(rows, func(a, b firingRef) bool {
		return a.key == b.key && a.runAt.Equal(b.runAt) && a.leasedBy == b.leasedBy
	})
}

// sortAndDedupe puts a batch of encoded keys into primary-key order and removes
// repeats, for Cancel, which binds keys alone.
func sortAndDedupe(keys []string) []string {
	slices.Sort(keys)

	return slices.Compact(keys)
}

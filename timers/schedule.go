package timers

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
)

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
// The whole batch is one statement, so either every timer in it is scheduled or
// none is. Unlike a work queue's enqueue there is no group commit across
// concurrent callers: scheduling is not a per-request write path — one row is
// created when a trial starts, not on every read of it — so the contention that
// makes merging worth its complexity does not arise. If you find yourself
// scheduling on every request, you want a work queue.
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
			runAt:   scheduled[i].RunAt.UTC(),
			payload: scheduled[i].Payload,
		})
	}

	rows = sortAndDedupeTimers(rows)

	query, args := buildSchedule(t.cfg.resolvedTable(), t.cfg.Name, rows)

	if err := t.retrier.Do(ctx, "schedule", func() error {
		if _, execErr := t.client.Writer().ExecContext(ctx, query, args...); execErr != nil {
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
// instant has arrived, because Postgres answers that.
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

// sortAndDedupeTimers puts a batch into primary-key order and collapses repeats
// of a key onto its last occurrence.
//
// The sort is the lock ordering the whole design rests on; see buildSchedule.
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
// Two firings of one key with different instants are not repeats and both
// survive: at most one of them can match the row, and which one is the question
// the fence exists to answer.
func sortAndDedupeFirings(rows []firingRef) []firingRef {
	slices.SortFunc(rows, func(a, b firingRef) int {
		if byKey := strings.Compare(a.key, b.key); byKey != 0 {
			return byKey
		}

		return a.runAt.Compare(b.runAt)
	})

	return slices.CompactFunc(rows, func(a, b firingRef) bool {
		return a.key == b.key && a.runAt.Equal(b.runAt)
	})
}

// sortAndDedupe puts a batch of encoded keys into primary-key order and removes
// repeats, for Cancel, which binds keys alone.
func sortAndDedupe(keys []string) []string {
	slices.Sort(keys)

	return slices.Compact(keys)
}

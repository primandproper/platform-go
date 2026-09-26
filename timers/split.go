package timers

import (
	"context"
	"slices"
	"time"

	"github.com/primandproper/platform-go/v14/timers/internal/timerssplitdb"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// This file is the set on MySQL and SQLite: the statements of
// timers/internal/timerssplitdb, and the transactions and loops that turn them
// into the same operations the Postgres statements are one round trip each.
// Every method here is reached from the method of the same purpose in
// timers.go or schedule.go when the set was built over one of the two, and
// nothing here decides anything those methods have not already decided — the
// fences, the reschedule rule and the clock are the corpus's, and the retries
// and the instruments are the caller's.

// claimSplit is one claim attempt as statements in one transaction: the
// candidates, the locking read that takes the ones nobody else holds, the
// lease, and the read-back by the name the lease stamped.
//
// The transaction is the whole of what makes that one claim rather than
// several writes. On MySQL the locking read's row locks are held until it
// commits, so no other claimant can take the rows this one is about to lease;
// on SQLite the transaction is the only writer. The flag saying whether a lease
// lapsed is read in the locking read and carried across to the read-back by
// key, because the lease in between overwrites what it was computed from.
func (t *Timers[K]) claimSplit(ctx context.Context, limit int, lease time.Duration, leasedBy string) ([]Due[K], error) {
	var due []Due[K]

	err := t.client.WithTransaction(ctx, func(tx database.Tx) error {
		reclaimed, err := t.lockDue(ctx, tx, limit)
		if err != nil {
			return err
		}

		if len(reclaimed) == 0 {
			return nil
		}

		keys := make([]string, 0, len(reclaimed))
		for key := range reclaimed {
			keys = append(keys, key)
		}

		// Leased in key order, whatever order the read found them in, which is
		// the order every other keyed writer takes its locks in.
		slices.Sort(keys)

		if _, err = t.split.LeaseTimers(ctx, tx, timerssplitdb.LeaseTimersParams{
			LeaseMicroseconds: lease.Microseconds(),
			LeasedBy:          &leasedBy,
			TimerSet:          t.cfg.Name,
			AttemptCeiling:    int64(t.cfg.attemptCeiling()),
			TimerKeys:         keys,
		}); err != nil {
			return platformerrors.Wrap(err, "leasing due timers")
		}

		leased, err := t.split.FetchLeasedTimers(ctx, tx, timerssplitdb.FetchLeasedTimersParams{
			TimerSet:  t.cfg.Name,
			LeasedBy:  &leasedBy,
			TimerKeys: keys,
		})
		if err != nil {
			return platformerrors.Wrap(err, "reading leased timers")
		}

		due = make([]Due[K], 0, len(leased))

		for i := range leased {
			// A zero-length payload comes back from SQLite's driver as nil, and
			// nil is what "no payload" means; the column knows which it holds.
			payload := leased[i].Payload
			if leased[i].HasPayload && payload == nil {
				payload = []byte{}
			}

			fired, decodeErr := t.claimedDue(&claimedRow{
				key:       leased[i].TimerKey,
				payload:   payload,
				runAt:     leased[i].RunAt,
				late:      leased[i].LateMicroseconds,
				attempts:  leased[i].Attempts,
				reclaimed: reclaimed[leased[i].TimerKey],
			}, leasedBy)
			if decodeErr != nil {
				return decodeErr
			}

			due = append(due, fired)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return due, nil
}

// lockDue takes up to limit of the set's due timers the way the Postgres
// claim's LIMIT above its lock does: a timer another claimant holds is skipped
// and replaced rather than counted, so the batch is full whenever that many are
// due.
//
// Each pass reads the next candidates in claim order from the transaction's
// snapshot, locks the ones nobody holds, and keeps them; a pass that read fewer
// candidates than it asked for has read the last of them. On SQLite nothing is
// ever held, so the first pass is the only one.
//
// It reports each timer it took against whether that claim is a reclaim.
//
// tx is the generated querier's executor rather than a database.Tx, which is
// what claimSplit hands it, so that a test can hold a claim's locks open on a
// transaction of its own.
func (t *Timers[K]) lockDue(ctx context.Context, tx timerssplitdb.DBTX, limit int) (map[string]bool, error) {
	reclaimed := make(map[string]bool, limit)

	for offset := 0; len(reclaimed) < limit; {
		want := limit - len(reclaimed)

		candidates, err := t.split.SelectDueTimers(ctx, tx, timerssplitdb.SelectDueTimersParams{
			TimerSet:       t.cfg.Name,
			AttemptCeiling: int64(t.cfg.attemptCeiling()),
			ResultLimit:    int64(want),
			ResultOffset:   int64(offset),
		})
		if err != nil {
			return nil, platformerrors.Wrap(err, "selecting due timers")
		}

		if len(candidates) == 0 {
			break
		}

		offset += len(candidates)

		keys := make([]string, 0, len(candidates))
		for i := range candidates {
			keys = append(keys, candidates[i].TimerKey)
		}

		// Locked in key order, which is the order every other keyed writer
		// takes its locks in.
		slices.Sort(keys)

		locked, err := t.split.LockDueTimers(ctx, tx, timerssplitdb.LockDueTimersParams{
			TimerSet:       t.cfg.Name,
			AttemptCeiling: int64(t.cfg.attemptCeiling()),
			TimerKeys:      keys,
		})
		if err != nil {
			return nil, platformerrors.Wrap(err, "locking due timers")
		}

		for i := range locked {
			reclaimed[locked[i].TimerKey] = locked[i].Reclaimed
		}

		if len(candidates) < want {
			break
		}
	}

	return reclaimed, nil
}

// scheduleSplit writes one batch as a statement per timer, in one transaction.
//
// The rows arrive in key order — see sortAndDedupeTimers — and are written in
// it, which is the lock-ordering discipline the Postgres statement's ORDER BY
// applies within itself. One transaction rather than one per timer so that the
// batch lands or fails as the Postgres statement does: either every timer in
// it is scheduled or none is.
func (t *Timers[K]) scheduleSplit(ctx context.Context, rows []encodedTimer) error {
	return t.client.WithTransaction(ctx, func(tx database.Tx) error {
		for i := range rows {
			if err := t.split.ScheduleTimer(ctx, tx, timerssplitdb.ScheduleTimerParams{
				TimerSet:          t.cfg.Name,
				TimerKey:          rows[i].key,
				RunAtMicroseconds: rows[i].runAt.UnixMicro(),
				Payload:           rows[i].payload,
			}); err != nil {
				return platformerrors.Wrap(err, "writing timer")
			}
		}

		return nil
	})
}

// reapSplit is one reaping pass as a read and a delete, in one transaction.
// The read takes no lock — see the split corpus's selectReapableTimers — and
// the delete repeats its test, so two reapers that read the same rows delete
// them once between them and a timer restarted in between is not deleted.
func (t *Timers[K]) reapSplit(ctx context.Context) (int64, error) {
	var reaped int64

	err := t.client.WithTransaction(ctx, func(tx database.Tx) error {
		doomed, err := t.split.SelectReapableTimers(ctx, tx, timerssplitdb.SelectReapableTimersParams{
			TimerSet:              t.cfg.Name,
			RetentionMicroseconds: t.cfg.Retention.Microseconds(),
			ResultLimit:           int64(t.cfg.ReapBatchSize),
		})
		if err != nil {
			return platformerrors.Wrap(err, "selecting reapable timers")
		}

		if len(doomed) == 0 {
			return nil
		}

		keys := make([]string, 0, len(doomed))
		for i := range doomed {
			keys = append(keys, doomed[i].TimerKey)
		}

		// Deleted in key order, whatever order the read found them in, which is
		// the order every other keyed writer takes its locks in.
		slices.Sort(keys)

		reaped, err = t.split.DeleteReapedTimers(ctx, tx, timerssplitdb.DeleteReapedTimersParams{
			TimerSet:              t.cfg.Name,
			RetentionMicroseconds: t.cfg.Retention.Microseconds(),
			TimerKeys:             keys,
		})
		if err != nil {
			return platformerrors.Wrap(err, "deleting reaped timers")
		}

		return nil
	})
	if err != nil {
		return 0, err
	}

	return reaped, nil
}

// byHolder splits a sorted batch of firings into one run of keys per claim that
// holds them, which is what the split corpus's outcome writes take: a claim's
// name and the keys it is reporting on.
//
// The instants are dropped here, and the split corpus's heldBy is why that
// loses nothing: every statement that moves a timer's instant takes the name
// with it, so the name alone matches only the instant it was handed.
//
// The refs arrive sorted by key — see sortAndDedupeFirings — so each run is in
// key order, which is the order the writes lock in; a key named twice under
// one claim, with two instants, is one row and is named once. The runs come
// back in the holders' order, so the statements a batch becomes are the same
// statements in the same order however the caller built it.
func byHolder(refs []firingRef) (holders []string, keys map[string][]string) {
	keys = map[string][]string{}

	for i := range refs {
		held, seen := keys[refs[i].leasedBy]
		if !seen {
			holders = append(holders, refs[i].leasedBy)
		}

		if len(held) > 0 && held[len(held)-1] == refs[i].key {
			continue
		}

		keys[refs[i].leasedBy] = append(held, refs[i].key)
	}

	slices.Sort(holders)

	return holders, keys
}

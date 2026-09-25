package workqueue

import (
	"context"
	"slices"
	"time"

	"github.com/primandproper/platform-go/v14/workqueue/internal/workqueuesplitdb"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// This file is the queue on MySQL and SQLite: the statements of
// workqueue/internal/workqueuesplitdb, and the transactions and loops that turn
// them into the same operations the Postgres statements are one round trip
// each. Every method here is reached from the method of the same purpose in
// workqueue.go or enqueue.go when the queue was built over one of the two, and
// nothing here decides anything those methods have not already decided — the
// fence, the merge rule and the clock are the corpus's, and the retries and the
// instruments are the caller's.

// claimSplit is one claim attempt as three statements in one transaction: the
// locking read, the lease, and the read-back by the name the lease stamped.
//
// The transaction is the whole of what makes that one claim rather than three
// writes. On MySQL the read's row locks are held until it commits, so no other
// claimer can select the rows this one is about to lease; on SQLite the
// transaction is the only writer. The flag saying whether a lease lapsed is read
// in the first statement and carried across to the third by key, because the
// lease in between overwrites what it was computed from.
func (q *Queue[K]) claimSplit(ctx context.Context, limit int, lease time.Duration, leasedBy string) ([]Item[K], error) {
	var items []Item[K]

	err := q.client.WithTransaction(ctx, func(tx database.Tx) error {
		due, err := q.split.SelectDueItems(ctx, tx, workqueuesplitdb.SelectDueItemsParams{
			QueueName:      q.cfg.Name,
			AttemptCeiling: int64(q.cfg.attemptCeiling()),
			ResultLimit:    int64(limit),
		})
		if err != nil {
			return platformerrors.Wrap(err, "selecting due work queue items")
		}

		if len(due) == 0 {
			return nil
		}

		keys := make([]string, 0, len(due))
		reclaimed := make(map[string]bool, len(due))

		for i := range due {
			keys = append(keys, due[i].ItemKey)
			reclaimed[due[i].ItemKey] = due[i].Reclaimed
		}

		if _, err = q.split.LeaseItems(ctx, tx, workqueuesplitdb.LeaseItemsParams{
			LeaseMicroseconds: lease.Microseconds(),
			LeasedBy:          &leasedBy,
			QueueName:         q.cfg.Name,
			AttemptCeiling:    int64(q.cfg.attemptCeiling()),
			ItemKeys:          keys,
		}); err != nil {
			return platformerrors.Wrap(err, "leasing work queue items")
		}

		leased, err := q.split.FetchLeasedItems(ctx, tx, workqueuesplitdb.FetchLeasedItemsParams{
			QueueName: q.cfg.Name,
			LeasedBy:  &leasedBy,
		})
		if err != nil {
			return platformerrors.Wrap(err, "reading leased work queue items")
		}

		items = make([]Item[K], 0, len(leased))

		for i := range leased {
			item, decodeErr := q.claimedItem(leased[i].ItemKey, leased[i].Priority, leased[i].Attempts,
				leasedBy, reclaimed[leased[i].ItemKey])
			if decodeErr != nil {
				return decodeErr
			}

			items = append(items, item)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return items, nil
}

// upsertSplit writes one merged batch as a statement per row, in one
// transaction.
//
// The rows arrive in key order — see newEnqueueBatcher — and are written in it,
// which is the lock-ordering discipline the Postgres statement's ORDER BY
// applies within itself. One transaction rather than one per row so that the
// batch lands or fails as the Postgres statement does: a waiter told its keys
// are in is never told that about half of them.
func (q *Queue[K]) upsertSplit(ctx context.Context, rows []encodedEntry) error {
	return q.client.WithTransaction(ctx, func(tx database.Tx) error {
		for i := range rows {
			if err := q.split.EnqueueItem(ctx, tx, workqueuesplitdb.EnqueueItemParams{
				QueueName:         q.cfg.Name,
				ItemKey:           rows[i].key,
				Priority:          int64(rows[i].priority),
				DelayMicroseconds: rows[i].delayMicros,
			}); err != nil {
				return platformerrors.Wrap(err, "upserting work queue item")
			}
		}

		return nil
	})
}

// extendHeld is Extend's statement for one claim: push the leases out, then
// count what the claim still holds, in one transaction.
//
// The count is a second statement because the extension's own row count is not
// the answer on MySQL, which reports rows changed rather than matched — and an
// extension shorter than the lease it lands on changes nothing while the claim
// still holds the item. Counted in the same transaction, after the extension,
// the two statements see the same rows: the extension locked them and changes
// neither their holder nor their completion.
func (q *Queue[K]) extendHeld(ctx context.Context, lease time.Duration, holder string, keys []string) (int64, error) {
	var held int64

	err := q.client.WithTransaction(ctx, func(tx database.Tx) error {
		if _, err := q.split.ExtendItems(ctx, tx, workqueuesplitdb.ExtendItemsParams{
			LeaseMicroseconds: lease.Microseconds(),
			QueueName:         q.cfg.Name,
			LeasedBy:          &holder,
			ItemKeys:          keys,
		}); err != nil {
			return platformerrors.Wrap(err, "extending work queue leases")
		}

		row, err := q.split.CountHeldItems(ctx, tx, workqueuesplitdb.CountHeldItemsParams{
			QueueName: q.cfg.Name,
			LeasedBy:  &holder,
			ItemKeys:  keys,
		})
		if err != nil {
			return platformerrors.Wrap(err, "counting held work queue items")
		}

		held = row.Held

		return nil
	})

	return held, err
}

// reapSplit is one reaping pass as a locking read and a delete, in one
// transaction, for the reason the claim is: the read's locks are what keep two
// reapers from selecting the same rows, and on MySQL they last until commit.
func (q *Queue[K]) reapSplit(ctx context.Context) (int64, error) {
	var reaped int64

	err := q.client.WithTransaction(ctx, func(tx database.Tx) error {
		doomed, err := q.split.SelectReapableItems(ctx, tx, workqueuesplitdb.SelectReapableItemsParams{
			QueueName:             q.cfg.Name,
			RetentionMicroseconds: q.cfg.Retention.Microseconds(),
			ResultLimit:           int64(q.cfg.ReapBatchSize),
		})
		if err != nil {
			return platformerrors.Wrap(err, "selecting reapable work queue items")
		}

		if len(doomed) == 0 {
			return nil
		}

		keys := make([]string, 0, len(doomed))
		for i := range doomed {
			keys = append(keys, doomed[i].ItemKey)
		}

		// Deleted in key order, whatever order the read found them in, which is
		// the order every other keyed writer takes its locks in.
		slices.Sort(keys)

		reaped, err = q.split.DeleteReapedItems(ctx, tx, workqueuesplitdb.DeleteReapedItemsParams{
			QueueName:             q.cfg.Name,
			RetentionMicroseconds: q.cfg.Retention.Microseconds(),
			ItemKeys:              keys,
		})
		if err != nil {
			return platformerrors.Wrap(err, "reaping completed work queue items")
		}

		return nil
	})
	if err != nil {
		return 0, err
	}

	return reaped, nil
}

// readStatsSplit is the health read on the split corpus, in the Postgres row's
// shape so that Stats has one row to read.
func (q *Queue[K]) readStatsSplit(ctx context.Context) (statsRow, error) {
	row, err := q.split.ReadQueueStats(ctx, q.client.Reader(), workqueuesplitdb.ReadQueueStatsParams{
		QueueName:      q.cfg.Name,
		AttemptCeiling: int64(q.cfg.attemptCeiling()),
	})
	if err != nil {
		return statsRow{}, err
	}

	return statsRow(row), nil
}

// byHolder splits a sorted batch of claimed items into one run of keys per claim
// that holds them, which is what the split corpus's outcome writes take: a
// claim's name and the keys it is reporting on.
//
// The refs arrive sorted by key and then by holder — see sortAndDedupeItems —
// so each run is in key order, which is the order the writes lock in. The runs
// come back in the holders' order, so the statements a batch becomes are the
// same statements in the same order however the caller built it.
func byHolder(refs []itemRef) (holders []string, keys map[string][]string) {
	keys = map[string][]string{}

	for i := range refs {
		if _, seen := keys[refs[i].leasedBy]; !seen {
			holders = append(holders, refs[i].leasedBy)
		}

		keys[refs[i].leasedBy] = append(keys[refs[i].leasedBy], refs[i].key)
	}

	slices.Sort(holders)

	return holders, keys
}

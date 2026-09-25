package operations

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"github.com/primandproper/platform-go/v14/operations/internal/operationsdb"
	"github.com/primandproper/platform-go/v14/operations/internal/operationssplitdb"
	"github.com/primandproper/platform-go/v14/operations/internal/queries"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// This file is the store on MySQL and SQLite: the statements of
// operations/internal/operationssplitdb, and the transactions that turn them
// into the same answers the Postgres statements give in one round trip each.
// Every function here is reached from the SQLStore method of the same purpose
// when the store was built over one of the two, and nothing here decides
// anything those methods have not already decided — the guards, the monotonic
// floors and the clock are the corpus's, and the spans and the counters are the
// caller's.
//
// Each of them answers in the Postgres roster's row type. The two generated
// packages' rows are field for field the same, and the conversions below are
// the assertion: the day they stop being identical this stops building.
//
// Where a Postgres write hands its row back through RETURNING, the split write
// reports a row count, and the row is read on the same transaction only once
// the count says the write matched. A write that matched nothing therefore
// answers with sql.ErrNoRows, which is what the Postgres statement's empty
// RETURNING reports and what the caller already reads as the guard missing —
// never with a row somebody else's write left behind.

// insertSplit is Insert's statement pair: the guarded insert, and the read of
// the row it wrote, both on the caller's transaction.
//
// The read has to be on that transaction. The row is uncommitted until the
// caller commits, so a read on any other connection would not find it.
func (s *SQLStore) insertSplit(ctx context.Context, tx database.Tx, op *Operation) (*operationsdb.GetOperationRow, error) {
	created, err := s.split.InsertOperation(ctx, tx, operationssplitdb.InsertOperationParams(createParams(op)))
	if err != nil {
		return nil, err
	}

	// The id was taken. On MySQL the conflict branch assigns the key to itself,
	// which changes nothing and so counts nothing — see the corpus.
	if created == 0 {
		return nil, sql.ErrNoRows
	}

	row, err := s.split.GetOperationInScope(ctx, tx, operationssplitdb.GetOperationInScopeParams{
		ID:    op.ID,
		Scope: op.Owner,
	})
	if err != nil {
		return nil, err
	}

	shared := operationsdb.GetOperationRow(row)

	return &shared, nil
}

// beginSplit is Begin's statement pair: the guarded claim, and the read of the
// row it moved, in one transaction.
//
// The transaction is what makes the row the claim's own. The claim's lock is
// held until it commits — a row lock on MySQL, the one writer on SQLite — so
// nothing can move the row between the two statements.
func (s *SQLStore) beginSplit(
	ctx context.Context,
	id string,
	attempts int,
	lease time.Duration,
) (*operationsdb.GetOperationRow, error) {
	var claimed *operationsdb.GetOperationRow

	err := s.client.WithTransaction(ctx, func(tx database.Tx) error {
		won, err := s.split.BeginOperation(ctx, tx, operationssplitdb.BeginOperationParams{
			RunningState:      string(StateRunning),
			Attempts:          int64(attempts),
			LeaseMicroseconds: lease.Microseconds(),
			ID:                id,
			PendingState:      string(StatePending),
		})
		if err != nil {
			return err
		}

		if won == 0 {
			return sql.ErrNoRows
		}

		row, err := s.split.GetOperation(ctx, tx, operationssplitdb.GetOperationParams{ID: id})
		if err != nil {
			return err
		}

		shared := operationsdb.GetOperationRow(row)
		claimed = &shared

		return nil
	})
	if err != nil {
		return nil, err
	}

	return claimed, nil
}

// progressSplit is Progress's statement pair: the flush, and the read of what
// the Postgres flush returns — whether a cancellation has been requested, and
// the revision the flush wrote — in one transaction, for beginSplit's reason.
//
// The read-back is keyed by the id and repeats the flush's guard, and the
// transaction is what makes the revision it finds the flush's own rather than a
// later write's: the flush holds the row until commit, and every write to this
// table moves the revision, so no other write can land between the two.
func (s *SQLStore) progressSplit(
	ctx context.Context,
	params *operationsdb.RecordOperationProgressParams,
) (operationsdb.RecordOperationProgressRow, error) {
	var ack operationsdb.RecordOperationProgressRow

	err := s.client.WithTransaction(ctx, func(tx database.Tx) error {
		held, err := s.split.RecordOperationProgress(ctx, tx, operationssplitdb.RecordOperationProgressParams(*params))
		if err != nil {
			return err
		}

		if held == 0 {
			return sql.ErrNoRows
		}

		row, err := s.split.GetOperationAck(ctx, tx, operationssplitdb.GetOperationAckParams{
			ID:           params.ID,
			RunningState: params.RunningState,
		})
		if err != nil {
			return err
		}

		ack = operationsdb.RecordOperationProgressRow(row)

		return nil
	})

	return ack, err
}

// finishSplit is the terminal write, whichever of its two forms the caller
// asked for, guarded on the active set as the two states it is.
func (s *SQLStore) finishSplit(
	ctx context.Context,
	params *operationsdb.FinishOperationParams,
	unitsAllDone bool,
) (int64, error) {
	split := operationssplitdb.FinishOperationParams{
		State:          params.State,
		ResultURI:      params.ResultURI,
		ResultDetail:   params.ResultDetail,
		ErrorCode:      params.ErrorCode,
		ErrorMessage:   params.ErrorMessage,
		ErrorRetryable: params.ErrorRetryable,
		ID:             params.ID,
		PendingState:   string(StatePending),
		RunningState:   string(StateRunning),
	}

	if unitsAllDone {
		return s.split.FinishOperationWithEveryUnitDone(ctx, s.client.Writer(),
			operationssplitdb.FinishOperationWithEveryUnitDoneParams(split))
	}

	return s.split.FinishOperation(ctx, s.client.Writer(), split)
}

// requestCancelSplit is the cancellation write, guarded on the active set as
// the two states it is.
func (s *SQLStore) requestCancelSplit(ctx context.Context, id string) (int64, error) {
	return s.split.RequestOperationCancel(ctx, s.client.Writer(), operationssplitdb.RequestOperationCancelParams{
		PendingState:   string(StatePending),
		CancelledState: string(StateCancelled),
		ID:             id,
		RunningState:   string(StateRunning),
	})
}

// reapSplit is one reaping pass as a candidate read, a locking read by primary
// key and a delete, in one transaction, for the reason workqueue's claim is: the
// locks are what keep two reapers from deleting the same rows, and on MySQL they
// last until commit. See the corpus for why the lock is not taken by the first
// read.
func (s *SQLStore) reapSplit(ctx context.Context, retention time.Duration, limit int) (int64, error) {
	var reaped int64

	err := s.client.WithTransaction(ctx, func(tx database.Tx) error {
		candidates, err := s.split.SelectReapableOperations(ctx, tx, operationssplitdb.SelectReapableOperationsParams{
			SucceededState:        string(StateSucceeded),
			FailedState:           string(StateFailed),
			CancelledState:        string(StateCancelled),
			RetentionMicroseconds: retention.Microseconds(),
			ResultLimit:           int64(limit),
		})
		if err != nil {
			return err
		}

		if len(candidates) == 0 {
			return nil
		}

		ids := make([]string, 0, len(candidates))
		for i := range candidates {
			ids = append(ids, candidates[i].ID)
		}

		locked, err := s.split.LockReapableOperations(ctx, tx, operationssplitdb.LockReapableOperationsParams{
			SucceededState:        string(StateSucceeded),
			FailedState:           string(StateFailed),
			CancelledState:        string(StateCancelled),
			RetentionMicroseconds: retention.Microseconds(),
			IDs:                   ids,
		})
		if err != nil {
			return err
		}

		// Every candidate somebody else holds is theirs to delete.
		if len(locked) == 0 {
			return nil
		}

		// In id order, which is the order both reads returned them in and the
		// order the delete takes its locks in.
		doomed := make([]string, 0, len(locked))
		for i := range locked {
			doomed = append(doomed, locked[i].ID)
		}

		reaped, err = s.split.DeleteReapedOperations(ctx, tx, operationssplitdb.DeleteReapedOperationsParams{
			SucceededState:        string(StateSucceeded),
			FailedState:           string(StateFailed),
			CancelledState:        string(StateCancelled),
			RetentionMicroseconds: retention.Microseconds(),
			IDs:                   doomed,
		})

		return err
	})
	if err != nil {
		return 0, err
	}

	return reaped, nil
}

// listSplit is the paged read, in whichever direction the filter names, with the
// state filter padded into the five arguments the split listing binds.
func (s *SQLStore) listSplit(
	ctx context.Context,
	q database.SQLQueryExecutor,
	params *operationsdb.ListOperationsParams,
	filter *filtering.QueryFilter,
) ([]operationsdb.ListOperationsRow, error) {
	states := paddedStates(params.States)

	split := operationssplitdb.ListOperationsParams{
		CreatedAfter:  params.CreatedAfter,
		CreatedBefore: params.CreatedBefore,
		UpdatedAfter:  params.UpdatedAfter,
		UpdatedBefore: params.UpdatedBefore,
		Scope:         params.Scope,
		KindFilter:    params.Kind,
		State1:        states[0],
		State2:        states[1],
		State3:        states[2],
		State4:        states[3],
		State5:        states[4],
		PageCursor:    params.PageCursor,
		ResultLimit:   params.ResultLimit,
	}

	return sortedRows(filter,
		func() ([]operationsdb.ListOperationsRow, error) {
			rows, listErr := s.split.ListOperations(ctx, q, split)

			return convertRows(rows, func(r operationssplitdb.ListOperationsRow) operationsdb.ListOperationsRow {
				return operationsdb.ListOperationsRow(r)
			}), listErr
		},
		func() ([]operationssplitdb.ListOperationsDescendingRow, error) {
			return s.split.ListOperationsDescending(ctx, q, operationssplitdb.ListOperationsDescendingParams(split))
		},
		func(r operationssplitdb.ListOperationsDescendingRow) operationsdb.ListOperationsRow {
			return operationsdb.ListOperationsRow(r)
		})
}

// paddedStates fits a listing's state filter into the split listing's fixed
// arity. See queries.StateFilterArity.
//
// It reads the filter as the Postgres listing's array is read. A state outside
// the domain matches no row there, so it is dropped here, and a filter naming
// only such states binds the empty state in every slot, which no row holds. What
// remains is at most the whole domain, and is padded by repeating its first
// member: a duplicate in an IN list matches the same rows.
func paddedStates(states []string) [queries.StateFilterArity]string {
	var (
		padded   [queries.StateFilterArity]string
		distinct = make([]string, 0, queries.StateFilterArity)
	)

	for _, known := range allStates() {
		if slices.Contains(states, known) {
			distinct = append(distinct, known)
		}
	}

	if len(distinct) == 0 {
		return padded
	}

	for i := range padded {
		padded[i] = distinct[0]
		if i < len(distinct) {
			padded[i] = distinct[i]
		}
	}

	return padded
}

// convertRows is one generated row type's slice as another's.
func convertRows[From, To any](rows []From, same func(From) To) []To {
	if rows == nil {
		return nil
	}

	converted := make([]To, 0, len(rows))
	for i := range rows {
		converted = append(converted, same(rows[i]))
	}

	return converted
}

// getManySplit is the batched read on the split corpus.
func (s *SQLStore) getManySplit(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	ids []string,
) ([]operationsdb.GetOperationRow, error) {
	rows, err := s.split.GetOperations(ctx, q, operationssplitdb.GetOperationsParams{Scope: scope, IDs: ids})

	return convertRows(rows, func(r operationssplitdb.GetOperationsRow) operationsdb.GetOperationRow {
		return operationsdb.GetOperationRow(r)
	}), err
}

// strandedSplit is the recovery sweep's read on the split corpus.
func (s *SQLStore) strandedSplit(ctx context.Context, grace time.Duration, limit int) ([]operationsdb.GetOperationRow, error) {
	rows, err := s.split.ListStrandedOperations(ctx, s.client.Reader(), operationssplitdb.ListStrandedOperationsParams{
		PendingState:      string(StatePending),
		GraceMicroseconds: grace.Microseconds(),
		RunningState:      string(StateRunning),
		ResultLimit:       int64(limit),
	})

	return convertRows(rows, func(r operationssplitdb.ListStrandedOperationsRow) operationsdb.GetOperationRow {
		return operationsdb.GetOperationRow(r)
	}), err
}

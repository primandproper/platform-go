package database_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v5/database"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// rollbackRecorder mimics a Client.RollbackTransaction: it rolls the transaction back
// (satisfying sqlmock's ExpectRollback) and records how many times it was invoked.
type rollbackRecorder struct {
	calls int
}

func (r *rollbackRecorder) rollback(_ context.Context, tx database.SQLQueryExecutorAndTransactionManager) {
	r.calls++
	_ = tx.Rollback()
}

func newRunInTxTestDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()

	db, mock, err := sqlmock.New()
	must.NoError(t, err)

	return db, mock
}

func TestRunInTransaction(T *testing.T) {
	T.Parallel()

	T.Run("commits when fn returns nil", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		db, mock := newRunInTxTestDB(t)
		rb := &rollbackRecorder{}

		mock.ExpectBegin()
		mock.ExpectExec("UPDATE things").WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()

		var gotTx database.SQLQueryExecutorAndTransactionManager
		err := database.RunInTransaction(ctx, db, rb.rollback, func(tx database.SQLQueryExecutorAndTransactionManager) error {
			gotTx = tx
			_, execErr := tx.ExecContext(ctx, "UPDATE things SET x = 1")
			return execErr
		})

		test.NoError(t, err)
		test.NotNil(t, gotTx)
		test.EqOp(t, 0, rb.calls)
		must.NoError(t, mock.ExpectationsWereMet())
	})

	T.Run("rolls back and returns the error when fn fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		db, mock := newRunInTxTestDB(t)
		rb := &rollbackRecorder{}

		mock.ExpectBegin()
		mock.ExpectRollback()

		sentinel := errors.New("fn failed")
		err := database.RunInTransaction(ctx, db, rb.rollback, func(_ database.SQLQueryExecutorAndTransactionManager) error {
			return sentinel
		})

		test.ErrorIs(t, err, sentinel)
		test.EqOp(t, 1, rb.calls)
		must.NoError(t, mock.ExpectationsWereMet())
	})

	T.Run("wraps begin errors without invoking fn or rollback", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		db, mock := newRunInTxTestDB(t)
		rb := &rollbackRecorder{}

		beginErr := errors.New("cannot begin")
		mock.ExpectBegin().WillReturnError(beginErr)

		fnCalled := false
		err := database.RunInTransaction(ctx, db, rb.rollback, func(_ database.SQLQueryExecutorAndTransactionManager) error {
			fnCalled = true
			return nil
		})

		test.ErrorIs(t, err, beginErr)
		test.StrContains(t, err.Error(), "beginning transaction")
		test.False(t, fnCalled)
		test.EqOp(t, 0, rb.calls)
		must.NoError(t, mock.ExpectationsWereMet())
	})

	T.Run("wraps commit errors and does not roll back again", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		db, mock := newRunInTxTestDB(t)
		rb := &rollbackRecorder{}

		commitErr := errors.New("cannot commit")
		mock.ExpectBegin()
		mock.ExpectCommit().WillReturnError(commitErr)

		err := database.RunInTransaction(ctx, db, rb.rollback, func(_ database.SQLQueryExecutorAndTransactionManager) error {
			return nil
		})

		test.ErrorIs(t, err, commitErr)
		test.StrContains(t, err.Error(), "committing transaction")
		// A failed commit already released the connection, so a second rollback would only
		// surface a spurious ErrTxDone.
		test.EqOp(t, 0, rb.calls)
		must.NoError(t, mock.ExpectationsWereMet())
	})

	T.Run("rolls back and re-panics when fn panics", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		db, mock := newRunInTxTestDB(t)
		rb := &rollbackRecorder{}

		mock.ExpectBegin()
		mock.ExpectRollback()

		recovered := func() (r any) {
			defer func() { r = recover() }()
			_ = database.RunInTransaction(ctx, db, rb.rollback, func(_ database.SQLQueryExecutorAndTransactionManager) error {
				panic("boom")
			})
			return nil
		}()

		test.EqOp(t, "boom", recovered)
		test.EqOp(t, 1, rb.calls)
		must.NoError(t, mock.ExpectationsWereMet())
	})

	T.Run("returns an error for nil dependencies", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		db, _ := newRunInTxTestDB(t)
		rb := &rollbackRecorder{}
		noopFn := func(_ database.SQLQueryExecutorAndTransactionManager) error { return nil }

		test.Error(t, database.RunInTransaction(ctx, nil, rb.rollback, noopFn))
		test.Error(t, database.RunInTransaction(ctx, db, nil, noopFn))
		test.Error(t, database.RunInTransaction(ctx, db, rb.rollback, nil))
	})
}

package database_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v5/database"
	mockdatabase "github.com/primandproper/platform-go/v5/database/mock"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// buildTransactionTestClient returns a mock Client backed by a go-sqlmock database.
// WriteDB and ReadDB both return the mocked pool, and RollbackTransaction behaves like
// the real implementations (it rolls the transaction back) so sqlmock expectations for
// rollbacks are satisfied.
func buildTransactionTestClient(t *testing.T) (*mockdatabase.ClientMock, sqlmock.Sqlmock) {
	t.Helper()

	fakeDB, sqlMock, err := sqlmock.New()
	must.NoError(t, err)

	client := &mockdatabase.ClientMock{
		WriteDBFunc: func() *sql.DB { return fakeDB },
		ReadDBFunc:  func() *sql.DB { return fakeDB },
		RollbackTransactionFunc: func(_ context.Context, tx database.SQLQueryExecutorAndTransactionManager) {
			_ = tx.Rollback()
		},
	}

	return client, sqlMock
}

func TestWithTransaction(T *testing.T) {
	T.Parallel()

	T.Run("commits when fn returns nil", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		client, sqlMock := buildTransactionTestClient(t)

		sqlMock.ExpectBegin()
		sqlMock.ExpectExec("UPDATE things").WillReturnResult(sqlmock.NewResult(1, 1))
		sqlMock.ExpectCommit()

		var gotTx database.SQLQueryExecutorAndTransactionManager
		err := database.WithTransaction(ctx, client, func(tx database.SQLQueryExecutorAndTransactionManager) error {
			gotTx = tx
			_, execErr := tx.ExecContext(ctx, "UPDATE things SET x = 1")
			return execErr
		})

		test.NoError(t, err)
		test.NotNil(t, gotTx)
		test.SliceEmpty(t, client.RollbackTransactionCalls())
		must.NoError(t, sqlMock.ExpectationsWereMet())
	})

	T.Run("rolls back and returns the error when fn fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		client, sqlMock := buildTransactionTestClient(t)

		sqlMock.ExpectBegin()
		sqlMock.ExpectRollback()

		sentinel := errors.New("fn failed")
		err := database.WithTransaction(ctx, client, func(_ database.SQLQueryExecutorAndTransactionManager) error {
			return sentinel
		})

		test.ErrorIs(t, err, sentinel)
		test.SliceLen(t, 1, client.RollbackTransactionCalls())
		must.NoError(t, sqlMock.ExpectationsWereMet())
	})

	T.Run("wraps and returns begin errors without invoking fn", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		client, sqlMock := buildTransactionTestClient(t)

		beginErr := errors.New("cannot begin")
		sqlMock.ExpectBegin().WillReturnError(beginErr)

		fnCalled := false
		err := database.WithTransaction(ctx, client, func(_ database.SQLQueryExecutorAndTransactionManager) error {
			fnCalled = true
			return nil
		})

		test.ErrorIs(t, err, beginErr)
		test.StrContains(t, err.Error(), "beginning transaction")
		test.False(t, fnCalled)
		test.SliceEmpty(t, client.RollbackTransactionCalls())
		must.NoError(t, sqlMock.ExpectationsWereMet())
	})

	T.Run("wraps commit errors", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		client, sqlMock := buildTransactionTestClient(t)

		commitErr := errors.New("cannot commit")
		sqlMock.ExpectBegin()
		sqlMock.ExpectCommit().WillReturnError(commitErr)

		err := database.WithTransaction(ctx, client, func(_ database.SQLQueryExecutorAndTransactionManager) error {
			return nil
		})

		test.ErrorIs(t, err, commitErr)
		test.StrContains(t, err.Error(), "committing transaction")
		// A failed commit already releases the connection, so we must not roll back again.
		test.SliceEmpty(t, client.RollbackTransactionCalls())
		must.NoError(t, sqlMock.ExpectationsWereMet())
	})

	T.Run("rolls back and re-panics when fn panics", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		client, sqlMock := buildTransactionTestClient(t)

		sqlMock.ExpectBegin()
		sqlMock.ExpectRollback()

		recovered := func() (r any) {
			defer func() { r = recover() }()
			_ = database.WithTransaction(ctx, client, func(_ database.SQLQueryExecutorAndTransactionManager) error {
				panic("boom")
			})
			return nil
		}()

		test.EqOp(t, "boom", recovered)
		test.SliceLen(t, 1, client.RollbackTransactionCalls())
		must.NoError(t, sqlMock.ExpectationsWereMet())
	})

	T.Run("returns an error for a nil client", func(t *testing.T) {
		t.Parallel()

		err := database.WithTransaction(t.Context(), nil, func(_ database.SQLQueryExecutorAndTransactionManager) error {
			return nil
		})

		test.Error(t, err)
	})

	T.Run("returns an error for a nil fn", func(t *testing.T) {
		t.Parallel()

		client, _ := buildTransactionTestClient(t)

		err := database.WithTransaction(t.Context(), client, nil)

		test.Error(t, err)
	})
}

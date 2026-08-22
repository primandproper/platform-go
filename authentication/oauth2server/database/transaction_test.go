package database

import (
	"context"
	"database/sql"
	stderrors "errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	databasemock "github.com/primandproper/platform-go/v13/database/mock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errStatementFailed is what the executors below answer with.
var errStatementFailed = platformerrors.New("connection reset by peer")

// failingClient is a client whose transactions open and whose statements do not.
//
// A closed database fails at BeginTx, before any callback runs, so it cannot
// reach the branch each of these methods has *inside* its transaction — and
// those are the ones that matter: a statement that fails halfway through a
// family revocation or a four-table sweep is the case the transaction exists
// for.
func failingClient(t *testing.T, result sql.Result, execErr error) database.Client {
	t.Helper()

	executor := &databasemock.SQLQueryExecutorMock{
		ExecContextFunc: func(context.Context, string, ...any) (sql.Result, error) {
			return result, execErr
		},
	}

	return &databasemock.ClientMock{
		DialectFunc: func() dialect.Dialect { return dialect.SQLite },
		WriterFunc:  func() database.SQLQueryExecutor { return executor },
		ReaderFunc:  func() database.SQLQueryExecutor { return executor },
		WithTransactionFunc: func(_ context.Context, fn func(database.SQLQueryExecutor) error) error {
			return fn(executor)
		},
		CloseFunc: func() error { return nil },
	}
}

func TestStore_StatementFailuresInsideTransactions(T *testing.T) {
	T.Parallel()

	T.Run("every transaction reports the statement that failed", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		store, err := NewStore(&Config{}, failingClient(t, nil, errStatementFailed))
		must.NoError(t, err)

		// Each of these opens a transaction and fails on its first statement,
		// which is a different line in each method and the same outcome: the
		// transaction is rolled back and the caller is told, rather than being
		// handed a partial result.
		_, err = store.ConsumeAuthorizationCode(ctx, oauth2server.Hash("c"))
		test.Error(t, err)

		_, err = store.ConsumeRefreshToken(ctx, oauth2server.Hash("r"))
		test.Error(t, err)

		revoked, err := store.RevokeFamily(ctx, "family")
		test.Error(t, err)
		test.EqOp(t, int64(0), revoked)

		// A partial sweep is representable and a partial count is not reported:
		// zero, and the error.
		swept, err := store.Sweep(ctx, time.Now().UTC())
		test.Error(t, err)
		test.EqOp(t, int64(0), swept)
	})

	T.Run("a sweep that fails on the clients table reports too", func(t *testing.T) {
		t.Parallel()

		// Clients are swept by their own statement, after the other three, so a
		// failure there is a separate branch — and one a test that only broke
		// the first statement would never reach.
		var calls int

		executor := &databasemock.SQLQueryExecutorMock{
			ExecContextFunc: func(context.Context, string, ...any) (sql.Result, error) {
				calls++
				if calls > 3 {
					return nil, errStatementFailed
				}

				return sweptResult{}, nil
			},
		}

		client := &databasemock.ClientMock{
			DialectFunc: func() dialect.Dialect { return dialect.SQLite },
			WriterFunc:  func() database.SQLQueryExecutor { return executor },
			WithTransactionFunc: func(_ context.Context, fn func(database.SQLQueryExecutor) error) error {
				return fn(executor)
			},
		}

		store, err := NewStore(&Config{}, client)
		must.NoError(t, err)

		swept, err := store.Sweep(t.Context(), time.Now().UTC())
		test.Error(t, err)
		test.EqOp(t, int64(0), swept)
	})

	T.Run("a result that cannot count its own rows is an error", func(t *testing.T) {
		t.Parallel()

		// Every one of this store's outcomes is decided by a row count —
		// ErrRecordExists, ErrAlreadyRedeemed, how much a sweep removed — so a
		// driver that cannot supply one has to report rather than be read as
		// zero, which is the value that means "somebody else already had this".
		store, err := NewStore(&Config{}, failingClient(t, uncountableResult{}, nil))
		must.NoError(t, err)

		err = store.CreateClient(t.Context(), &oauth2server.Client{
			CreatedAt: time.Now().UTC(),
			ID:        "uncountable",
		})
		test.Error(t, err)
		test.False(t, stderrors.Is(err, oauth2server.ErrClientExists))
	})
}

// sweptResult is a statement that removed one row.
type sweptResult struct{}

func (sweptResult) LastInsertId() (int64, error) { return 0, nil }
func (sweptResult) RowsAffected() (int64, error) { return 1, nil }

// uncountableResult is a statement that succeeded and cannot say how much it
// touched, which some drivers genuinely do.
type uncountableResult struct{}

func (uncountableResult) LastInsertId() (int64, error) { return 0, nil }
func (uncountableResult) RowsAffected() (int64, error) {
	return 0, platformerrors.New("this driver does not support RowsAffected")
}

func TestNewStore_Dialect(T *testing.T) {
	T.Parallel()

	T.Run("refuses a client whose dialect this package cannot render SQL for", func(t *testing.T) {
		t.Parallel()

		// Every statement here is rendered per dialect — the bind markers, the
		// insert-ignore clause — so a dialect with no rendering would produce
		// syntactically valid SQL that the server rejects at runtime.
		store, err := NewStore(&Config{}, &databasemock.ClientMock{
			DialectFunc: func() dialect.Dialect { return dialect.Dialect("cockroach") },
		})

		test.Nil(t, store)
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}

// The sweeper's own failure path: nothing is waiting on that goroutine, so a
// failed sweep has nowhere to go but the log.
func TestSweepEvery_FailedSweep(T *testing.T) {
	T.Parallel()

	T.Run("keeps ticking after a sweep that failed", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			var calls int

			executor := &databasemock.SQLQueryExecutorMock{
				ExecContextFunc: func(context.Context, string, ...any) (sql.Result, error) {
					calls++

					return nil, errStatementFailed
				},
			}

			client := &databasemock.ClientMock{
				DialectFunc: func() dialect.Dialect { return dialect.SQLite },
				WriterFunc:  func() database.SQLQueryExecutor { return executor },
				WithTransactionFunc: func(_ context.Context, fn func(database.SQLQueryExecutor) error) error {
					return fn(executor)
				},
			}

			store, err := NewStore(&Config{}, client, WithSweeper(t.Context(), 10*time.Second))
			must.NoError(t, err)
			test.NotNil(t, store)

			time.Sleep(35 * time.Second)
			synctest.Wait()

			// Three ticks, three attempts. A sweeper that returned on the first
			// failure would leave four tables growing for the life of the
			// process, and the only sign would be the absence of a log line.
			test.GreaterEq(t, 3, calls)
		})
	})
}

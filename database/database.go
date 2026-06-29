package database

import (
	"context"
	"database/sql"
	"io"
	"time"

	platformerrors "github.com/primandproper/platform-go/v2/errors"
)

var (
	// ErrDatabaseNotReady indicates the given database is not ready.
	ErrDatabaseNotReady = platformerrors.New("database is not ready yet")
)

type (
	// Scanner represents any database response (i.e. sql.Row[s]).
	Scanner interface {
		Scan(dest ...any) error
	}

	// ResultIterator represents any iterable database response (i.e. sql.Rows).
	ResultIterator interface {
		Next() bool
		Err() error
		Scanner
		io.Closer
	}

	// SQLQueryExecutor is a subset interface for sql.{DB|Tx} objects.
	SQLQueryExecutor interface {
		ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
		PrepareContext(context.Context, string) (*sql.Stmt, error)
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	}

	// SQLTransactionManager is a subset interface for sql.{DB|Tx} objects.
	SQLTransactionManager interface {
		Rollback() error
	}

	// SQLQueryExecutorAndTransactionManager is a subset interface for sql.{DB|Tx} objects.
	SQLQueryExecutorAndTransactionManager interface {
		SQLQueryExecutor
		SQLTransactionManager
	}

	Client interface {
		WriteDB() *sql.DB
		ReadDB() *sql.DB
		Close() error
		CurrentTime() time.Time
		RollbackTransaction(ctx context.Context, tx SQLQueryExecutorAndTransactionManager)
	}
)

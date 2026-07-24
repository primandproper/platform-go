package database

import (
	"context"
	"database/sql"
	"io"
	"time"

	platformerrors "github.com/primandproper/platform-go/v5/errors"
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

	// Client is the safe surface for database access. It deliberately does not expose a
	// raw *sql.DB: reads and single-statement writes go through the narrow executors
	// returned by Reader and Writer (which cannot begin a transaction), and all
	// transactional work goes through WithTransaction. A transaction is therefore
	// unreachable except via WithTransaction, so statements cannot accidentally run
	// outside a transaction or against the read replica.
	//
	// Callers that genuinely need the concrete pool (migrations, session-pinned advisory
	// locks, driver features off this seam) can obtain it via the RawAccess capability.
	Client interface {
		// Reader returns an executor for the read database. It exposes no transaction
		// control by design; use WithTransaction for anything transactional.
		Reader() SQLQueryExecutor
		// Writer returns an executor for the write database, for single, non-transactional
		// statements. Multi-statement work belongs in WithTransaction.
		Writer() SQLQueryExecutor
		// WithTransaction begins a transaction on the write database, invokes fn with it as
		// the sole executor, commits on a nil return, and rolls back on error or panic.
		WithTransaction(ctx context.Context, fn func(tx SQLQueryExecutorAndTransactionManager) error) error
		Close() error
		CurrentTime() time.Time
	}

	// RawAccess is an optional capability exposing the concrete *sql.DB pools for callers
	// that genuinely need them — schema migrations, session-pinned advisory locks, or
	// driver features outside the executor seam. A caller obtains it by asserting on a
	// Client:
	//
	//	raw, ok := client.(database.RawAccess)
	//
	// Reaching for RawAccess is a deliberate step outside the safe Client surface; prefer
	// Reader, Writer, and WithTransaction wherever they suffice.
	RawAccess interface {
		ReadDB() *sql.DB
		WriteDB() *sql.DB
	}
)

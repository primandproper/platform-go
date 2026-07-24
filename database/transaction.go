package database

import (
	"context"
	"database/sql"

	platformerrors "github.com/primandproper/platform-go/v6/errors"
)

// RunInTransaction begins a transaction on writeDB, invokes fn with that transaction as
// the sole query executor, and commits when fn returns nil. It is the shared engine
// behind each Client's WithTransaction method — application code should prefer
// Client.WithTransaction, which wraps this with the implementation's observability.
//
// The transaction handle is the only executor fn receives, so statements cannot
// accidentally target the read replica or another connection. Lifecycle is managed for
// the caller:
//
//   - rollback is invoked (with the transaction) on any non-nil error from fn, and the
//     error is returned unwrapped.
//   - a panic inside fn triggers rollback and is then re-raised, so no connection leaks
//     and the caller still observes the failure.
//   - a nil return from fn commits; commit errors are wrapped and returned.
//
// A failed commit has already released the connection back to the pool, so no second
// rollback is attempted (it would only surface a spurious ErrTxDone).
func RunInTransaction(
	ctx context.Context,
	writeDB *sql.DB,
	rollback func(ctx context.Context, tx SQLQueryExecutorAndTransactionManager),
	fn func(tx SQLQueryExecutorAndTransactionManager) error,
) error {
	if writeDB == nil || rollback == nil || fn == nil {
		return platformerrors.ErrNilInputProvided
	}

	tx, err := writeDB.BeginTx(ctx, nil)
	if err != nil {
		return platformerrors.Wrap(err, "beginning transaction")
	}

	// Roll back on panic and re-raise so the caller still sees the failure and the
	// pooled connection is not leaked.
	defer func() {
		if r := recover(); r != nil {
			rollback(ctx, tx)
			panic(r)
		}
	}()

	if fnErr := fn(tx); fnErr != nil {
		rollback(ctx, tx)
		return fnErr
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return platformerrors.Wrap(commitErr, "committing transaction")
	}

	return nil
}

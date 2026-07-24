package database

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v5/errors"
)

// WithTransaction begins a transaction on the write database, invokes fn with that
// transaction as the sole query executor, and commits when fn returns nil.
//
// The transaction handle is the only executor fn receives, so statements cannot
// accidentally target the read replica or another connection. The lifecycle is
// managed for the caller:
//
//   - fn returning a non-nil error triggers a rollback (via Client.RollbackTransaction)
//     and the error is returned to the caller unwrapped.
//   - a panic inside fn triggers a rollback and is then re-panicked, so no connection
//     leaks and the caller still observes the failure.
//   - a nil return from fn commits; commit errors are wrapped and returned.
//
// Begin, commit, and rollback are instrumented by the otelsql-wrapped driver, and
// RollbackTransaction records its own span, so the transaction lifecycle is traced
// consistently with the rest of the package.
func WithTransaction(ctx context.Context, db Client, fn func(tx SQLQueryExecutorAndTransactionManager) error) error {
	if db == nil || fn == nil {
		return platformerrors.ErrNilInputProvided
	}

	tx, err := db.WriteDB().BeginTx(ctx, nil)
	if err != nil {
		return platformerrors.Wrap(err, "beginning transaction")
	}

	// Roll back on panic and re-panic so the caller still sees the failure and the
	// pooled connection is not leaked.
	defer func() {
		if r := recover(); r != nil {
			db.RollbackTransaction(ctx, tx)
			panic(r)
		}
	}()

	if fnErr := fn(tx); fnErr != nil {
		db.RollbackTransaction(ctx, tx)
		return fnErr
	}

	if commitErr := tx.Commit(); commitErr != nil {
		// A failed Commit has already released the connection back to the pool, so a
		// second rollback here would only surface a spurious ErrTxDone.
		return platformerrors.Wrap(commitErr, "committing transaction")
	}

	return nil
}

// Package sqlclient holds the parts of a database.Client that do not vary by
// SQL driver.
//
// The postgres, mysql and sqlite clients differ in how they open a connection —
// the DSN, the pool type, the pragmas — and in nothing that happens afterwards.
// Readiness, close, rollback and the transaction wrapper were the same code in
// all three, which meant a fix landed in one of them and the other two kept the
// bug: mysql carries an annotation on a copied postgres fix, and sqlite's
// readiness probe logged the connection string where its siblings logged a
// "read"/"write" label.
//
// That last one is why this is a bug fix rather than a tidy-up. A connection
// string is a credential. sqlite's happens not to carry one, so the line was
// harmless where it was written and would not have been anywhere else — and a
// copied line does not stay where it was written.
package sqlclient

import (
	"context"
	"database/sql"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
)

// ClosePools releases whatever was opened, for the failure paths after a
// successful connect. Read and write may be the same handle when only one
// connection string is configured, so each is closed once. Close failures are
// joined onto cause rather than replacing it: cause is why the caller is
// unwinding, and a close error found on the way out is additional information,
// not a better answer.
func ClosePools(cause error, readDB, writeDB *sql.DB) error {
	if readDB != nil {
		if closeErr := readDB.Close(); closeErr != nil {
			cause = errors.Join(cause, errors.Wrap(closeErr, "closing read database"))
		}
	}

	if writeDB != nil && writeDB != readDB {
		if closeErr := writeDB.Close(); closeErr != nil {
			cause = errors.Join(cause, errors.Wrap(closeErr, "closing write database"))
		}
	}

	return cause
}

// Close closes both database/sql handles, logging each failure and returning
// them joined. The write handle is closed even when the read handle failed, so
// a read-close error cannot leak the write connection.
//
// A driver with pools underneath the database/sql layer closes those after this
// returns, so the connections drain back before the pool waits on them.
func Close(o11y observability.Observer, readDB, writeDB *sql.DB) error {
	var errs error

	logger := o11y.Logger()

	if err := readDB.Close(); err != nil {
		logger.Error("closing read database connection", err)
		errs = errors.Join(errs, err)
	}

	if writeDB != readDB {
		if err := writeDB.Close(); err != nil {
			logger.Error("closing write database connection", err)
			errs = errors.Join(errs, err)
		}
	}

	return errs
}

// IsReady reports whether both handles answer a ping within the config's
// attempt budget. A single connection string means one handle serving both
// roles, which is pinged once.
func IsReady(ctx context.Context, op observability.Operation, cfg database.ClientConfig, readDB, writeDB *sql.DB) bool {
	maxAttempts := int(cfg.GetMaxPingAttempts())
	waitPeriod := cfg.GetPingWaitPeriod()

	op.Set("db.ping.max_attempts", maxAttempts).Set("db.ping.wait_period", waitPeriod)

	if !WaitForPing(ctx, op, readDB, "read", maxAttempts, waitPeriod) {
		return false
	}

	if writeDB != readDB {
		return WaitForPing(ctx, op, writeDB, "write", maxAttempts, waitPeriod)
	}

	return true
}

// WaitForPing pings db until it answers, maxAttempts is spent, or ctx is done.
//
// connectionName labels the log lines and is the role the handle serves —
// "read" or "write". It is deliberately not the connection string: a DSN is a
// credential for every driver but sqlite, and a readiness probe that fails
// repeatedly is exactly the situation that fills a log with its own value.
func WaitForPing(
	ctx context.Context,
	op observability.Operation,
	db *sql.DB,
	connectionName string,
	maxAttempts int,
	waitPeriod time.Duration,
) bool {
	logger := op.Logger().WithValue("connection", connectionName)

	for attemptCount := range maxAttempts {
		if err := db.PingContext(ctx); err == nil {
			return true
		}

		logger.WithValue("attempt_count", attemptCount).Info("ping failed, waiting for db")

		// Don't sleep after the final attempt, and abort promptly if the caller's
		// context is canceled rather than sleeping through it.
		if attemptCount == maxAttempts-1 {
			break
		}

		select {
		case <-ctx.Done():
			return false
		case <-time.After(waitPeriod):
		}
	}

	return false
}

// WithTransaction runs fn inside a transaction on writeDB under a span of its
// own, committing on a nil return and rolling back on error or panic. See
// database.RunInTransaction.
func WithTransaction(
	ctx context.Context,
	o11y observability.Observer,
	writeDB *sql.DB,
	rollback func(ctx context.Context, tx database.SQLQueryExecutorAndTransactionManager),
	fn func(tx database.SQLQueryExecutor) error,
) error {
	ctx, op := o11y.Begin(ctx)
	defer op.End()

	return database.RunInTransaction(ctx, writeDB, rollback, fn)
}

// RollbackTransaction rolls tx back, recording a failure on the span rather
// than returning it. A rollback error reaches no caller who could act on it:
// the transaction is already being abandoned, and the connection is poisoned or
// it is not.
func RollbackTransaction(ctx context.Context, o11y observability.Observer, tx database.SQLQueryExecutorAndTransactionManager) {
	_, op := o11y.Begin(ctx)
	defer op.End()

	op.Logger().Debug("rolling back transaction")

	if err := tx.Rollback(); err != nil {
		op.Acknowledge(err, "rolling back transaction")
	}

	op.Logger().Debug("transaction rolled back")
}

// Now reads the clock a client was built with, falling back to the wall clock.
// A nil timeFunc is the ordinary case — only a test injects one.
func Now(timeFunc func() time.Time) time.Time {
	if timeFunc == nil {
		return time.Now()
	}

	return timeFunc()
}

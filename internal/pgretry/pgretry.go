// Package pgretry re-runs a Postgres write that failed for one of the two
// reasons Postgres resolves by asking the caller to run it again.
//
// Two packages in this module — workqueue and timers — own a table that several
// processes claim rows out of, and both had written the same loop by hand: the
// SQLSTATE pair, the retryable test, the attempt-bounded loop, and the bounded
// rendering of a cause into a nullable last_error column. The copies were
// identical down to the doc comments, which is the argument for one home: the
// next transient SQLSTATE worth surviving should be added once, not found in
// two places by whoever notices the first copy.
//
// It is deliberately about Postgres. The condition it recognizes is a class 40
// SQLSTATE reported by pgconn, and a caller running against MySQL or SQLite has
// neither those codes nor this failure mode.
package pgretry

import (
	"context"
	stderrors "errors"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/metric"
)

// The two class 40 SQLSTATEs Postgres resolves by asking the caller to re-run
// the whole statement. Anything else — a constraint violation, a dead
// connection — is the caller's problem and must not be retried.
const (
	pgSerializationFailure = "40001"
	pgDeadlockDetected     = "40P01"
)

// MaxStoredErrLen bounds what goes into a last_error column, so a pathological
// driver error cannot bloat the row.
const MaxStoredErrLen = 1024

// IsRetryable reports whether err is one of the two transient class 40
// conditions Postgres resolves by re-running the statement.
func IsRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !stderrors.As(err, &pgErr) {
		return false
	}

	return pgErr.Code == pgDeadlockDetected || pgErr.Code == pgSerializationFailure
}

// TruncateError renders a cause for a nullable last_error column, bounded.
//
// It returns any rather than string because a nil cause has to render as NULL
// rather than as the empty string: the row distinguishes "has not failed" from
// "failed", and "" would collapse the two. The bounding itself is
// platformerrors.TruncateError, so the cut stays on a rune boundary.
func TruncateError(err error) any {
	if err == nil {
		return nil
	}

	return platformerrors.TruncateError(err, MaxStoredErrLen)
}

// Retrier re-runs a write for as long as Postgres keeps reporting a retryable
// condition, up to Attempts times.
//
// Both of this module's users hold their tables under an ordered locking
// discipline, which makes a deadlock between two of their own writers
// impossible — so in a database they have to themselves these retries never
// fire. They exist for the case they do not: a consumer whose own statements
// touch those rows, through a foreign key from their domain table or a bulk
// cleanup, reintroduces exactly the cycle the ordering removed, and a retried
// deadlock is invisible where an unretried one is a failed request.
//
// The zero value is usable and retries nothing, which is the honest reading of
// "no attempt budget was configured".
type Retrier struct {
	// Logger is where a retry is reported. An absent one logs nowhere.
	Logger logging.Logger

	// Counter counts retries, so a table that has started contending is visible
	// before anyone reads a log. An absent one records nothing.
	Counter metrics.Int64Counter

	// AttemptKey names the log field carrying the attempt number. It is the
	// caller's, because the surrounding package's keys are namespaced to it.
	AttemptKey string

	// Subject names what is being written, in the log line: "work queue",
	// "timer".
	Subject string

	// AddOptions are the attributes Counter's measurements carry — the caller's
	// queue or set name, so one process running several does not collapse them.
	AddOptions []metric.AddOption

	// Attempts bounds how many times a write runs in total, the first included.
	// A value below 2 runs the write once and returns whatever it got.
	Attempts uint
}

// Do runs fn, re-running it while Postgres keeps asking for a retry.
//
// label says which write this is, and reaches the log line rather than the
// counter: it names an operation the caller already spans, and as a metric
// dimension it would multiply every queue's series by its statement count for
// an answer the trace already gives.
func (r *Retrier) Do(ctx context.Context, label string, fn func() error) error {
	var err error

	for attempt := uint(1); ; attempt++ {
		if err = fn(); err == nil {
			return nil
		}

		if attempt >= r.Attempts || !IsRetryable(err) {
			return err
		}

		if r.Counter != nil {
			r.Counter.Add(ctx, 1, r.AddOptions...)
		}

		if r.Logger != nil {
			r.Logger.WithValues(map[string]any{
				r.AttemptKey: attempt,
				"operation":  label,
			}).Info("retrying " + r.Subject + " write after a serialization failure")
		}
	}
}

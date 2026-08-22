// Package sqlguard runs the guarded write four durable-state packages in this
// module are built on, and says what it means when the guard matches nothing.
//
// A guarded write is an UPDATE whose WHERE clause encodes the state its caller
// believed the row was in — "finish this operation, if it is still running",
// "advance this saga, if it is still on the step I read". Matching zero rows is
// not an error the driver reports; it is a success that recorded nothing, and
// the whole reason the guard is in the statement is to notice. operations,
// dataprivacy, saga and metering had each written the same seven-step handling
// of that by hand: exec, read RowsAffected, stamp the span, and on zero mark
// the span, count the miss, log the identifier, and return a wrapped sentinel.
//
// The copies had already begun to differ — metering logs no identifier and
// returns an ad-hoc error where its siblings return a package sentinel — which
// is the argument for one home. What varies between callers is what the row is
// called and what a missed guard means to that caller, and that is exactly what
// a Guard carries.
package sqlguard

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Span and metric attribute suffixes, appended to a Guard's Namespace.
//
// They are derived rather than configured so that the four callers cannot drift
// on what the same fact is called. A dashboard reading guard misses across a
// deployment groups on one suffix; a package that spelled it `guard_miss` would
// be invisible in that panel while looking instrumented from inside.
const (
	rowsAffectedSuffix = ".rows_affected"
	missedSuffix       = ".guard_missed"
	storeOpSuffix      = ".store_operation"
)

// Guard describes one package's guarded writes: what the row is called, and
// what it means when the guard matches nothing.
//
// The zero value works and is silent — no counter, no identifier in the line —
// which is the honest behavior for a caller that has not said what a miss
// means. Everything it does say is a field rather than a parameter because it
// is fixed for the lifetime of a store, while the query and the identifier
// change per call.
type Guard struct {
	// MissCounter counts guards that matched nothing, labeled by the operation
	// that missed. An absent one records nothing.
	MissCounter metrics.Int64Counter

	// NotFound is the sentinel a missed guard returns, wrapped with Reason so
	// the caller keeps a matchable error and a readable message. An absent one
	// leaves Reason as the whole error.
	NotFound error

	// Namespace prefixes the span and metric attributes this writes — the
	// owning package's name, as it appears in its other keys: "operations",
	// "dataprivacy", "saga", "metering".
	Namespace string

	// IDKey names the log field carrying the row's identifier. Empty logs no
	// identifier, for a guard that is not keyed by one.
	IDKey string

	// Message is the line logged when the guard matches nothing. It is logged
	// rather than merely counted because this one is worth reading: the work
	// already ran — the export is written, the charge is posted — and the row
	// that should record it has moved on without us.
	Message string

	// Reason renders the returned error, as a format taking the identifier:
	// "saga instance %q is no longer advanceable".
	Reason string
}

// OpAttr labels a measurement with the store operation that produced it, so a
// guard miss distinguishes a caller cancelling twice from a worker losing a
// lease race — one is routine and one wants looking at.
//
// It is exported because not every guard is a guarded UPDATE. A caller that
// reads a row, finds it in the wrong state, and reports the miss itself wants
// the same attribute on the same counter, and two spellings of it would put
// half a package's misses in a series nothing groups with the other half.
func (g *Guard) OpAttr(operation string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(g.Namespace+storeOpSuffix, operation))
}

// Exec runs a guarded write and reports one that matched nothing.
//
// id identifies the row for the log line and the returned error; operation
// labels the miss counter — "finish", "advance", "release" — so a guard miss
// distinguishes a caller cancelling twice from a worker losing a lease race,
// one of which is routine and one of which wants looking at. description names
// the write in whatever error the driver returns.
func (g *Guard) Exec(
	ctx context.Context,
	op observability.Operation,
	q database.SQLQueryExecutor,
	query string,
	args []any,
	id, operation, description string,
) error {
	result, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return op.Error(err, "%s", description)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return op.Error(err, "reading result of %s", description)
	}

	op.Set(g.Namespace+rowsAffectedSuffix, affected)

	if affected > 0 {
		return nil
	}

	op.Set(g.Namespace+missedSuffix, true)

	if g.MissCounter != nil {
		g.MissCounter.Add(ctx, 1, g.OpAttr(operation))
	}

	logger := op.Logger()
	if g.IDKey != "" {
		logger = logger.WithValue(g.IDKey, id)
	}

	logger.Info(g.Message)

	if g.NotFound == nil {
		return platformerrors.Newf(g.Reason, id)
	}

	return platformerrors.Wrapf(g.NotFound, g.Reason, id)
}

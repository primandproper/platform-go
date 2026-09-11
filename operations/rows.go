package operations

import (
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v14/operations/internal/operationsdb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The typed seam between the generated package and this package's own types.
//
// operations/internal/operationsdb is sqlc-gen-unison's output: one params and
// one row struct per statement. These functions are the whole of what this
// package does with them — a row becomes an Operation, a domain value becomes
// the params — and every one is a struct literal on purpose. A renamed or
// retyped column changes the generated struct, and every conversion here stops
// compiling; the scan-by-position pairing this replaced reported the same
// mistake as a runtime scan error, or worse, as two same-typed columns silently
// transposed.
//
// Six statements project the same list — the scoped get, the unscoped one the
// cancellation reads back through, the batched get, the create's read-back, the
// claim's read-back, and the recovery sweep — so their row types are six names
// for one shape, and the conversions between them are Go's own.
// That is the assertion rather than a shortcut around one: the day two of those
// projections stop being identical in field name, type or order, this file stops
// building rather than filling the wrong fields. The listing is the one that
// genuinely differs, because it carries the two counts beside the row.

// utcTime normalizes a timestamp to UTC. Postgres hands a value back in the
// session's zone, so a caller comparing two of them, or rendering one into
// JSON, would otherwise get an answer that depends on where the row was read.
func utcTime(t time.Time) time.Time { return t.UTC() }

// utcPtr is utcTime for the optional stamps, preserving absence.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// rawMessage renders an encoded column as the JSON this package hands back,
// preserving the difference between absent and empty.
//
// It is copied rather than aliased. The value escapes into an Operation that
// outlives the row it came from, and what a driver did with the array behind a
// []byte is the driver's business rather than something this package can
// promise on a caller's behalf.
func rawMessage(encoded []byte) json.RawMessage {
	if len(encoded) == 0 {
		return nil
	}

	return json.RawMessage(append([]byte(nil), encoded...))
}

// unitsTotal narrows the nullable denominator. Nil stays nil: "there is no
// denominator" is a fact about the work that a zero could not distinguish from
// "there are no units yet".
func unitsTotal(total *int64) *int {
	if total == nil {
		return nil
	}

	narrowed := int(*total)

	return &narrowed
}

// operationFromRow turns one projection of the operations table into the
// Operation a caller receives.
//
// Result and Error are built here rather than stored as encoded structs, and
// each is built only in the state it means something in. A succeeded operation
// carrying an Error left over from a retried attempt, or a failed one carrying a
// half-written Result, would be a row that contradicts itself — and the
// contradiction would reach every client.
func operationFromRow(r *operationsdb.GetOperationRow) *Operation {
	op := &Operation{
		CreatedAt:     utcTime(r.CreatedAt),
		LastUpdatedAt: utcPtr(r.LastUpdatedAt),
		StartedAt:     utcPtr(r.StartedAt),
		FinishedAt:    utcPtr(r.FinishedAt),
		ID:            r.ID,
		Kind:          r.Kind,
		State:         State(r.State),
		Owner:         r.Scope,
		Request:       rawMessage(r.Request),
		Progress: Progress{
			UnitsTotal: unitsTotal(r.UnitsTotal),
			Unit:       r.ProgressUnit,
			Message:    r.ProgressMessage,
			CountLabel: r.CountLabel,
			Count:      r.ProgressCount,
			UnitsDone:  int(r.UnitsDone),
		},
		Revision:        r.Revision,
		Attempts:        int(r.Attempts),
		CancelRequested: r.CancelRequested,
	}

	op.Done = op.State.Terminal()

	if op.State == StateSucceeded && (r.ResultURI != "" || len(r.ResultDetail) > 0) {
		op.Result = &Result{URI: r.ResultURI, Detail: rawMessage(r.ResultDetail)}
	}

	if op.State == StateFailed && (r.ErrorCode != "" || r.ErrorMessage != "") {
		op.Error = &Error{Code: r.ErrorCode, Message: r.ErrorMessage, Retryable: r.ErrorRetryable}
	}

	return op
}

// operationsFromRows converts a batch of the shared projection, for the two
// reads that answer with many rows and no counts.
func operationsFromRows[Row any](rows []Row, same func(Row) operationsdb.GetOperationRow) []*Operation {
	ops := make([]*Operation, 0, len(rows))

	for i := range rows {
		row := same(rows[i])
		ops = append(ops, operationFromRow(&row))
	}

	return ops
}

// pageRow is one row of the listing: the operation, and the two counts the
// statement carries beside it.
//
// The counts ride on the rows rather than arriving from a second query, which is
// what makes a page and the number describing it come from one snapshot of the
// table. It also means a page with no rows carries no counts — see
// filtering.Drain, which reports that as unknown rather than as zero, and which
// is why a keyset walk's last page does not tell a client there are suddenly no
// results at all.
type pageRow struct {
	value    *Operation
	filtered int64
	total    int64
}

func pageValue(row pageRow) *Operation { return row.value }

func pageCounts(row pageRow) (filtered, total int64) { return row.filtered, row.total }

// operationPageRow converts a listing row, which is the shared projection plus
// the counts. It restates the fields rather than converting, because this row
// genuinely is a different shape.
func operationPageRow(r *operationsdb.ListOperationsRow) pageRow {
	shared := operationsdb.GetOperationRow{
		ID:              r.ID,
		Kind:            r.Kind,
		State:           r.State,
		Scope:           r.Scope,
		Request:         r.Request,
		UnitsTotal:      r.UnitsTotal,
		UnitsDone:       r.UnitsDone,
		ProgressUnit:    r.ProgressUnit,
		ProgressCount:   r.ProgressCount,
		CountLabel:      r.CountLabel,
		ProgressMessage: r.ProgressMessage,
		ResultURI:       r.ResultURI,
		ResultDetail:    r.ResultDetail,
		ErrorCode:       r.ErrorCode,
		ErrorMessage:    r.ErrorMessage,
		ErrorRetryable:  r.ErrorRetryable,
		Revision:        r.Revision,
		Attempts:        r.Attempts,
		CancelRequested: r.CancelRequested,
		CreatedAt:       r.CreatedAt,
		LastUpdatedAt:   r.LastUpdatedAt,
		StartedAt:       r.StartedAt,
		FinishedAt:      r.FinishedAt,
	}

	return pageRow{value: operationFromRow(&shared), filtered: r.FilteredCount, total: r.TotalCount}
}

// listParams is the whole argument list of the paged read: filtering's window
// and cursor, the scope, and this schema's two further narrowings.
//
// The scope is bound as the tenancy.Scope rather than as a string taken off it,
// which is what makes a read that lost its scope a driver error instead of a
// wider result set. It is also why it is a separate argument from listScope: a
// nil listScope narrows nothing further, and there is no value of it that can
// widen past the scope.
//
// The state set is bound rather than left off, and the empty set is never sent.
// A listing that names no states wants every state, and every state is a value
// here because operations.State is a closed set — see [allStates]. The
// alternative reading, where an empty set means "do not narrow", would be the
// one place in this module where an empty set matches everything instead of
// nothing.
func listParams(
	scope tenancy.Scope,
	listScope *ListScope,
	filter *filtering.QueryFilter,
) operationsdb.ListOperationsParams {
	params := operationsdb.ListOperationsParams{
		CreatedAfter:  utcPtr(filter.CreatedAfter),
		CreatedBefore: utcPtr(filter.CreatedBefore),
		UpdatedAfter:  utcPtr(filter.UpdatedAfter),
		UpdatedBefore: utcPtr(filter.UpdatedBefore),
		Scope:         scope,
		States:        allStates(),
		PageCursor:    filter.Cursor,
		ResultLimit:   int64(*filter.MaxResponseSize),
	}

	if listScope == nil {
		return params
	}

	if listScope.Kind != "" {
		kind := listScope.Kind
		params.Kind = &kind
	}

	if len(listScope.States) > 0 {
		params.States = stateStrings(listScope.States)
	}

	return params
}

// adoptScope settles which tenant an insert is for, and writes the answer back
// onto the operation.
//
// The scope the call named is the one the statement binds, so an operation that
// names a different one is refused rather than corrected: the two disagreeing is
// a caller holding one tenant's operation and recording it under another, which
// is a stale value or a mix-up and is not a thing to guess at. An operation that
// names none adopts the argument. tenancy.Scope tells the zero value apart from
// Global(), so "unset" here is genuinely unset rather than the global scope
// spelled shortly — which is the whole reason this can adopt at all.
func adoptScope(scope tenancy.Scope, op *Operation) error {
	if op.Owner != (tenancy.Scope{}) && op.Owner != scope {
		return platformerrors.Wrapf(ErrScopeMismatch,
			"operation names %q, the write names %q", op.Owner, scope)
	}

	op.Owner = scope

	return nil
}

// createParams is the insert's arguments. The state is bound rather than
// written into the statement as a literal, for the reason every status in this
// module is bound: a quoted literal in SQL text is one more place a spelling
// lives.
func createParams(op *Operation) operationsdb.CreateOperationParams {
	return operationsdb.CreateOperationParams{
		ID:         op.ID,
		Kind:       op.Kind,
		State:      string(StatePending),
		Scope:      op.Owner,
		Request:    op.Request,
		CountLabel: op.Progress.CountLabel,
	}
}

// stateStrings renders a state set as the bound array the statements take. It
// is also what a span attribute is joined from, since a span takes scalars and
// strings rather than a slice of a named type.
func stateStrings(states []State) []string {
	rendered := make([]string, 0, len(states))
	for _, state := range states {
		rendered = append(rendered, string(state))
	}

	return rendered
}

// allStates is every state an operation can be in, which is what a listing that
// narrows by none of them binds.
func allStates() []string {
	return stateStrings([]State{StatePending, StateRunning, StateSucceeded, StateFailed, StateCancelled})
}

// activeStates is the set the writes that must not move a terminal row guard
// on: pending or running.
//
// It is bound rather than written into the statements as a literal list for the
// same reason every other state is, and it is one function rather than a
// spelling per statement so the create, the claim, the finish and the
// cancellation cannot come to disagree about which rows are still in play.
func activeStates() []string {
	return stateStrings([]State{StatePending, StateRunning})
}

// terminalStates is what the retention sweep deletes: the states no worker will
// move an operation out of.
func terminalStates() []string {
	return stateStrings([]State{StateSucceeded, StateFailed, StateCancelled})
}

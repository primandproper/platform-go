package operations

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/operations/internal/operationsdb"
	"github.com/primandproper/platform-go/v14/operations/internal/queries"

	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// sharedRow is the projection five statements return, filled in enough to tell
// the conversions apart.
func sharedRow() operationsdb.GetOperationRow {
	return operationsdb.GetOperationRow{
		ID:              "op1",
		Kind:            "export",
		State:           string(StateRunning),
		Owner:           "u1",
		Request:         []byte(`{"a":1}`),
		UnitsTotal:      pointer.To(int64(9)),
		UnitsDone:       3,
		ProgressUnit:    "identity",
		ProgressCount:   4300,
		CountLabel:      "records",
		ProgressMessage: "collecting",
		Revision:        4,
		Attempts:        2,
		CreatedAt:       time.Date(2026, time.August, 27, 12, 0, 0, 0, time.FixedZone("somewhere", 3600)),
	}
}

func TestOperationFromRow(T *testing.T) {
	T.Parallel()

	T.Run("carries the row across", func(t *testing.T) {
		t.Parallel()

		row := sharedRow()
		op := operationFromRow(&row)

		test.EqOp(t, "op1", op.ID)
		test.EqOp(t, "export", op.Kind)
		test.EqOp(t, StateRunning, op.State)
		test.EqOp(t, "u1", op.Owner)
		test.Eq(t, json.RawMessage(`{"a":1}`), op.Request)
		must.NotNil(t, op.Progress.UnitsTotal)
		test.EqOp(t, 9, *op.Progress.UnitsTotal)
		test.EqOp(t, 3, op.Progress.UnitsDone)
		test.EqOp(t, int64(4300), op.Progress.Count)
		test.EqOp(t, "records", op.Progress.CountLabel)
		test.EqOp(t, int64(4), op.Revision)
		test.EqOp(t, 2, op.Attempts)
	})

	// Postgres hands a timestamp back in the session's zone, so a caller
	// comparing two of them, or rendering one into JSON, would otherwise get an
	// answer that depends on where the row was read.
	T.Run("normalizes every timestamp to UTC", func(t *testing.T) {
		t.Parallel()

		at := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.FixedZone("somewhere", 3600))

		row := sharedRow()
		row.LastUpdatedAt, row.StartedAt, row.FinishedAt = &at, &at, &at

		op := operationFromRow(&row)

		test.EqOp(t, time.UTC, op.CreatedAt.Location())

		for name, stamp := range map[string]*time.Time{
			"lastUpdatedAt": op.LastUpdatedAt, "startedAt": op.StartedAt, "finishedAt": op.FinishedAt,
		} {
			must.NotNil(t, stamp, must.Sprintf("stamp %q", name))
			test.EqOp(t, time.UTC, stamp.Location(), test.Sprintf("stamp %q", name))
			test.True(t, stamp.Equal(at), test.Sprintf("stamp %q", name))
		}
	})

	T.Run("an absent stamp stays absent", func(t *testing.T) {
		t.Parallel()

		row := sharedRow()
		op := operationFromRow(&row)

		test.Nil(t, op.LastUpdatedAt)
		test.Nil(t, op.StartedAt)
		test.Nil(t, op.FinishedAt)
	})

	// "There is no denominator" is a fact about the work that a zero could not
	// distinguish from "there are no units yet".
	T.Run("an absent denominator stays absent", func(t *testing.T) {
		t.Parallel()

		row := sharedRow()
		row.UnitsTotal = nil

		test.Nil(t, operationFromRow(&row).Progress.UnitsTotal)
	})

	T.Run("an absent request stays absent", func(t *testing.T) {
		t.Parallel()

		row := sharedRow()
		row.Request = nil

		test.Nil(t, operationFromRow(&row).Request)
	})

	// A succeeded operation carrying an Error left over from a retried attempt,
	// or a failed one carrying a half-written Result, would be a row that
	// contradicts itself — and the contradiction would reach every client.
	T.Run("the outcome is read in the state it means something in", func(t *testing.T) {
		t.Parallel()

		filled := func(state State) *Operation {
			row := sharedRow()
			row.State = string(state)
			row.ResultURI = "s3://bundle"
			row.ErrorCode = "boom"
			row.ErrorMessage = "went wrong"
			row.ErrorRetryable = true

			return operationFromRow(&row)
		}

		succeeded := filled(StateSucceeded)
		must.NotNil(t, succeeded.Result)
		test.EqOp(t, "s3://bundle", succeeded.Result.URI)
		test.Nil(t, succeeded.Error)

		failed := filled(StateFailed)
		must.NotNil(t, failed.Error)
		test.EqOp(t, "boom", failed.Error.Code)
		test.True(t, failed.Error.Retryable)
		test.Nil(t, failed.Result)

		running := filled(StateRunning)
		test.Nil(t, running.Result)
		test.Nil(t, running.Error)
	})

	// Done is the one signal a client is obliged to understand, and it is
	// derived rather than stored so there is no second source of truth for it.
	T.Run("done follows the state", func(t *testing.T) {
		t.Parallel()

		for _, state := range []State{StatePending, StateRunning, StateSucceeded, StateFailed, StateCancelled} {
			row := sharedRow()
			row.State = string(state)

			test.EqOp(t, state.Terminal(), operationFromRow(&row).Done, test.Sprintf("state %q", state))
		}
	})
}

// TestGeneratedRowsAreOneProjection is what the struct conversions in this
// package rest on. Five statements project queries.Columns, so their row types
// are five names for one shape; the conversions between them are Go's own and
// would stop compiling if that changed. What the compiler cannot see is the
// column list itself growing without the projections following, which is what
// this counts.
func TestGeneratedRowsAreOneProjection(T *testing.T) {
	T.Parallel()

	for name, rowType := range map[string]reflect.Type{
		"GetOperation":           reflect.TypeFor[operationsdb.GetOperationRow](),
		"GetOperations":          reflect.TypeFor[operationsdb.GetOperationsRow](),
		"CreateOperation":        reflect.TypeFor[operationsdb.CreateOperationRow](),
		"BeginOperation":         reflect.TypeFor[operationsdb.BeginOperationRow](),
		"ListStrandedOperations": reflect.TypeFor[operationsdb.ListStrandedOperationsRow](),
	} {
		test.EqOp(T, len(queries.Columns), rowType.NumField(), test.Sprintf("statement %q", name))
	}

	// The listing is the one that genuinely differs: the same projection plus
	// the two counts that ride on every row of it.
	test.EqOp(T, len(queries.Columns)+2, reflect.TypeFor[operationsdb.ListOperationsRow]().NumField())
}

func TestStateSets(T *testing.T) {
	T.Parallel()

	// The three sets partition the states, so a row is active or terminal and
	// never neither — which is what makes "reap the terminal ones" and "guard
	// the active ones" cover the table between them.
	T.Run("active and terminal partition every state", func(t *testing.T) {
		t.Parallel()

		every := allStates()

		test.SliceLen(t, 5, every)
		test.SliceLen(t, len(every), append(activeStates(), terminalStates()...))

		for _, state := range append(activeStates(), terminalStates()...) {
			test.True(t, slices.Contains(every, state), test.Sprintf("state %q", state))
		}
	})

	T.Run("terminal is what State.Terminal says it is", func(t *testing.T) {
		t.Parallel()

		terminal := terminalStates()

		for _, state := range allStates() {
			test.EqOp(t, State(state).Terminal(), slices.Contains(terminal, state),
				test.Sprintf("state %q", state))
		}
	})
}

func TestListParams(T *testing.T) {
	T.Parallel()

	// A listing that narrows by no state wants every state, and the statement's
	// set is never empty: an empty set matches nothing everywhere else in this
	// module, and this is not the place to make it mean the opposite.
	T.Run("an unscoped listing binds every state and narrows nothing", func(t *testing.T) {
		t.Parallel()

		params := listParams(nil, normalized(t, nil))

		test.Nil(t, params.Owner)
		test.Nil(t, params.Kind)
		test.Eq(t, allStates(), params.States)
	})

	T.Run("an empty scope is the same as no scope", func(t *testing.T) {
		t.Parallel()

		params := listParams(&ListScope{}, normalized(t, nil))

		test.Nil(t, params.Owner)
		test.Nil(t, params.Kind)
		test.Eq(t, allStates(), params.States)
	})

	T.Run("a scope narrows what it names", func(t *testing.T) {
		t.Parallel()

		params := listParams(&ListScope{
			Owner:  "u1",
			Kind:   "export",
			States: []State{StateFailed},
		}, normalized(t, nil))

		must.NotNil(t, params.Owner)
		test.EqOp(t, "u1", *params.Owner)
		must.NotNil(t, params.Kind)
		test.EqOp(t, "export", *params.Kind)
		test.Eq(t, []string{string(StateFailed)}, params.States)
	})

	// The filter window used to reach the listing and be dropped on the floor,
	// because the statement it bound into was assembled by hand and never
	// carried one. The generated list carries all four bounds.
	T.Run("the filter window reaches the statement", func(t *testing.T) {
		t.Parallel()

		at := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.FixedZone("somewhere", 3600))

		filter := filtering.DefaultQueryFilter()
		filter.CreatedAfter = &at
		filter.CreatedBefore = &at
		filter.UpdatedAfter = &at
		filter.UpdatedBefore = &at
		filter.Cursor = pointer.To("op1")

		params := listParams(nil, normalized(t, filter))

		for name, bound := range map[string]*time.Time{
			"createdAfter":  params.CreatedAfter,
			"createdBefore": params.CreatedBefore,
			"updatedAfter":  params.UpdatedAfter,
			"updatedBefore": params.UpdatedBefore,
		} {
			must.NotNil(t, bound, must.Sprintf("bound %q", name))
			test.EqOp(t, time.UTC, bound.Location(), test.Sprintf("bound %q", name))
		}

		must.NotNil(t, params.PageCursor)
		test.EqOp(t, "op1", *params.PageCursor)
	})
}

func TestPageFilter(T *testing.T) {
	T.Parallel()

	T.Run("a nil filter is the default one", func(t *testing.T) {
		t.Parallel()

		filter := normalized(t, nil)

		must.NotNil(t, filter.MaxResponseSize)
		test.EqOp(t, uint16(filtering.DefaultQueryFilterLimit), *filter.MaxResponseSize)
	})

	// A zero reaching the statement is a page of no rows, which reads to a
	// client as an empty collection rather than as a caller who sent nothing.
	T.Run("a zero page size is the default one", func(t *testing.T) {
		t.Parallel()

		asked := filtering.DefaultQueryFilter()
		asked.MaxResponseSize = pointer.To(uint16(0))

		filter := normalized(t, asked)

		must.NotNil(t, filter.MaxResponseSize)
		test.EqOp(t, uint16(filtering.DefaultQueryFilterLimit), *filter.MaxResponseSize)
	})

	// An unbounded limit reaching the server is a caller asking for the whole
	// table, and the shared ceiling is what every other paged read in this
	// module is held to.
	T.Run("an oversized page is clamped", func(t *testing.T) {
		t.Parallel()

		asked := filtering.DefaultQueryFilter()
		asked.MaxResponseSize = pointer.To(uint16(65535))

		filter := normalized(t, asked)

		must.NotNil(t, filter.MaxResponseSize)
		test.Less(t, uint16(65535), *filter.MaxResponseSize)
	})

	// The caller's filter is not the store's to edit: a handler reusing one
	// across two reads would otherwise find its page size rewritten underneath.
	T.Run("the caller's filter is left alone", func(t *testing.T) {
		t.Parallel()

		asked := filtering.DefaultQueryFilter()
		asked.MaxResponseSize = nil

		_ = normalized(t, asked)

		test.Nil(t, asked.MaxResponseSize)
	})
}

func TestCreateParams(T *testing.T) {
	T.Parallel()

	params := createParams(&Operation{
		ID:       "op1",
		Kind:     "export",
		Owner:    "u1",
		Request:  json.RawMessage(`{"a":1}`),
		Progress: Progress{CountLabel: "records"},
	})

	test.EqOp(T, "op1", params.ID)
	test.EqOp(T, "export", params.Kind)
	test.EqOp(T, "u1", params.Owner)
	test.EqOp(T, "records", params.CountLabel)

	// The state is the statement's argument rather than a literal in its text,
	// so there is one spelling of "pending" in the package.
	test.EqOp(T, string(StatePending), params.State)
}

// normalized is pageFilter for the assertions that are not about the one error
// it reports, which is a sort direction filtering does not recognize.
func normalized(t *testing.T, filter *filtering.QueryFilter) *filtering.QueryFilter {
	t.Helper()

	bounded, err := pageFilter(filter)
	must.NoError(t, err)

	return bounded
}

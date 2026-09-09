package saga

import (
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v14/saga/internal/sagadb"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
)

// The typed seam between the generated package and this one.
//
// saga/internal/sagadb is sqlc-gen-unison's output: one params and one row
// struct per statement, the same on all three dialects. These functions are the
// whole of what this package does with them — a row becomes a Record, a Record
// becomes the params — and every one is a struct literal on purpose. A renamed
// or retyped column changes the generated struct, and every conversion here
// stops compiling; the projection-and-scan-targets pairing these replaced
// reported the same mistake as a runtime scan error, or worse, as two
// same-typed columns silently transposed.
//
// The row structs are nominal per statement, so a listing's row cannot convert
// to the get's even where the columns agree — which is why the page converters
// restate the fields rather than casting. Restating is the cost; the compiler
// checking every field name is what it buys.

// pageRow is one row of a paged read: the value, and the two counts the
// statement carried alongside it.
type pageRow struct {
	value    *Record
	filtered int64
	total    int64
}

// pageCounts reads the counts off a row, for filtering.Drain.
func pageCounts(row pageRow) (filtered, total int64) {
	return row.filtered, row.total
}

// pageValue reads the value off a row, for filtering.Drain.
func pageValue(row pageRow) *Record { return row.value }

// recordFromRow turns the get's row into a Record.
//
// It is the one conversion, and every other row shape restates itself into the
// get's before calling it — so what a row means is written down once, and a
// column that changes type breaks the restatement rather than the meaning.
//
// The step list is decoded here and the decode can fail, unlike the encode: a
// column can be edited by hand, and a saga whose step names do not parse is one
// no worker should advance.
func recordFromRow(r *sagadb.GetSagaInstanceRow) (*Record, error) {
	inst := &Record{
		CreatedAt:     r.CreatedAt.UTC(),
		LastUpdatedAt: utcPtr(r.LastUpdatedAt),
		ID:            r.ID,
		Definition:    r.Definition,
		LastError:     r.LastError,
		Status:        Status(r.Status),
		ResumeStatus:  Status(r.ResumeStatus),
		CurrentStep:   int(r.CurrentStep),
		Attempts:      int(r.Attempts),
	}

	if len(r.State) > 0 {
		inst.State = json.RawMessage(r.State)
	}

	if err := json.Unmarshal([]byte(r.StepNames), &inst.StepNames); err != nil {
		return nil, platformerrors.Wrap(err, "decoding saga step names")
	}

	return inst, nil
}

// recordFromBatchRow turns a row of the claimed batch into a Record.
func recordFromBatchRow(r *sagadb.ListSagaInstancesByIDsRow) (*Record, error) {
	return recordFromRow(&sagadb.GetSagaInstanceRow{
		ID:            r.ID,
		Definition:    r.Definition,
		Status:        r.Status,
		CurrentStep:   r.CurrentStep,
		StepNames:     r.StepNames,
		State:         r.State,
		Attempts:      r.Attempts,
		LastError:     r.LastError,
		ResumeStatus:  r.ResumeStatus,
		CreatedAt:     r.CreatedAt,
		LastUpdatedAt: r.LastUpdatedAt,
		ArchivedAt:    r.ArchivedAt,
		NextAttempt:   r.NextAttempt,
		ClaimedUntil:  r.ClaimedUntil,
	})
}

// instancePageRow turns a row of the unnarrowed listing into a page row.
func instancePageRow(r *sagadb.ListSagaInstancesRow) (pageRow, error) {
	inst, err := recordFromRow(&sagadb.GetSagaInstanceRow{
		ID:            r.ID,
		Definition:    r.Definition,
		Status:        r.Status,
		CurrentStep:   r.CurrentStep,
		StepNames:     r.StepNames,
		State:         r.State,
		Attempts:      r.Attempts,
		LastError:     r.LastError,
		ResumeStatus:  r.ResumeStatus,
		CreatedAt:     r.CreatedAt,
		LastUpdatedAt: r.LastUpdatedAt,
		ArchivedAt:    r.ArchivedAt,
		NextAttempt:   r.NextAttempt,
		ClaimedUntil:  r.ClaimedUntil,
	})
	if err != nil {
		return pageRow{}, err
	}

	return pageRow{value: inst, filtered: r.FilteredCount, total: r.TotalCount}, nil
}

// instancePageRowByDefinition turns a row of the definition-narrowed listing
// into a page row.
func instancePageRowByDefinition(r *sagadb.ListSagaInstancesByDefinitionRow) (pageRow, error) {
	inst, err := recordFromRow(&sagadb.GetSagaInstanceRow{
		ID:            r.ID,
		Definition:    r.Definition,
		Status:        r.Status,
		CurrentStep:   r.CurrentStep,
		StepNames:     r.StepNames,
		State:         r.State,
		Attempts:      r.Attempts,
		LastError:     r.LastError,
		ResumeStatus:  r.ResumeStatus,
		CreatedAt:     r.CreatedAt,
		LastUpdatedAt: r.LastUpdatedAt,
		ArchivedAt:    r.ArchivedAt,
		NextAttempt:   r.NextAttempt,
		ClaimedUntil:  r.ClaimedUntil,
	})
	if err != nil {
		return pageRow{}, err
	}

	return pageRow{value: inst, filtered: r.FilteredCount, total: r.TotalCount}, nil
}

// insertParams renders a new instance as the create binds it.
//
// The step list is encoded here and the encode returns no error, because it
// cannot produce one: json.Marshal fails on cycles, channels, funcs and NaNs,
// and a []string is none of those. An error branch here would be one nothing
// can reach and no test can cover.
func insertParams(inst *Record, nextAttempt time.Time) sagadb.InsertSagaInstanceParams {
	//nolint:errcheck,errchkjson // a []string always marshals; see above.
	stepNames, _ := json.Marshal(inst.StepNames)

	return sagadb.InsertSagaInstanceParams{
		ID:           inst.ID,
		Definition:   inst.Definition,
		Status:       string(inst.Status),
		CurrentStep:  int64(inst.CurrentStep),
		StepNames:    string(stepNames),
		State:        stateOrNil(inst.State),
		Attempts:     int64(inst.Attempts),
		LastError:    inst.LastError,
		ResumeStatus: string(inst.ResumeStatus),
		CreatedAt:    inst.CreatedAt.UTC(),
		NextAttempt:  nextAttempt.UTC(),
	}
}

// advanceParams renders a cursor that moved as the advance binds it.
//
// The pair of advance statements differ in one assignment — whether the lease
// is dropped — so this builds the params for one and the caller converts, which
// is the same reasoning sortedRows converts a descending page: two renderings of
// one statement, where the conversion is the assertion that they have not
// drifted apart.
func advanceParams(inst *Record, nextAttempt, at time.Time) sagadb.AdvanceSagaInstanceParams {
	stamp := at.UTC()

	return sagadb.AdvanceSagaInstanceParams{
		Status:             string(inst.Status),
		CurrentStep:        int64(inst.CurrentStep),
		State:              stateOrNil(inst.State),
		LastError:          inst.LastError,
		ResumeStatus:       string(inst.ResumeStatus),
		NextAttempt:        nextAttempt.UTC(),
		LastUpdatedAt:      &stamp,
		ID:                 inst.ID,
		RunningStatus:      string(StatusRunning),
		CompensatingStatus: string(StatusCompensating),
	}
}

// stateOrNil maps an empty encoding to a SQL NULL rather than an empty blob.
//
// It is database.BlobOrNil's rule at a generated statement's typed parameter:
// that helper answers `any`, for a driver argument, and a []byte field wants the
// distinction made before it is assigned. Two renderings of "no state" would
// make the round trip depend on which call site wrote the row.
func stateOrNil(state json.RawMessage) []byte {
	if len(state) == 0 {
		return nil
	}

	return state
}

// utcPtr normalizes an optional timestamp to UTC, preserving absence.
//
// Every timestamp this package writes is UTC, so every one it returns is too —
// Postgres hands back a time in the session's zone, MySQL in the server's, and
// SQLite whatever the stored text parsed as, so a caller comparing two of those
// would get an answer that depends on where the row was read.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// sortedRows runs whichever of a paged read's two statements the filter's sort
// direction names, and hands back the ascending statement's rows either way.
//
// A paged list is two statements here, because a direction is which way the
// ORDER BY runs and which way the cursor comparison points — statement text,
// not a bound value, on all three engines. saga/internal/queries renders the
// pair and filtering.QueryFilter.SortsDescending picks between them.
//
// The descending rows are converted rather than restated field by field, and
// that is deliberate: they are one projection rendered twice, with the walk
// reversed and nothing else changed, so the conversion is the assertion. The
// day the two projections stop being identical in field name, type or order,
// this stops building rather than filling the wrong fields.
func sortedRows[Ascending, Descending any](
	filter *filtering.QueryFilter,
	ascending func() ([]Ascending, error),
	descending func() ([]Descending, error),
	same func(Descending) Ascending,
) ([]Ascending, error) {
	if !filter.SortsDescending() {
		return ascending()
	}

	rows, err := descending()
	if err != nil {
		return nil, err
	}

	page := make([]Ascending, 0, len(rows))
	for i := range rows {
		page = append(page, same(rows[i]))
	}

	return page, nil
}

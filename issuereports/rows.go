package issuereports

import (
	"time"

	"github.com/primandproper/platform-go/v14/issuereports/internal/issuereportsdb"

	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The typed seam between the generated package and the domain types.
//
// issuereports/internal/issuereportsdb is sqlc-gen-unison's output: one params
// and one row struct per statement, the same on all three dialects. These
// functions are the whole of what this package does with them — a row becomes
// the domain type, a domain value becomes the params — and every one is a struct
// literal on purpose. A renamed or retyped column changes the generated struct,
// and every conversion here stops compiling; a scan-by-position pairing would
// report the same mistake as a runtime scan error, or worse, as two same-typed
// columns silently transposed.
//
// The row structs are nominal per statement, so a list row cannot convert to a
// get row even where the columns agree — which is why the page converters
// restate the fields rather than casting.

// utcPtr normalizes an optional timestamp to UTC, preserving absence. It is the
// one home for the rule, and every conversion below goes through it.
//
// Every timestamp this package writes is UTC, so every one it returns is too —
// Postgres hands back a time in the session's zone, MySQL in the server's, and
// SQLite whatever the string parsed as, so a caller comparing two of those, or
// rendering one into JSON, would get an answer that depends on where the row was
// read.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// pageRow is one row of a rendered list query: the value, and the two counts the
// statement carries beside it.
//
// The counts ride on the rows rather than arriving from a second query, which is
// what makes a page and the number describing it come from one snapshot of the
// table. It also means a page with no rows carries no counts — see
// filtering.Drain, which reports that as unknown rather than as zero.
type pageRow struct {
	value    *Report
	filtered int64
	total    int64
}

// pageCounts reads the counts off a row, for filtering.Drain.
func pageCounts(row pageRow) (filtered, total int64) { return row.filtered, row.total }

// pageValue reads the value off a row, for filtering.Drain.
func pageValue(row pageRow) *Report { return row.value }

// sortedRows runs whichever of a paged read's two statements the filter's sort
// direction names, and hands back the ascending statement's rows either way.
//
// A paged list is two statements here, because a direction is which way the
// ORDER BY runs and which way the cursor comparison points — statement text, not
// a bound value, on all three engines. database/querygen emits the pair and
// filtering.QueryFilter.SortsDescending picks between them; this is where the
// pick is made, once, rather than at each of the four paged reads. A read that
// reached for the ascending statement while holding a descending filter would
// answer in the order the client did not ask for, and nothing about the rows
// that came back would say so.
//
// The descending rows are converted rather than restated field by field, which
// is the one place in this file that casts. The preamble's rule is about two
// projections that happen to agree; these are one projection rendered twice,
// with the walk reversed and nothing else changed. So the conversion is the
// assertion, and Go makes it the compiler's: the day the two projections stop
// being identical, in field name, type or order, this stops building rather than
// filling the wrong fields.
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

// listWindow is the filter window every generated list statement binds. One
// reading of the filter, restated into each nominal params type by the
// constructors below.
//
// The UTC normalization on the four times is load-bearing on SQLite, not
// cosmetic: that column compares as text, the stored shape is UTC
// `YYYY-MM-DD HH:MM:SS`, and the driver renders a bound time.Time with its own
// zone's clock in exactly that prefix position — so a UTC value compares
// correctly to the second and a zoned one is off by its offset, silently.
type listWindow struct {
	createdAfter    *time.Time
	createdBefore   *time.Time
	updatedAfter    *time.Time
	updatedBefore   *time.Time
	pageCursor      *string
	resultLimit     int64
	includeArchived bool
}

// windowFrom reads the window off a page filter. The filter has been through
// pageFilter, so MaxResponseSize is set; only IncludeArchived defaults here, and
// it defaults to excluding, which is what the statement's COALESCE would have
// done with a NULL anyway — bound explicitly so the parameter is a bool rather
// than a pointer whose nil means the same thing.
func windowFrom(filter *filtering.QueryFilter) listWindow {
	w := listWindow{
		createdAfter:  utcPtr(filter.CreatedAfter),
		createdBefore: utcPtr(filter.CreatedBefore),
		updatedAfter:  utcPtr(filter.UpdatedAfter),
		updatedBefore: utcPtr(filter.UpdatedBefore),
		pageCursor:    filter.Cursor,
		resultLimit:   int64(*filter.MaxResponseSize),
	}

	if filter.IncludeArchived != nil {
		w.includeArchived = *filter.IncludeArchived
	}

	return w
}

func reportFromRow(r *issuereportsdb.GetReportRow) *Report {
	return &Report{
		CreatedAt:     r.CreatedAt.UTC(),
		LastUpdatedAt: utcPtr(r.LastUpdatedAt),
		ArchivedAt:    utcPtr(r.ArchivedAt),
		ClosedAt:      utcPtr(r.ClosedAt),
		ID:            r.ID,
		Reporter:      r.Reporter,
		Kind:          r.Kind,
		Details:       r.Details,
		SubjectType:   r.SubjectType,
		SubjectID:     r.SubjectID,
		Status:        Status(r.Status),
		Resolution:    r.Resolution,
		Scope:         r.Scope,
	}
}

// reportFromArchivedRow converts the archive's read-back, which is the one row
// shape here that casts rather than restating itself.
//
// It is sortedRows' reason rather than an exception to the preamble's rule.
// GetArchivedReport projects the same list GetReport does — the table's columns,
// in that order — and differs from it only in which rows it will look at, so the
// two row types are one projection rendered twice. The conversion is therefore
// the assertion: the day the two projections stop agreeing, in field name, type
// or order, this stops building rather than filling the wrong fields.
func reportFromArchivedRow(r *issuereportsdb.GetArchivedReportRow) *Report {
	row := issuereportsdb.GetReportRow(*r)

	return reportFromRow(&row)
}

// reportPageRow is the one conversion from a list row, and every list converts
// through it.
//
// The four lists are four nominally distinct row types over one projection — the
// same SELECT with more predicates — so the other three convert to this one's
// type first, in listPage below. That makes the identity of the projections the
// compiler's assertion rather than four restatements that could come to disagree
// about which column is which.
func reportPageRow(r *issuereportsdb.ListReportsRow) pageRow {
	return pageRow{
		value: &Report{
			CreatedAt:     r.CreatedAt.UTC(),
			LastUpdatedAt: utcPtr(r.LastUpdatedAt),
			ArchivedAt:    utcPtr(r.ArchivedAt),
			ClosedAt:      utcPtr(r.ClosedAt),
			ID:            r.ID,
			Reporter:      r.Reporter,
			Kind:          r.Kind,
			Details:       r.Details,
			SubjectType:   r.SubjectType,
			SubjectID:     r.SubjectID,
			Status:        Status(r.Status),
			Resolution:    r.Resolution,
			Scope:         r.Scope,
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func createReportParams(scope tenancy.Scope, r *Report) issuereportsdb.CreateReportParams {
	return issuereportsdb.CreateReportParams{
		ID:          r.ID,
		Scope:       scope,
		Reporter:    r.Reporter,
		Kind:        r.Kind,
		Details:     r.Details,
		SubjectType: r.SubjectType,
		SubjectID:   r.SubjectID,
		Status:      r.Status.String(),
		Resolution:  r.Resolution,
		ClosedAt:    utcPtr(r.ClosedAt),
	}
}

func updateReportParams(scope tenancy.Scope, r *Report) issuereportsdb.UpdateReportParams {
	return issuereportsdb.UpdateReportParams{
		Kind:        r.Kind,
		Details:     r.Details,
		SubjectType: r.SubjectType,
		SubjectID:   r.SubjectID,
		ID:          r.ID,
		Scope:       scope,
	}
}

// listReportsParams is the scope's whole list: the window, the scope, and the
// page. The three narrowed lists restate it with one argument added, because
// their params types are nominally distinct and carry a field this one does not
// — so a conversion is not available and each says which predicate it binds.
func listReportsParams(
	scope tenancy.Scope,
	filter *filtering.QueryFilter,
) issuereportsdb.ListReportsParams {
	w := windowFrom(filter)

	return issuereportsdb.ListReportsParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func listByStatusParams(
	scope tenancy.Scope,
	status Status,
	filter *filtering.QueryFilter,
) issuereportsdb.ListReportsByStatusParams {
	w := windowFrom(filter)

	return issuereportsdb.ListReportsByStatusParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		Status:          status.String(),
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func listByReporterParams(
	scope tenancy.Scope,
	reporter string,
	filter *filtering.QueryFilter,
) issuereportsdb.ListReportsByReporterParams {
	w := windowFrom(filter)

	return issuereportsdb.ListReportsByReporterParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		Reporter:        reporter,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func listBySubjectTypeParams(
	scope tenancy.Scope,
	subjectType string,
	filter *filtering.QueryFilter,
) issuereportsdb.ListReportsBySubjectTypeParams {
	w := windowFrom(filter)

	return issuereportsdb.ListReportsBySubjectTypeParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		SubjectType:     subjectType,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func listForSubjectParams(
	scope tenancy.Scope,
	subjectType, subjectID string,
	filter *filtering.QueryFilter,
) issuereportsdb.ListReportsForSubjectParams {
	w := windowFrom(filter)

	return issuereportsdb.ListReportsForSubjectParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		SubjectType:     subjectType,
		SubjectID:       subjectID,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

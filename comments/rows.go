package comments

import (
	"time"

	"github.com/primandproper/platform-go/v14/comments/internal/commentsdb"

	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The typed seam between the generated package and the domain types.
//
// comments/internal/commentsdb is sqlc-gen-unison's output: one params and one
// row struct per statement, the same on all three dialects. These functions are
// the whole of what this package does with them — a row becomes the domain type,
// a domain value becomes the params — and every one is a struct literal on
// purpose. A renamed or retyped column changes the generated struct, and every
// conversion here stops compiling; a scan-by-position pairing would report the
// same mistake as a runtime scan error, or worse, as two same-typed columns
// silently transposed.
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
	value    *Comment
	filtered int64
	total    int64
}

// pageCounts reads the counts off a row, for filtering.Drain.
func pageCounts(row pageRow) (filtered, total int64) { return row.filtered, row.total }

// pageValue reads the value off a row, for filtering.Drain.
func pageValue(row pageRow) *Comment { return row.value }

// sortedRows runs whichever of a paged read's two statements the filter's sort
// direction names, and hands back the ascending statement's rows either way.
//
// A paged list is two statements here, because a direction is which way the
// ORDER BY runs and which way the cursor comparison points — statement text, not
// a bound value, on all three engines. database/querygen emits the pair and
// filtering.QueryFilter.SortsDescending picks between them; this is where the
// pick is made, once, rather than at each of the three paged reads. A read that
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

// convert casts a narrowed list's rows to the base list's row type.
//
// The three list statements are one projection rendered three times, with
// different predicates and nothing else changed, so the conversion is the
// assertion: the day two of those projections stop being identical, in field
// name, type or order, this stops building rather than filling the wrong fields.
func convert[From, To any](rows []From, same func(From) To) []To {
	converted := make([]To, 0, len(rows))
	for i := range rows {
		converted = append(converted, same(rows[i]))
	}

	return converted
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

func commentFromRow(r *commentsdb.GetCommentRow) *Comment {
	return &Comment{
		CreatedAt:     r.CreatedAt.UTC(),
		LastUpdatedAt: utcPtr(r.LastUpdatedAt),
		ArchivedAt:    utcPtr(r.ArchivedAt),
		ID:            r.ID,
		ParentID:      r.ParentID,
		Author:        r.Author,
		Body:          r.Body,
		Target:        Target{Type: TargetType(r.TargetType), ID: r.TargetID},
		Scope:         r.Scope,
	}
}

// commentFromArchivedRow converts the archive's read-back, which is the one row
// shape here that casts rather than restating itself.
//
// It is sortedRows' reason rather than an exception to the preamble's rule.
// GetArchivedComment projects the same list GetComment does — the table's
// columns, in that order — and differs from it only in which rows it will look
// at, so the two row types are one projection rendered twice. The conversion is
// therefore the assertion: the day the two projections stop agreeing, in field
// name, type or order, this stops building rather than filling the wrong fields.
func commentFromArchivedRow(r *commentsdb.GetArchivedCommentRow) *Comment {
	row := commentsdb.GetCommentRow(*r)

	return commentFromRow(&row)
}

// commentPageRow is the one conversion from a list row, and every list converts
// through it.
//
// The three lists are three nominally distinct row types over one projection —
// the same SELECT with different predicates — so the other two convert to this
// one's type first, in listPage below. That makes the identity of the
// projections the compiler's assertion rather than three restatements that could
// come to disagree about which column is which.
func commentPageRow(r *commentsdb.ListCommentsRow) pageRow {
	return pageRow{
		value: &Comment{
			CreatedAt:     r.CreatedAt.UTC(),
			LastUpdatedAt: utcPtr(r.LastUpdatedAt),
			ArchivedAt:    utcPtr(r.ArchivedAt),
			ID:            r.ID,
			ParentID:      r.ParentID,
			Author:        r.Author,
			Body:          r.Body,
			Target:        Target{Type: TargetType(r.TargetType), ID: r.TargetID},
			Scope:         r.Scope,
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func createCommentParams(scope tenancy.Scope, c *Comment) commentsdb.CreateCommentParams {
	return commentsdb.CreateCommentParams{
		ID:         c.ID,
		Scope:      scope,
		TargetType: c.Target.Type.String(),
		TargetID:   c.Target.ID,
		ParentID:   c.ParentID,
		Author:     c.Author,
		Body:       c.Body,
	}
}

func updateCommentParams(scope tenancy.Scope, c *Comment) commentsdb.UpdateCommentParams {
	return commentsdb.UpdateCommentParams{
		Body:  c.Body,
		ID:    c.ID,
		Scope: scope,
	}
}

// listCommentsParams is one level of one target's discussion: the window, the
// scope, the target, and the parent — the empty string for the roots. The two
// narrowed lists restate it with fewer arguments, because their params types are
// nominally distinct, so a conversion is not available and each says which
// predicate it binds.
func listCommentsParams(
	scope tenancy.Scope,
	target Target,
	parentID string,
	filter *filtering.QueryFilter,
) commentsdb.ListCommentsParams {
	w := windowFrom(filter)

	return commentsdb.ListCommentsParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		TargetType:      target.Type.String(),
		TargetID:        target.ID,
		ParentID:        parentID,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func listByTargetTypeParams(
	scope tenancy.Scope,
	targetType TargetType,
	filter *filtering.QueryFilter,
) commentsdb.ListCommentsByTargetTypeParams {
	w := windowFrom(filter)

	return commentsdb.ListCommentsByTargetTypeParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		TargetType:      targetType.String(),
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func listByAuthorParams(
	scope tenancy.Scope,
	author string,
	filter *filtering.QueryFilter,
) commentsdb.ListCommentsByAuthorParams {
	w := windowFrom(filter)

	return commentsdb.ListCommentsByAuthorParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		Author:          author,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

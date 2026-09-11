package waitlists

import (
	"time"

	"github.com/primandproper/platform-go/v14/waitlists/internal/waitlistsdb"

	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The typed seam between the generated package and the domain types.
//
// waitlists/internal/waitlistsdb is sqlc-gen-unison's output: one params and one
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
// restate the fields rather than casting. Restating is the cost; the compiler
// checking every field name is what it buys.

// utcPtr normalizes an optional timestamp to UTC, preserving absence. It is the
// one home for the rule, and every conversion below goes through it.
//
// Every timestamp this package returns is normalized here — Postgres hands back
// a time in the session's zone, MySQL in the server's, and SQLite whatever the
// string parsed as, so a caller comparing two of those, or rendering one into
// JSON, would otherwise get an answer that depends on where the row was read.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// listWindow is the filter window every generated list statement binds, in the
// shape the generated params carry it. One reading of the filter, restated into
// each nominal params type by the constructors below.
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
//
// The UTC normalization on the four times is load-bearing on SQLite, not
// cosmetic. That column compares as text, the stored shape is UTC
// `YYYY-MM-DD HH:MM:SS`, and the driver renders a bound time.Time with its own
// zone's clock in exactly that prefix position — so a UTC value compares
// correctly to the second and a zoned one is off by its offset, silently.
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

// pageRow is one row of a rendered list query: the value, and the two counts the
// statement carries beside it.
//
// The counts ride on the rows rather than arriving from a second query, which is
// what makes a page and the number describing it come from one snapshot of the
// table. It also means a page with no rows carries no counts — see
// filtering.Drain, which is what reports that as unknown rather than as zero.
type pageRow[T any] struct {
	value    *T
	filtered int64
	total    int64
}

// pageCounts reads the counts off a row, for filtering.Drain.
func pageCounts[T any](row pageRow[T]) (filtered, total int64) {
	return row.filtered, row.total
}

// pageValue reads the value off a row, for filtering.Drain.
func pageValue[T any](row pageRow[T]) *T { return row.value }

// Lists.

func listFromRow(r *waitlistsdb.GetListRow) *List {
	return &List{
		ID:            r.ID,
		Scope:         r.Scope,
		Name:          r.Name,
		Description:   r.Description,
		ClosesAt:      r.ClosesAt.UTC(),
		CreatedAt:     r.CreatedAt.UTC(),
		LastUpdatedAt: utcPtr(r.LastUpdatedAt),
		ArchivedAt:    utcPtr(r.ArchivedAt),
	}
}

func listPageRow(r *waitlistsdb.ListListsRow) pageRow[List] {
	return pageRow[List]{
		value: &List{
			ID:            r.ID,
			Scope:         r.Scope,
			Name:          r.Name,
			Description:   r.Description,
			ClosesAt:      r.ClosesAt.UTC(),
			CreatedAt:     r.CreatedAt.UTC(),
			LastUpdatedAt: utcPtr(r.LastUpdatedAt),
			ArchivedAt:    utcPtr(r.ArchivedAt),
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func openListPageRow(r *waitlistsdb.ListOpenListsRow) pageRow[List] {
	return pageRow[List]{
		value: &List{
			ID:            r.ID,
			Scope:         r.Scope,
			Name:          r.Name,
			Description:   r.Description,
			ClosesAt:      r.ClosesAt.UTC(),
			CreatedAt:     r.CreatedAt.UTC(),
			LastUpdatedAt: utcPtr(r.LastUpdatedAt),
			ArchivedAt:    utcPtr(r.ArchivedAt),
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func createListParams(l *List, scope tenancy.Scope) waitlistsdb.CreateListParams {
	return waitlistsdb.CreateListParams{
		ID:          l.ID,
		Scope:       scope,
		Name:        l.Name,
		Description: l.Description,
		ClosesAt:    l.ClosesAt.UTC(),
	}
}

func updateListParams(l *List, scope tenancy.Scope) waitlistsdb.UpdateListParams {
	return waitlistsdb.UpdateListParams{
		Name:        l.Name,
		Description: l.Description,
		ClosesAt:    l.ClosesAt.UTC(),
		ID:          l.ID,
		Scope:       scope,
	}
}

func listListsParams(scope tenancy.Scope, filter *filtering.QueryFilter) waitlistsdb.ListListsParams {
	w := windowFrom(filter)

	return waitlistsdb.ListListsParams{
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

func listOpenListsParams(
	scope tenancy.Scope,
	asOf time.Time,
	filter *filtering.QueryFilter,
) waitlistsdb.ListOpenListsParams {
	w := windowFrom(filter)

	return waitlistsdb.ListOpenListsParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		OpenAsOf:        asOf.UTC(),
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

// Signups.

func signupFromRow(r *waitlistsdb.GetSignupRow) *Signup {
	return &Signup{
		ID:              r.ID,
		Scope:           r.Scope,
		ListID:          r.WaitlistID,
		Contact:         r.Contact,
		ContactDigest:   r.ContactDigest,
		Subject:         Subject{Type: SubjectType(r.SubjectType), ID: r.SubjectID},
		Notes:           r.Notes,
		Status:          Status(r.Status),
		StatusChangedAt: utcPtr(r.StatusChangedAt),
		CreatedAt:       r.CreatedAt.UTC(),
		LastUpdatedAt:   utcPtr(r.LastUpdatedAt),
		ArchivedAt:      utcPtr(r.ArchivedAt),
	}
}

func signupFromDigestRow(r *waitlistsdb.GetSignupByContactDigestRow) *Signup {
	return &Signup{
		ID:              r.ID,
		Scope:           r.Scope,
		ListID:          r.WaitlistID,
		Contact:         r.Contact,
		ContactDigest:   r.ContactDigest,
		Subject:         Subject{Type: SubjectType(r.SubjectType), ID: r.SubjectID},
		Notes:           r.Notes,
		Status:          Status(r.Status),
		StatusChangedAt: utcPtr(r.StatusChangedAt),
		CreatedAt:       r.CreatedAt.UTC(),
		LastUpdatedAt:   utcPtr(r.LastUpdatedAt),
		ArchivedAt:      utcPtr(r.ArchivedAt),
	}
}

func signupPageRow(r *waitlistsdb.ListSignupsRow) pageRow[Signup] {
	return pageRow[Signup]{
		value: &Signup{
			ID:              r.ID,
			Scope:           r.Scope,
			ListID:          r.WaitlistID,
			Contact:         r.Contact,
			ContactDigest:   r.ContactDigest,
			Subject:         Subject{Type: SubjectType(r.SubjectType), ID: r.SubjectID},
			Notes:           r.Notes,
			Status:          Status(r.Status),
			StatusChangedAt: utcPtr(r.StatusChangedAt),
			CreatedAt:       r.CreatedAt.UTC(),
			LastUpdatedAt:   utcPtr(r.LastUpdatedAt),
			ArchivedAt:      utcPtr(r.ArchivedAt),
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func subjectSignupPageRow(r *waitlistsdb.ListSignupsForSubjectRow) pageRow[Signup] {
	return pageRow[Signup]{
		value: &Signup{
			ID:              r.ID,
			Scope:           r.Scope,
			ListID:          r.WaitlistID,
			Contact:         r.Contact,
			ContactDigest:   r.ContactDigest,
			Subject:         Subject{Type: SubjectType(r.SubjectType), ID: r.SubjectID},
			Notes:           r.Notes,
			Status:          Status(r.Status),
			StatusChangedAt: utcPtr(r.StatusChangedAt),
			CreatedAt:       r.CreatedAt.UTC(),
			LastUpdatedAt:   utcPtr(r.LastUpdatedAt),
			ArchivedAt:      utcPtr(r.ArchivedAt),
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func insertSignupParams(s *Signup, scope tenancy.Scope) waitlistsdb.InsertSignupParams {
	return waitlistsdb.InsertSignupParams{
		ID:              s.ID,
		Scope:           scope,
		WaitlistID:      s.ListID,
		Contact:         s.Contact,
		ContactDigest:   s.ContactDigest,
		SubjectType:     string(s.Subject.Type),
		SubjectID:       s.Subject.ID,
		Notes:           s.Notes,
		Status:          string(s.Status),
		StatusChangedAt: utcPtr(s.StatusChangedAt),
	}
}

func listSignupsParams(
	scope tenancy.Scope,
	listID string,
	filter *filtering.QueryFilter,
) waitlistsdb.ListSignupsParams {
	w := windowFrom(filter)

	return waitlistsdb.ListSignupsParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		WaitlistID:      listID,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func listSignupsForSubjectParams(
	scope tenancy.Scope,
	subject Subject,
	filter *filtering.QueryFilter,
) waitlistsdb.ListSignupsForSubjectParams {
	w := windowFrom(filter)

	return waitlistsdb.ListSignupsForSubjectParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		SubjectType:     string(subject.Type),
		SubjectID:       subject.ID,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

// transitionSignupParams is the guarded lifecycle move: the status the row must
// already hold, and the one it takes on.
func transitionSignupParams(
	scope tenancy.Scope,
	listID, signupID string,
	from, to Status,
	at time.Time,
) waitlistsdb.TransitionSignupParams {
	stamped := at.UTC()

	return waitlistsdb.TransitionSignupParams{
		Status:          string(to),
		StatusChangedAt: &stamped,
		ID:              signupID,
		Scope:           scope,
		WaitlistID:      listID,
		ExpectedStatus:  string(from),
	}
}

// withdrawSignupParams is the erasure: everything that identifies a person
// blanked, the digest untouched, and the guard inverted so a row that is already
// withdrawn matches nothing.
func withdrawSignupParams(
	scope tenancy.Scope,
	listID, signupID string,
	at time.Time,
) waitlistsdb.WithdrawSignupParams {
	stamped := at.UTC()

	return waitlistsdb.WithdrawSignupParams{
		Contact:         "",
		SubjectType:     "",
		SubjectID:       "",
		Notes:           "",
		Status:          string(StatusWithdrawn),
		StatusChangedAt: &stamped,
		ID:              signupID,
		Scope:           scope,
		WaitlistID:      listID,
		ExpectedStatus:  string(StatusWithdrawn),
	}
}

// withdrawSignupsForSubjectParams is the same erasure over every signup one
// principal holds in the scope: the same columns blanked, the same status
// pair stamped, and the subject bound twice — once as what the predicate
// requires and once as the empty string the SET list assigns.
func withdrawSignupsForSubjectParams(
	scope tenancy.Scope,
	subject Subject,
	at time.Time,
) waitlistsdb.WithdrawSignupsForSubjectParams {
	stamped := at.UTC()

	return waitlistsdb.WithdrawSignupsForSubjectParams{
		Contact:           "",
		SubjectType:       "",
		SubjectID:         "",
		Notes:             "",
		Status:            string(StatusWithdrawn),
		StatusChangedAt:   &stamped,
		Scope:             scope,
		ErasedSubjectType: string(subject.Type),
		ErasedSubjectID:   subject.ID,
	}
}

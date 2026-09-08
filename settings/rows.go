package settings

import (
	"time"

	"github.com/primandproper/platform-go/v14/settings/internal/settingsdb"

	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"
)

// The typed seam between the generated package and the domain types.
//
// settings/internal/settingsdb is sqlc-gen-unison's output: one params and one
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
// Every timestamp this package writes is the database's, so every one it returns
// is normalized here — Postgres hands back a time in the session's zone, MySQL
// in the server's, and SQLite whatever the string parsed as, so a caller
// comparing two of those, or rendering one into JSON, would otherwise get an
// answer that depends on where the row was read.
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

// pageValue reads the value off a row, for filtering.Drain. The value is
// returned as it stands rather than copied, so whatever a caller did to the
// slice of pointers before draining — attaching enumerations — is what the page
// carries.
func pageValue[T any](row pageRow[T]) *T { return row.value }

// pageValues collects a page's values, for the passes a caller makes over them
// before draining.
func pageValues[T any](rows []pageRow[T]) []*T {
	values := make([]*T, 0, len(rows))
	for _, row := range rows {
		values = append(values, row.value)
	}

	return values
}

// Definitions.

func definitionFromRow(r *settingsdb.GetDefinitionRow) *Definition {
	return &Definition{
		ID:            r.ID,
		Scope:         r.Scope,
		Name:          r.Name,
		Description:   r.Description,
		Kind:          Kind(r.Kind),
		Default:       r.DefaultValue,
		AdminOnly:     r.AdminOnly,
		CreatedAt:     r.CreatedAt.UTC(),
		LastUpdatedAt: utcPtr(r.LastUpdatedAt),
		ArchivedAt:    utcPtr(r.ArchivedAt),
	}
}

func definitionFromNameRow(r *settingsdb.GetDefinitionByNameRow) *Definition {
	return &Definition{
		ID:            r.ID,
		Scope:         r.Scope,
		Name:          r.Name,
		Description:   r.Description,
		Kind:          Kind(r.Kind),
		Default:       r.DefaultValue,
		AdminOnly:     r.AdminOnly,
		CreatedAt:     r.CreatedAt.UTC(),
		LastUpdatedAt: utcPtr(r.LastUpdatedAt),
		ArchivedAt:    utcPtr(r.ArchivedAt),
	}
}

func definitionPageRow(r *settingsdb.ListDefinitionsRow) pageRow[Definition] {
	return pageRow[Definition]{
		value: &Definition{
			ID:            r.ID,
			Scope:         r.Scope,
			Name:          r.Name,
			Description:   r.Description,
			Kind:          Kind(r.Kind),
			Default:       r.DefaultValue,
			AdminOnly:     r.AdminOnly,
			CreatedAt:     r.CreatedAt.UTC(),
			LastUpdatedAt: utcPtr(r.LastUpdatedAt),
			ArchivedAt:    utcPtr(r.ArchivedAt),
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func createDefinitionParams(d *Definition, scope tenancy.Scope) settingsdb.CreateDefinitionParams {
	return settingsdb.CreateDefinitionParams{
		ID:           d.ID,
		Scope:        scope,
		Name:         d.Name,
		Description:  d.Description,
		Kind:         string(d.Kind),
		DefaultValue: d.Default,
		AdminOnly:    d.AdminOnly,
	}
}

func updateDefinitionParams(d *Definition, scope tenancy.Scope) settingsdb.UpdateDefinitionParams {
	return settingsdb.UpdateDefinitionParams{
		Name:         d.Name,
		Description:  d.Description,
		Kind:         string(d.Kind),
		DefaultValue: d.Default,
		AdminOnly:    d.AdminOnly,
		ID:           d.ID,
		Scope:        scope,
	}
}

func listDefinitionsParams(scope tenancy.Scope, filter *filtering.QueryFilter) settingsdb.ListDefinitionsParams {
	w := windowFrom(filter)

	return settingsdb.ListDefinitionsParams{
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

// Values.

func valueFromRow(r *settingsdb.GetValueRow) *Value {
	return &Value{
		ID:            r.ID,
		Scope:         r.Scope,
		DefinitionID:  r.DefinitionID,
		Subject:       Subject{Type: SubjectType(r.SubjectType), ID: r.SubjectID},
		Raw:           r.Value,
		CreatedAt:     r.CreatedAt.UTC(),
		LastUpdatedAt: utcPtr(r.LastUpdatedAt),
		ArchivedAt:    utcPtr(r.ArchivedAt),
	}
}

func valuePageRowForSubject(r *settingsdb.ListValuesForSubjectRow) pageRow[Value] {
	return pageRow[Value]{
		value: &Value{
			ID:            r.ID,
			Scope:         r.Scope,
			DefinitionID:  r.DefinitionID,
			Subject:       Subject{Type: SubjectType(r.SubjectType), ID: r.SubjectID},
			Raw:           r.Value,
			CreatedAt:     r.CreatedAt.UTC(),
			LastUpdatedAt: utcPtr(r.LastUpdatedAt),
			ArchivedAt:    utcPtr(r.ArchivedAt),
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func valuePageRowForDefinition(r *settingsdb.ListValuesForDefinitionRow) pageRow[Value] {
	return pageRow[Value]{
		value: &Value{
			ID:            r.ID,
			Scope:         r.Scope,
			DefinitionID:  r.DefinitionID,
			Subject:       Subject{Type: SubjectType(r.SubjectType), ID: r.SubjectID},
			Raw:           r.Value,
			CreatedAt:     r.CreatedAt.UTC(),
			LastUpdatedAt: utcPtr(r.LastUpdatedAt),
			ArchivedAt:    utcPtr(r.ArchivedAt),
		},
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func listValuesForSubjectParams(
	scope tenancy.Scope,
	subject Subject,
	filter *filtering.QueryFilter,
) settingsdb.ListValuesForSubjectParams {
	w := windowFrom(filter)

	return settingsdb.ListValuesForSubjectParams{
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

func listValuesForDefinitionParams(
	scope tenancy.Scope,
	definitionID string,
	filter *filtering.QueryFilter,
) settingsdb.ListValuesForDefinitionParams {
	w := windowFrom(filter)

	return settingsdb.ListValuesForDefinitionParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		DefinitionID:    definitionID,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func upsertValueParams(id string, scope tenancy.Scope, subject Subject, definitionID, raw string) settingsdb.UpsertValueParams {
	return settingsdb.UpsertValueParams{
		ID:           id,
		Scope:        scope,
		DefinitionID: definitionID,
		SubjectType:  string(subject.Type),
		SubjectID:    subject.ID,
		Value:        raw,
	}
}

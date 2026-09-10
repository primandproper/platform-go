package mediaregistry

import (
	"time"

	"github.com/primandproper/platform-go/v14/mediaregistry/internal/registrydb"

	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"
)

// The typed seam between the generated package and the domain type.
//
// mediaregistry/internal/registrydb is sqlc-gen-unison's output: one params
// and one row struct per statement, the same on all three dialects. These
// functions are the whole of what this package does with them — a row becomes an
// Object, an Object becomes the params — and every one is a struct literal on
// purpose. A renamed or retyped column changes the generated struct, and every
// conversion here stops compiling; a scan-by-position pairing would report the
// same mistake as a runtime scan error, or worse, as two same-typed columns
// silently transposed.
//
// The row structs are nominal per statement, so a list row cannot convert to a
// get row even where the columns agree — which is why the converters below
// restate the fields rather than casting. Restating is the cost; the compiler
// checking every field name is what it buys.

// utcPtr normalizes an optional timestamp to UTC, preserving absence.
//
// Postgres hands back a time in the session's zone, MySQL in the server's, and
// SQLite whatever the string parsed as, so a caller comparing two of those, or
// rendering one into JSON, would otherwise get an answer that depends on where
// the row was read.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// createObjectParams renders an ObjectInput, under the scope the write named and
// the id it settled on, as the create's arguments.
//
// The three convention timestamps are absent because the database owns them —
// see mediaregistry/internal/queries — and the scope binds as the Scope the call
// passed rather than a string derived from it, so an unset scope is a driver
// error instead of a row silently written into the global tenant. The input
// carries no scope of its own to read instead.
//
// The id is an argument rather than a field for the same reason: RecordObject
// mints it where the input carries none, and what the write settled belongs on
// the row it reads back rather than anywhere the caller can see it.
func createObjectParams(scope tenancy.Scope, id string, in ObjectInput) registrydb.CreateObjectParams { //nolint:gocritic // hugeParam: by value on purpose — see ObjectInput, and the call does a round trip
	return registrydb.CreateObjectParams{
		ID:            id,
		Scope:         scope,
		ObjectKey:     in.Key,
		ContentType:   in.ContentType,
		SizeBytes:     in.Size,
		OwnerID:       in.OwnerID,
		BelongsToType: in.BelongsTo.Type,
		BelongsToID:   in.BelongsTo.ID,
	}
}

// objectFromRow is the one conversion that builds an Object; every other row
// shape below restates itself into a GetObjectRow and comes through here, so
// there is one place a column becomes a field.
func objectFromRow(r *registrydb.GetObjectRow) *Object {
	return &Object{
		CreatedAt:     r.CreatedAt.UTC(),
		LastUpdatedAt: utcPtr(r.LastUpdatedAt),
		ArchivedAt:    utcPtr(r.ArchivedAt),
		ID:            r.ID,
		Scope:         r.Scope,
		Key:           r.ObjectKey,
		ContentType:   r.ContentType,
		OwnerID:       r.OwnerID,
		BelongsTo:     Subject{Type: r.BelongsToType, ID: r.BelongsToID},
		Size:          r.SizeBytes,
	}
}

// objectFromArchivedRow converts the archive's read-back, which is the one row
// shape here that casts rather than restating itself.
//
// It is sortedRows' reason rather than an exception to the preamble's rule.
// GetArchivedObject projects the same list GetObject does — ObjectColumns, in
// that order — and differs from it only in which rows it will look at, so the
// two row types are one projection rendered twice. The conversion is therefore
// the assertion: the day the two projections stop agreeing, in field name, type
// or order, this stops building rather than filling the wrong fields.
func objectFromArchivedRow(r *registrydb.GetArchivedObjectRow) *Object {
	row := registrydb.GetObjectRow(*r)

	return objectFromRow(&row)
}

func objectFromKeyRow(r *registrydb.GetObjectByKeyRow) *Object {
	return objectFromRow(&registrydb.GetObjectRow{
		ID:            r.ID,
		Scope:         r.Scope,
		ObjectKey:     r.ObjectKey,
		ContentType:   r.ContentType,
		SizeBytes:     r.SizeBytes,
		OwnerID:       r.OwnerID,
		BelongsToType: r.BelongsToType,
		BelongsToID:   r.BelongsToID,
		CreatedAt:     r.CreatedAt,
		LastUpdatedAt: r.LastUpdatedAt,
		ArchivedAt:    r.ArchivedAt,
	})
}

// pageRow is one row of a rendered list query: the value, and the two counts the
// statement carries beside it.
//
// The counts ride on the rows rather than arriving from a second query, which is
// what makes a page and the number describing it come from one snapshot of the
// table. It also means a page with no rows carries no counts — see
// filtering.Drain, which reports that as unknown rather than as zero.
//
// It exists rather than handing filtering.Drain the generated row directly
// because those rows are wide: three of them are the whole table plus two
// counts, and Drain takes its converters by value.
type pageRow struct {
	value    *Object
	filtered int64
	total    int64
}

// pageValue and pageCounts are what filtering.Drain reads off a row.
func pageValue(row pageRow) *Object { return row.value }

func pageCounts(row pageRow) (filtered, total int64) { return row.filtered, row.total }

func objectPageRow(r *registrydb.ListObjectsRow) pageRow {
	return pageRow{
		value: objectFromRow(&registrydb.GetObjectRow{
			ID:            r.ID,
			Scope:         r.Scope,
			ObjectKey:     r.ObjectKey,
			ContentType:   r.ContentType,
			SizeBytes:     r.SizeBytes,
			OwnerID:       r.OwnerID,
			BelongsToType: r.BelongsToType,
			BelongsToID:   r.BelongsToID,
			CreatedAt:     r.CreatedAt,
			LastUpdatedAt: r.LastUpdatedAt,
			ArchivedAt:    r.ArchivedAt,
		}),
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func objectPageRowForOwner(r *registrydb.ListObjectsByOwnerRow) pageRow {
	return pageRow{
		value: objectFromRow(&registrydb.GetObjectRow{
			ID:            r.ID,
			Scope:         r.Scope,
			ObjectKey:     r.ObjectKey,
			ContentType:   r.ContentType,
			SizeBytes:     r.SizeBytes,
			OwnerID:       r.OwnerID,
			BelongsToType: r.BelongsToType,
			BelongsToID:   r.BelongsToID,
			CreatedAt:     r.CreatedAt,
			LastUpdatedAt: r.LastUpdatedAt,
			ArchivedAt:    r.ArchivedAt,
		}),
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

func objectPageRowForSubject(r *registrydb.ListObjectsBySubjectRow) pageRow {
	return pageRow{
		value: objectFromRow(&registrydb.GetObjectRow{
			ID:            r.ID,
			Scope:         r.Scope,
			ObjectKey:     r.ObjectKey,
			ContentType:   r.ContentType,
			SizeBytes:     r.SizeBytes,
			OwnerID:       r.OwnerID,
			BelongsToType: r.BelongsToType,
			BelongsToID:   r.BelongsToID,
			CreatedAt:     r.CreatedAt,
			LastUpdatedAt: r.LastUpdatedAt,
			ArchivedAt:    r.ArchivedAt,
		}),
		filtered: r.FilteredCount,
		total:    r.TotalCount,
	}
}

// objectID is the cursor filtering.Drain derives the next page from.
func objectID(o *Object) string { return o.ID }

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

func listObjectsParams(scope tenancy.Scope, filter *filtering.QueryFilter) registrydb.ListObjectsParams {
	w := windowFrom(filter)

	return registrydb.ListObjectsParams{
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

func listByOwnerParams(scope tenancy.Scope, ownerID string, filter *filtering.QueryFilter) registrydb.ListObjectsByOwnerParams {
	w := windowFrom(filter)

	return registrydb.ListObjectsByOwnerParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		OwnerID:         ownerID,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func listBySubjectParams(scope tenancy.Scope, subject Subject, filter *filtering.QueryFilter) registrydb.ListObjectsBySubjectParams {
	w := windowFrom(filter)

	return registrydb.ListObjectsBySubjectParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		BelongsToType:   subject.Type,
		BelongsToID:     subject.ID,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
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

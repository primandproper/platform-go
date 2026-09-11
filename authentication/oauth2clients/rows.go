package oauth2clients

import (
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/internal/oauth2clientsdb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
)

// The generated row types are one projection rendered five times — the get, the
// lookup, and the two pages in both directions — with different predicates and
// nothing else changed. So the conversions below convert rather than restate
// field by field wherever Go will let them: the day two of those projections
// stop being identical, in field name, type or order, this file stops building
// rather than filling the wrong fields.

// clientFromRow builds a Client out of the shape every read projects.
//
// It takes the get's row type, and the other four reads convert into it. The
// only fallible step is the two JSON lists, which is why this returns an error
// at all — a row whose redirect_uris column is not a JSON array is a row
// something outside this package wrote.
func clientFromRow(row *oauth2clientsdb.GetRegisteredClientRow) (*Client, error) {
	redirectURIs, err := decodeStrings(row.RedirectUris)
	if err != nil {
		return nil, platformerrors.Wrap(err, "decoding redirect URIs")
	}

	scopes, err := decodeStrings(row.Scopes)
	if err != nil {
		return nil, platformerrors.Wrap(err, "decoding scopes")
	}

	return &Client{
		CreatedAt:     row.CreatedAt,
		LastUpdatedAt: row.LastUpdatedAt,
		ArchivedAt:    row.ArchivedAt,
		Scope:         row.Scope,
		BelongsToUser: row.BelongsToUser,
		ID:            row.ID,
		ClientID:      row.ClientID,
		SecretHash:    row.SecretHash,
		Name:          row.Name,
		Description:   row.Description,
		RedirectURIs:  redirectURIs,
		Scopes:        scopes,
	}, nil
}

// encodeStrings renders a string slice for a text column.
//
// json.Marshal of a []string has no failure mode — every string is encodable and
// there is no cycle to find — so the error is dropped rather than returned up a
// path that could do nothing with it.
//
// This is deliberately the same encoding authentication/oauth2serverstore
// writes for the same kind of list. The two never read each other's columns, so
// the copy cannot drift into a wrong answer; what it buys is that a person
// looking at either table sees one shape.
func encodeStrings(values []string) string {
	if len(values) == 0 {
		// "[]" rather than "" so the column is always valid JSON, and a reader
		// never has to treat empty as a third case beside "none" and "some".
		return "[]"
	}

	//nolint:errcheck,errchkjson // json.Marshal cannot fail for []string; see the doc comment.
	encoded, _ := json.Marshal(values)

	return string(encoded)
}

// decodeStrings parses a string slice out of a text column.
func decodeStrings(encoded string) ([]string, error) {
	if encoded == "" || encoded == "[]" {
		// A nil slice, not an error and not an empty one: to every caller in
		// this package they are the same thing.
		return nil, nil
	}

	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, platformerrors.Wrap(err, "decoding string list")
	}

	return values, nil
}

// pageRow is one row of a rendered list query: the value, and the two counts the
// statement carries beside it.
//
// The counts ride on the rows rather than arriving from a second query, which is
// what makes a page and the number describing it come from one snapshot of the
// table. It also means a page with no rows carries no counts — see
// filtering.Drain, which reports that as unknown rather than as zero.
type pageRow struct {
	value    *Client
	filtered int64
	total    int64
}

// pageCounts reads the counts off a row, for filtering.Drain.
func pageCounts(row pageRow) (filtered, total int64) { return row.filtered, row.total }

// pageValue reads the value off a row, for filtering.Drain.
func pageValue(row pageRow) *Client { return row.value }

// clientPageRow converts one listed row into the shape filtering.Drain reads.
//
// The projection is restated field by field rather than converted, which is the
// one place in this file that cannot use a cast: a listed row carries the two
// counts beside the projection, so it is a wider struct than the get's and Go
// will not convert between them. The compiler still catches a renamed or
// retyped column here; what it cannot catch is a reordering, which is why the
// generated projection order is the order this literal is written in.
func clientPageRow(row *oauth2clientsdb.ListRegisteredClientsRow) (pageRow, error) {
	client, err := clientFromRow(&oauth2clientsdb.GetRegisteredClientRow{
		ID:            row.ID,
		Scope:         row.Scope,
		BelongsToUser: row.BelongsToUser,
		Name:          row.Name,
		Description:   row.Description,
		ClientID:      row.ClientID,
		SecretHash:    row.SecretHash,
		RedirectUris:  row.RedirectUris,
		Scopes:        row.Scopes,
		CreatedAt:     row.CreatedAt,
		LastUpdatedAt: row.LastUpdatedAt,
		ArchivedAt:    row.ArchivedAt,
	})
	if err != nil {
		return pageRow{}, err
	}

	return pageRow{value: client, filtered: row.FilteredCount, total: row.TotalCount}, nil
}

// sortedRows runs whichever of a paged read's two statements the filter's sort
// direction names, and hands back the ascending statement's rows either way.
//
// A paged list is two statements here, because a direction is which way the
// ORDER BY runs and which way the cursor comparison points — statement text, not
// a bound value, on all three engines. database/querygen emits the pair and
// filtering.QueryFilter.SortsDescending picks between them; this is where the
// pick is made, once, rather than at each of the two paged reads. A read that
// reached for the ascending statement while holding a descending filter would
// answer in the order the client did not ask for, and nothing about the rows
// that came back would say so.
//
// The descending rows are converted rather than restated field by field. These
// are one projection rendered twice, with the walk reversed and nothing else
// changed, so the conversion is the assertion and Go makes it the compiler's.
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

// pageFilter bounds a caller's filter, defaulting a nil one.
//
// It is where MaxResponseSize acquires a value, because the generated statements
// bind it as a plain int64 rather than a nullable: a page with no limit is a
// table scan the caller did not ask for.
func pageFilter(filter *filtering.QueryFilter) *filtering.QueryFilter {
	if filter == nil {
		return filtering.DefaultQueryFilter()
	}

	bounded := *filter

	size := uint16(filtering.DefaultQueryFilterLimit)
	if bounded.MaxResponseSize != nil {
		size = filtering.ClampResponseSize(uint64(*bounded.MaxResponseSize))
	}

	bounded.MaxResponseSize = &size

	return &bounded
}

// listWindow is the filter window every generated list statement binds. One
// reading of the filter, restated into each nominal params type by the callers.
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

// utcPtr normalizes a bound time to UTC. See listWindow for why.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

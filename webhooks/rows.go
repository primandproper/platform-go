package webhooks

import (
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v14/webhooks/internal/webhooksdb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The typed seam between the generated package and this one's types.
//
// webhooks/internal/webhooksdb is sqlc-gen-unison's output: one params and one
// row struct per statement, the same on all three dialects. These functions are
// the whole of what this package does with them — a row becomes an Endpoint, a
// Subscription, an Attempt or a ClaimedDispatch, and a value becomes the params
// — and every one is a struct literal on purpose. A renamed or retyped column
// changes the generated struct, and every conversion here stops compiling; the
// scan-by-position pairing these replaced reported the same mistake as a
// runtime scan error, or worse, as two same-typed columns silently transposed.
//
// The row structs are nominal per statement, so a list row cannot convert to a
// get row even where the columns agree, which is why the converters below
// restate the fields rather than casting. Restating is the cost; the compiler
// checking every field name is what it buys.

// endpointColumns is one endpoint's row, whichever statement projected it.
//
// Four statements project an endpoint — the get, the two pages, the fan-out
// lookup — and a fifth projects one under an alias beside a claimed dispatch.
// Their row types are five nominal structs with the same fields, so this is
// where they converge, once, rather than five copies of the conversion into an
// Endpoint.
type endpointColumns struct {
	CreatedAt      time.Time
	LastUpdatedAt  *time.Time
	ArchivedAt     *time.Time
	CreatedBy      *string
	SecretCurrent  []byte
	SecretPrevious []byte
	Headers        []byte
	ID             string
	Name           string
	URL            string
	ContentType    string
	Scope          tenancy.Scope
	Disabled       bool
}

// endpoint builds the domain value.
//
// The headers are the one column that can fail to convert: they are stored as
// the JSON object a caller supplied, and an endpoint registered before any were
// stored — or one whose column holds a JSON null — yields a nil map rather than
// an error.
func (c *endpointColumns) endpoint() (*Endpoint, error) {
	endpoint := &Endpoint{
		CreatedAt:     c.CreatedAt.UTC(),
		LastUpdatedAt: utcPtr(c.LastUpdatedAt),
		ArchivedAt:    utcPtr(c.ArchivedAt),
		Scope:         c.Scope,
		CreatedBy:     ownerScope(c.CreatedBy),
		ID:            c.ID,
		Name:          c.Name,
		URL:           c.URL,
		ContentType:   c.ContentType,
		Secret:        Secret{Current: c.SecretCurrent, Previous: c.SecretPrevious},
		Disabled:      c.Disabled,
	}

	if len(c.Headers) > 0 {
		if err := json.Unmarshal(c.Headers, &endpoint.Headers); err != nil {
			return nil, platformerrors.Wrapf(err, "unmarshaling headers for webhook endpoint %q", endpoint.ID)
		}
	}

	return endpoint, nil
}

func endpointFromGet(r *webhooksdb.GetEndpointRow) endpointColumns {
	return endpointColumns{
		CreatedAt:      r.CreatedAt,
		LastUpdatedAt:  r.LastUpdatedAt,
		ArchivedAt:     r.ArchivedAt,
		CreatedBy:      r.CreatedBy,
		SecretCurrent:  r.SecretCurrent,
		SecretPrevious: r.SecretPrevious,
		Headers:        r.Headers,
		ID:             r.ID,
		Name:           r.Name,
		URL:            r.URL,
		ContentType:    r.ContentType,
		Scope:          r.Scope,
		Disabled:       r.Disabled,
	}
}

func endpointFromListRow(r *webhooksdb.ListEndpointsRow) endpointColumns {
	return endpointColumns{
		CreatedAt:      r.CreatedAt,
		LastUpdatedAt:  r.LastUpdatedAt,
		ArchivedAt:     r.ArchivedAt,
		CreatedBy:      r.CreatedBy,
		SecretCurrent:  r.SecretCurrent,
		SecretPrevious: r.SecretPrevious,
		Headers:        r.Headers,
		ID:             r.ID,
		Name:           r.Name,
		URL:            r.URL,
		ContentType:    r.ContentType,
		Scope:          r.Scope,
		Disabled:       r.Disabled,
	}
}

func endpointFromEventRow(r *webhooksdb.ListEndpointsForEventRow) endpointColumns {
	return endpointColumns{
		CreatedAt:      r.CreatedAt,
		LastUpdatedAt:  r.LastUpdatedAt,
		ArchivedAt:     r.ArchivedAt,
		CreatedBy:      r.CreatedBy,
		SecretCurrent:  r.SecretCurrent,
		SecretPrevious: r.SecretPrevious,
		Headers:        r.Headers,
		ID:             r.ID,
		Name:           r.Name,
		URL:            r.URL,
		ContentType:    r.ContentType,
		Scope:          r.Scope,
		Disabled:       r.Disabled,
	}
}

// endpointFromClaimedRow reads the endpoint half of a claimed dispatch, which
// the statement projects under an alias so that two tables sharing most of
// their column names do not collide in one row type.
func endpointFromClaimedRow(r *webhooksdb.FetchClaimedDispatchesRow) endpointColumns {
	return endpointColumns{
		CreatedAt:      r.EndpointCreatedAt,
		LastUpdatedAt:  r.EndpointLastUpdatedAt,
		ArchivedAt:     r.EndpointArchivedAt,
		CreatedBy:      r.EndpointCreatedBy,
		SecretCurrent:  r.EndpointSecretCurrent,
		SecretPrevious: r.EndpointSecretPrevious,
		Headers:        r.EndpointHeaders,
		ID:             r.EndpointID,
		Name:           r.EndpointName,
		URL:            r.EndpointURL,
		ContentType:    r.EndpointContentType,
		Scope:          r.EndpointScope,
		Disabled:       r.EndpointDisabled,
	}
}

// claimedFromRow builds the unit the worker delivers: the dispatch, the payload
// and event it carries, the tenant it belongs to, and the subscriber resolved
// at claim time.
func claimedFromRow(r *webhooksdb.FetchClaimedDispatchesRow) (ClaimedDispatch, error) {
	endpoint := endpointFromClaimedRow(r)

	resolved, err := endpoint.endpoint()
	if err != nil {
		return ClaimedDispatch{}, err
	}

	return ClaimedDispatch{
		Endpoint:    resolved,
		Payload:     r.Payload,
		Scope:       r.Scope,
		EventType:   EventType(r.EventType),
		ID:          r.ID,
		DeliveryID:  r.DeliveryID,
		EndpointID:  r.EndpointID,
		OrderingKey: r.OrderingKey,
		Attempts:    int(r.Attempts),
	}, nil
}

// subscriptionColumns is one subscription's row, whichever statement projected
// it. Four do, for the reason five project an endpoint.
type subscriptionColumns struct {
	CreatedAt     time.Time
	LastUpdatedAt *time.Time
	ArchivedAt    *time.Time
	ID            string
	EndpointID    string
	EventType     string
}

func (c *subscriptionColumns) subscription() Subscription {
	return Subscription{
		CreatedAt:     c.CreatedAt.UTC(),
		LastUpdatedAt: utcPtr(c.LastUpdatedAt),
		ArchivedAt:    utcPtr(c.ArchivedAt),
		ID:            c.ID,
		EndpointID:    c.EndpointID,
		EventType:     EventType(c.EventType),
	}
}

func subscriptionFromGet(r *webhooksdb.GetSubscriptionRow) subscriptionColumns {
	return subscriptionColumns{
		CreatedAt:     r.CreatedAt,
		LastUpdatedAt: r.LastUpdatedAt,
		ArchivedAt:    r.ArchivedAt,
		ID:            r.ID,
		EndpointID:    r.EndpointID,
		EventType:     r.EventType,
	}
}

func subscriptionFromPair(r *webhooksdb.GetSubscriptionByPairRow) subscriptionColumns {
	return subscriptionColumns{
		CreatedAt:     r.CreatedAt,
		LastUpdatedAt: r.LastUpdatedAt,
		ArchivedAt:    r.ArchivedAt,
		ID:            r.ID,
		EndpointID:    r.EndpointID,
		EventType:     r.EventType,
	}
}

func subscriptionFromEndpointRow(r *webhooksdb.ListSubscriptionsForEndpointRow) subscriptionColumns {
	return subscriptionColumns{
		CreatedAt:     r.CreatedAt,
		LastUpdatedAt: r.LastUpdatedAt,
		ArchivedAt:    r.ArchivedAt,
		ID:            r.ID,
		EndpointID:    r.EndpointID,
		EventType:     r.EventType,
	}
}

func subscriptionFromListRow(r *webhooksdb.ListSubscriptionsRow) subscriptionColumns {
	return subscriptionColumns{
		CreatedAt:     r.CreatedAt,
		LastUpdatedAt: r.LastUpdatedAt,
		ArchivedAt:    r.ArchivedAt,
		ID:            r.ID,
		EndpointID:    r.EndpointID,
		EventType:     r.EventType,
	}
}

// attemptFromRow builds one line of the delivery log.
//
// The duration is stored in milliseconds and the status code and attempt count
// as 64-bit integers, because the three engines do not agree on the width of an
// INTEGER column and unison refuses a signature that differs between them. The
// narrowing back to the domain's own types happens here, once.
func attemptFromRow(r *webhooksdb.ListAttemptsRow) *Attempt {
	return &Attempt{
		CreatedAt:    r.CreatedAt.UTC(),
		ID:           r.ID,
		DeliveryID:   r.DeliveryID,
		EndpointID:   r.EndpointID,
		Error:        stringOrEmpty(r.Error),
		Duration:     time.Duration(r.DurationMs) * time.Millisecond,
		StatusCode:   int(r.StatusCode),
		AttemptCount: int(r.AttemptCount),
	}
}

// utcPtr normalizes an optional timestamp to UTC, preserving absence.
//
// Every timestamp this package writes is UTC, so every one it reads back is
// too: Postgres hands back a time in the session's zone, MySQL in the server's,
// and SQLite whatever the string parsed as, so a caller comparing two of those
// — or rendering one into JSON — would otherwise get an answer that depends on
// where the row was read.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// ownerScope maps a nullable created_by column back to a Scope: NULL is the
// unset one, and anything else — the empty identifier included — is the scope
// it names.
//
// tenancy.Scope.Scan refuses a NULL outright, which is right for the NOT NULL
// scope column it was written for and wrong here: this column is nullable
// because the attribution is optional, so the NULL has a meaning rather than
// being a schema mismatch.
func ownerScope(stored *string) tenancy.Scope {
	if stored == nil {
		return tenancy.Scope{}
	}

	return tenancy.FromOwner(*stored)
}

// ownerOrNil maps an unset CreatedBy to a SQL NULL rather than to the empty
// identifier.
//
// The empty identifier is tenancy.Global(), a scope like any other, so binding
// the owner directly would record "no principal registered this" and "the
// global principal registered this" in the same column value — and Scope.Value
// refuses the unset one outright, which is right for a required column and
// wrong for an optional one. NULL is the absence, and ownerScope maps it back.
func ownerOrNil(scope tenancy.Scope) *string {
	if scope.Validate() != nil {
		return nil
	}

	owner := scope.Owner()

	return &owner
}

// secretOrNil maps an empty previous secret to a SQL NULL rather than an empty
// blob, so "not rotating" and "rotating to an empty key" cannot be confused in
// the column.
func secretOrNil(secret []byte) []byte {
	if len(secret) == 0 {
		return nil
	}

	return secret
}

// stringOrEmpty reads a nullable text column as the empty string, which is what
// the domain types hold for a fact that is not there: an attempt that succeeded
// has no error, and a dispatch that has not failed has no last one.
func stringOrEmpty(stored *string) string {
	if stored == nil {
		return ""
	}

	return *stored
}

// textOrNil is stringOrEmpty's inverse, for the columns that distinguish an
// absent string from an empty one.
func textOrNil(value string) *string {
	if value == "" {
		return nil
	}

	return &value
}

// timeOrNil binds an optional instant. The three columns that take one —
// claimed_until, delivered_at, and the retention horizon a reap deletes behind
// — are nullable, so the generated parameter is a pointer whether or not the
// caller has a value to put in it.
func timeOrNil(at time.Time) *time.Time {
	utc := at.UTC()

	return &utc
}

// listWindow is the filter window every generated list statement binds, in the
// shape the generated params carry it. One reading of the filter, restated into
// each nominal params type by the constructors at the call sites.
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
// pageFilter, so MaxResponseSize is set; only IncludeArchived defaults here,
// and it defaults to excluding, which is what the statement's COALESCE would
// have done with an absent flag anyway.
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

// pageFilter fills in what a paged read needs and the caller may have left out.
//
// A page size that is present and zero is left alone and returns no rows, which
// is the loud reading of an explicit zero and the same distinction
// filtering.ClampResponseSize draws. Only absence is defaulted. The sort
// direction passes through untouched and is read where it is used.
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

// sortedRows runs whichever of a paged read's two statements the filter's sort
// direction names, and hands back the ascending statement's rows either way.
//
// A paged list is two statements here, because a direction is which way the
// ORDER BY runs and which way the cursor comparison points — statement text,
// not a bound value, on all three engines. database/querygen emits the pair and
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
// being identical, in field name, type or order, this stops building rather
// than filling the wrong fields.
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

// pageRow is one row of a rendered list query: the value, and the two counts
// the statement carries beside it.
//
// The counts ride on the rows rather than arriving from a second query, which
// is what makes a page and the numbers describing it come from one snapshot of
// the table — the three counting statements this replaced were a second round
// trip against a table that had moved on. It also means a page with no rows
// carries no counts; see filtering.Drain, which reports that as unknown rather
// than as zero.
type pageRow[T any] struct {
	value    *T
	filtered int64
	total    int64
}

// pageValue reads the value off a row, for filtering.Drain. The value is
// returned as it stands rather than copied, so whatever a caller did to the
// slice of pointers before draining — attaching subscriptions — is what the
// page carries.
func pageValue[T any](row pageRow[T]) *T { return row.value }

// pageCounts reads the counts off a row, for filtering.Drain.
func pageCounts[T any](row pageRow[T]) (filtered, total int64) {
	return row.filtered, row.total
}

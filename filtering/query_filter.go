// Package filtering is the shared vocabulary for list queries: which slice of a
// collection a caller asked for, and which slice they got.
//
// A QueryFilter carries the request half — a cursor, a page size, a sort
// direction, created/updated time windows, and whether archived rows count — and
// round-trips through URL query parameters, so the same value is what a handler
// parses out of an *http.Request and what a client puts back on the wire. The
// query-parameter names are exported constants rather than string literals
// spelled out per handler, which is what keeps a client and a server agreeing on
// them. Pagination is the response half, and is what an API response embeds
// alongside its data to say what was applied and where the next page starts.
//
// It builds no SQL and touches no database. This package decides what a caller
// asked for; translating that into a query belongs to whatever store answers it.
//
// Page size is clamped rather than rejected: a request for more than
// MaxQueryFilterLimit gets MaxQueryFilterLimit, and an absent one gets
// DefaultQueryFilterLimit. A parameter that is present and unreadable is a
// different matter, and parsing reports it — every parameter is still attempted
// and everything that parsed is applied, so the filter is always usable, but the
// error is there because a mistyped filter that is silently dropped answers with
// a plausible-looking page that excludes nothing. The handler decides which of
// those it wants; the parse does not decide for it.
//
// A nil *QueryFilter is usable throughout: it renders as the default filter and
// says so when attached to a logger, so handlers need no nil check before
// passing one along.
//
// Cursors are directional, and the two halves of a round trip do not carry the
// same one. A QueryFilter's Cursor is the page being asked for. The Pagination
// that answers it reports PreviousCursor as the cursor that reached this page
// and Cursor as the one that reaches the next, so an empty PreviousCursor is
// the first page. Nothing needs to compare the two against each other to work
// that out, and a caller that does is inferring a contract stated here.
//
// The next cursor is the last row's identifier whenever the page held rows, so
// it is empty only for an empty page. It is not a "there is more" signal: a
// full page and the final page carry an equally non-empty Cursor, and the
// counts are what distinguish them.
//
// The counts, in turn, are answerable or not, and Pagination says which. A
// store that counts by carrying the numbers along on the rows — so that the
// page and the number describing it cannot come from two different moments —
// has nothing to read them off when the page came back empty, and the zero it
// would report is not "nothing matched" but "no row to carry the number on". A
// caller walking a keyset to its end sees those two as the same 0. CountsKnown
// separates them, and Counts is the read that cannot skip the question.
//
// Defaults and bounds apply to a filter however it arrived. Parsing one out of
// query parameters is only the transport that has a parser here; Normalize is
// the same rule for a filter decoded from anywhere else, and is what the schema
// describes to a caller that will never call it.
//
// The page-size ceiling is a var rather than a constant, so a service that
// wants a different one is not held to platform's. MaxQueryFilterLimit says
// what setting it costs and when it may be set; the schema follows it, so
// raising the clamp raises what the type publishes about itself as well.
//
// The page-size ceiling is the one bound Normalize cannot be left to apply on
// its own, because a decoder reaches uint16 before it reaches a QueryFilter and
// a page size that narrows before it is clamped wraps rather than clamps.
// SetMaxResponseSize takes the wide value a wire format actually carries and
// applies the ceiling in the order that works; ClampResponseSize is the same
// rule for a parser that has the number before it has a filter.
//
// QueryFilterSchema describes the request half as JSON Schema, for the surfaces
// that ask for a filter in that dialect rather than in query parameters: a
// tool-calling model, an MCP tool definition, an OpenAPI document. It is
// reflected off the struct, so it is the one description of this type there is.
package filtering

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	platformerrors "github.com/primandproper/platform-go/v12/errors"
	"github.com/primandproper/platform-go/v12/observability/keys"
	"github.com/primandproper/platform-go/v12/observability/logging"
)

const (
	// sortAscendingString is the pre-determined Ascending sortType for external use.
	sortAscendingString = "asc"
	// sortDescendingString is the pre-determined Descending sortType for external use.
	sortDescendingString = "desc"
)

var (
	// SortAscending is the pre-determined Ascending string for external use.
	SortAscending = new(sortAscendingString)
	// SortDescending is the pre-determined Descending string for external use.
	SortDescending = new(sortDescendingString)
)

// MaxQueryFilterLimit is the largest page a list query is answered with.
// Anything above it clamps to it, and ClampResponseSize is where that happens.
//
// It is a var because the ceiling is a policy question and this module is the
// wrong place to answer it for everybody: a service whose rows are three narrow
// columns has no reason to be held to a number picked with somebody else's rows
// in mind. Set it once during initialization — before the first filter is
// parsed and before any schema is reflected — and leave it alone after that.
// Nothing here guards it, so a write racing a list request is a data race like
// any other.
//
// The published bound follows it, which is the half that is easy to leave
// behind. QueryFilter carries no `maximum` struct tag, deliberately: a tag is
// fixed when this package compiles and this number is not, so a tag would go on
// promising 250 to every generated client and every tool-calling model while
// the clamp quietly enforced something else. PrepareJSONSchema writes the
// current value into every reflection of the type instead.
//
// The type is uint16 rather than an untyped constant because that is the type
// of the field it bounds, which makes a ceiling too large to store in a page
// size a compile error at the assignment rather than a truncation at the clamp.
// The field is uint16 rather than uint8 for its own reason: at uint8 the ceiling
// sat five above the limit, so `maxResponseSize: 300` was an unmarshal error
// rather than the clamp every other over-limit value gets. ClampResponseSize
// still takes a uint64, and still has to.
//
// DefaultQueryFilterLimit is deliberately not settable the same way. It is
// applied at runtime here, but it is also written into SQL at generate time —
// database/querygen emits `LIMIT COALESCE(sqlc.narg(result_limit), 50)` into
// files that are checked in — so a value changed in a running process would
// disagree with statements that shipped before it started.
var MaxQueryFilterLimit uint16 = 250

const (
	// DefaultQueryFilterLimit represents how many results we return in a response by default.
	DefaultQueryFilterLimit = 50

	// QueryKeySearchWithDatabase is the query param key to find search queries in requests.
	QueryKeySearchWithDatabase = "useDB"

	// QueryKeyLimit is the query param key to specify a limit in a query.
	QueryKeyLimit = "limit"
	// QueryKeyCursor is the query param key for specifying which cursor to use in a list query.
	QueryKeyCursor = "cursor"
	// QueryKeyCreatedBefore is the query param key for a creation time limit in a list query.
	QueryKeyCreatedBefore = "createdBefore"
	// QueryKeyCreatedAfter is the query param key for a creation time limit in a list query.
	QueryKeyCreatedAfter = "createdAfter"
	// QueryKeyUpdatedBefore is the query param key for an updated time limit in a list query.
	QueryKeyUpdatedBefore = "updatedBefore"
	// QueryKeyUpdatedAfter is the query param key for an updated time limit in a list query.
	QueryKeyUpdatedAfter = "updatedAfter"
	// QueryKeyIncludeArchived is the query param key for including archived results in a query.
	QueryKeyIncludeArchived = "includeArchived"
	// QueryKeySortBy is the query param key for sort order in a query.
	QueryKeySortBy = "sortBy"
)

type (
	// Pagination represents a pagination request.
	Pagination struct {
		_ struct{} `json:"-"`

		// AppliedQueryFilter is the filter this page was answered with, after
		// defaults and bounds were applied — not necessarily the one the client
		// sent.
		AppliedQueryFilter *QueryFilter `json:"appliedQueryFilter"`

		// Cursor reaches the page after this one. It is the last row's
		// identifier, so it is empty only when this page held no rows and says
		// nothing about whether a further page exists.
		Cursor string `json:"cursor"`

		// PreviousCursor is the cursor that reached this page, echoed back from
		// the filter that was applied. It is empty on the first page, which is
		// how the first page is recognized.
		PreviousCursor string `json:"previousCursor"`

		// FilteredCount is how many rows matched the filter and TotalCount how
		// many were in scope regardless of it. Neither describes this page:
		// they describe the collection it was cut from, which is why they do
		// not shrink as a caller walks it.
		//
		// Both mean nothing unless CountsKnown is set. Counts is the accessor
		// that hands them over with that fact attached.
		FilteredCount uint64 `json:"filteredCount"`

		TotalCount uint64 `json:"totalCount"`

		MaxResponseSize uint16 `json:"maxResponseSize"`

		// CountsKnown reports whether the counts above were answered at all.
		//
		// They are plain integers, so an unanswered pair reads as 0 and 0 —
		// which is also what a collection with nothing in it reads as, and
		// nothing else here tells those apart. A store whose counts ride along
		// on the rows, handed a page that came back empty, has no row to read
		// them off; a caller walking a keyset therefore sees FilteredCount go
		// 5, 5, 0, and the last of those is not a result. A UI rendering "0
		// results" off the final page of a walk is the obvious way to get this
		// wrong, and it looks correct in every test that does not page to the
		// end.
		//
		// False is the zero value, so a Pagination assembled as a literal
		// vouches for nothing until it says otherwise, and ToPagination — which
		// is built from a request, where there are no counts yet — leaves it
		// alone. NewQueryFilteredResult sets it, because a caller that passed
		// counts in has answered them. A caller with none to pass has
		// NewQueryFilteredResultWithoutCounts rather than a zero that means
		// something else.
		CountsKnown bool `json:"countsKnown"`
	}

	// QueryFilter represents all the filters a User could apply to a list query.
	//
	// The tags beyond `json` are the type's JSON Schema. QueryFilterSchema
	// reflects them, and so does the OpenAPI reflector routing runs, so what a
	// generated client is told about a filter and what a model on the other end
	// of a tool call is told about one come from the same place and cannot
	// disagree. Nothing about the schema is written out anywhere else — that is
	// the point, because a second copy of this struct can be wrong and nothing
	// would say so.
	//
	// The numbers here are literals because a struct tag cannot name a
	// constant. TestQueryFilterSchema_Bounds ties each one back to the constant
	// it repeats, which is what keeps the tag and the code from drifting apart.
	//
	// MaxResponseSize's ceiling is the one number that is not here at all. A tag
	// cannot name a constant, but it also cannot hold a value a consumer can
	// change, and MaxQueryFilterLimit is a var — so `maximum` is written by
	// PrepareJSONSchema, out of the var itself, on every reflection of this
	// type. `minimum` stays a tag, because zero is not a policy question.
	//
	// `nullable:"false"` is on every field because these are optional, not
	// nullable: an absent one filters nothing, and none of them is ever emitted
	// as null — `omitempty` sees to that. Left alone the reflector reads the
	// pointer and offers null as a value, which on SortBy would have contradicted
	// its own enum.
	//
	// The fields are separated by blank lines because tagalign pads a run of
	// adjacent ones out to the longest tag in it, and one description long
	// enough to be worth writing puts a hundred spaces in front of every other
	// field's `json`.
	QueryFilter struct {
		// The blank field carries the document-level keywords. Its `json:"-"`
		// is gone rather than joined by them: the reflector discards a field
		// tagged "-" before it reads anything else, and encoding/json skips a
		// blank field whatever it is tagged.
		_ struct{} `additionalProperties:"false" description:"The slice of a collection a caller is asking for: how many rows, where to resume, which direction, and optional windows on when a row was created or last updated. Every field is optional, and an absent one filters nothing."`

		SortBy *string `default:"asc" description:"Sort direction, not a column name: \"asc\" for oldest first, \"desc\" for newest first, and nothing else." enum:"asc,desc" json:"sortBy,omitempty" nullable:"false"`

		CreatedAfter *time.Time `description:"Only rows created after this instant." json:"createdAfter,omitempty" nullable:"false"`

		CreatedBefore *time.Time `description:"Only rows created before this instant." json:"createdBefore,omitempty" nullable:"false"`

		UpdatedAfter *time.Time `description:"Only rows last updated after this instant." json:"updatedAfter,omitempty" nullable:"false"`

		UpdatedBefore *time.Time `description:"Only rows last updated before this instant." json:"updatedBefore,omitempty" nullable:"false"`

		MaxResponseSize *uint16 `default:"50" description:"Maximum number of rows in one page. Absent means 50. A value above the maximum is clamped to it rather than rejected, so asking for more than the ceiling is answered with the ceiling." json:"maxResponseSize,omitempty" minimum:"0" nullable:"false"`

		IncludeArchived *bool `default:"false" description:"Include archived rows. Absent leaves them out." json:"includeArchived,omitempty" nullable:"false"`

		Cursor *string `description:"Opaque cursor from a previous response's cursor field. The page resumes after the row it names; absent starts at the beginning." json:"cursor,omitempty" nullable:"false"`
	}

	QueryFilteredResult[T any] struct {
		_    struct{} `json:"-"`
		Data []*T     `json:"data"`
		Pagination
	}
)

// DefaultQueryFilter builds the default query filter.
func DefaultQueryFilter() *QueryFilter {
	return &QueryFilter{
		MaxResponseSize: new(uint16(DefaultQueryFilterLimit)),
		SortBy:          SortAscending,
	}
}

// AttachToLogger attaches a QueryFilter's values to a logging.Logger.
func (qf *QueryFilter) AttachToLogger(logger logging.Logger) logging.Logger {
	l := logging.EnsureLogger(logger).Clone()

	if qf == nil {
		return l.WithValue(keys.FilterIsNilKey, true)
	}

	if qf.Cursor != nil {
		l = l.WithValue(QueryKeyCursor, qf.Cursor)
	}

	if qf.MaxResponseSize != nil {
		l = l.WithValue(QueryKeyLimit, qf.MaxResponseSize)
	}

	if qf.SortBy != nil {
		l = l.WithValue(QueryKeySortBy, qf.SortBy)
	}

	if qf.CreatedBefore != nil {
		l = l.WithValue(QueryKeyCreatedBefore, qf.CreatedBefore)
	}

	if qf.CreatedAfter != nil {
		l = l.WithValue(QueryKeyCreatedAfter, qf.CreatedAfter)
	}

	if qf.UpdatedBefore != nil {
		l = l.WithValue(QueryKeyUpdatedBefore, qf.UpdatedBefore)
	}

	if qf.UpdatedAfter != nil {
		l = l.WithValue(QueryKeyUpdatedAfter, qf.UpdatedAfter)
	}

	return l
}

// FromParams overrides the core QueryFilter values with values retrieved from
// url.Params, reporting any parameter that was supplied and could not be read.
//
// An absent parameter is not a failure — the filter simply keeps whatever it
// already held. A parameter that is present and unreadable is, and that is the
// distinction the method exists to draw. It used to make no distinction at all:
// `limit=fifty` and `createdAfter=yesterday` parsed to an error that was
// discarded, and the caller got an unfiltered list that looked exactly like a
// filtered one with nothing excluded. The person who notices is whoever reconciles
// the numbers a week later.
//
// Every parameter is attempted, so one bad value does not hide the next; the
// returned error joins all of them. Whatever did parse is applied, which makes an
// ignored error behave as the old method did — but a caller reporting the failure
// to a client should discard the filter rather than list against a half-applied
// one.
func (qf *QueryFilter) FromParams(params url.Values) error {
	var errs []error

	// unreadable names the parameter and the value that would not parse. The
	// value is the caller's own input coming back to them, and the transport
	// mappers answer ErrUnrecognizedInputValue with a constant message, so it
	// reaches logs and traces without reaching the response body.
	unreadable := func(key, value string, cause error) error {
		return platformerrors.Wrapf(
			platformerrors.Join(platformerrors.ErrUnrecognizedInputValue, cause),
			"reading %s parameter %q", key, value,
		)
	}

	// parseTime reads one RFC3339Nano parameter, recording an error only when the
	// parameter was supplied and would not parse.
	//
	// The absence check is not redundant with the parse: an absent parameter reads
	// as "", which time.Parse rejects by allocating a *time.ParseError. Four of
	// those per call, on every list request, for filters the overwhelming majority
	// of requests do not send — checking first costs a comparison and skips all of
	// it. It is also what keeps an unsent filter from being reported as an
	// unreadable one.
	parseTime := func(key string, into **time.Time) {
		raw := params.Get(key)
		if raw == "" {
			return
		}

		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			errs = append(errs, unreadable(key, raw, err))

			return
		}

		*into = &t
	}

	if i := params.Get(QueryKeyCursor); i != "" {
		qf.Cursor = &i
	}

	if raw := params.Get(QueryKeyLimit); raw != "" {
		i, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			errs = append(errs, unreadable(QueryKeyLimit, raw, err))
		} else {
			qf.MaxResponseSize = new(ClampResponseSize(i))
		}
	}

	parseTime(QueryKeyCreatedBefore, &qf.CreatedBefore)
	parseTime(QueryKeyCreatedAfter, &qf.CreatedAfter)
	parseTime(QueryKeyUpdatedBefore, &qf.UpdatedBefore)
	parseTime(QueryKeyUpdatedAfter, &qf.UpdatedAfter)

	if raw := params.Get(QueryKeyIncludeArchived); raw != "" {
		i, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, unreadable(QueryKeyIncludeArchived, raw, err))
		} else {
			qf.IncludeArchived = &i
		}
	}

	if raw := params.Get(QueryKeySortBy); raw != "" {
		switch strings.ToLower(raw) {
		case sortAscendingString:
			qf.SortBy = SortAscending
		case sortDescendingString:
			qf.SortBy = SortDescending
		default:
			// A sort order nobody recognized is the most expensive of these to
			// swallow: the request comes back sorted the other way, in full, and
			// looks entirely successful.
			errs = append(errs, platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue,
				"reading %s parameter %q", QueryKeySortBy, raw))
		}
	}

	return platformerrors.Join(errs...)
}

// ClampResponseSize is the page-size ceiling, and the only place it is applied.
//
// It takes a uint64 because the ceiling has to be applied before the narrowing
// to uint16, not after. Every wire format narrows first if it is left to do it
// alone: protobuf has no uint16, so a page size crosses as a uint32; JSON hands
// a decoder a number; a query parameter hands it a string. All three reach
// *uint16 before there is a QueryFilter to hold one, which is before Normalize
// can see the value — and a narrowing that happens first is silent. A requested
// 70000 wraps to 4464, Normalize clamps that to MaxQueryFilterLimit, and the
// client receives a legible-looking answer to a question it did not ask.
// Normalize cannot catch it by construction, because by the time it runs 4464 is
// indistinguishable from a page size the client actually sent.
//
// SetMaxResponseSize is this applied to a filter, and is what a decoder holding
// one should reach for, since a clamp that must be called is a clamp that can be
// forgotten. This is the bare function, for a parser that has the wide value
// before it has a filter to put it in.
//
// Still clamped rather than rejected: MaxQueryFilterLimit documents an
// over-large limit as a clamp, and a client asking for more than the ceiling has
// asked a legible question with a legible answer. Zero is left alone here rather
// than filled in — FromParams distinguishes a supplied value from an absent one
// and Normalize is what supplies the default, so a clamp that also defaulted
// would take that distinction away from both.
func ClampResponseSize(size uint64) uint16 {
	return uint16(min(size, uint64(MaxQueryFilterLimit)))
}

// Normalize applies the defaults and bounds a filter is answered under, so a
// filter that did not arrive as query parameters is held to the same rule as one
// that did.
//
// FromParams is the parser for one transport. This is the part that is not about
// transport at all: an absent or zero page size becomes DefaultQueryFilterLimit,
// an over-large one clamps to MaxQueryFilterLimit, and an absent sort direction
// becomes SortAscending. A decoder for protobuf, a JSON body, or a tool call
// reaches the same filter the HTTP path would have produced without restating
// any of those numbers.
//
// A sort direction that is present and unrecognized is reported rather than
// corrected, wrapping errors.ErrUnrecognizedInputValue exactly as FromParams
// does — the filter is still usable and still normalized, because the caller
// that logs and lists anyway should get the ascending page rather than none, but
// the value is not quietly turned into one the caller did not ask for. That is
// the failure this package is most careful about: the list comes back sorted the
// other way, in full, and looks entirely successful.
//
// A nil filter normalizes to nothing, since a nil *QueryFilter already renders
// as the default filter everywhere it is read.
func (qf *QueryFilter) Normalize() error {
	if qf == nil {
		return nil
	}

	if qf.MaxResponseSize == nil || *qf.MaxResponseSize == 0 {
		qf.MaxResponseSize = new(uint16(DefaultQueryFilterLimit))
	} else {
		qf.MaxResponseSize = new(ClampResponseSize(uint64(*qf.MaxResponseSize)))
	}

	if qf.SortBy == nil {
		qf.SortBy = SortAscending

		return nil
	}

	switch strings.ToLower(*qf.SortBy) {
	case sortAscendingString:
		qf.SortBy = SortAscending
	case sortDescendingString:
		qf.SortBy = SortDescending
	default:
		raw := *qf.SortBy
		qf.SortBy = SortAscending

		return platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue,
			"reading %s parameter %q", QueryKeySortBy, raw)
	}

	return nil
}

// SetCursor sets the current page with certain constraints.
func (qf *QueryFilter) SetCursor(cursor *string) {
	if cursor != nil {
		qf.Cursor = cursor
	}
}

// SetMaxResponseSize sets the page size from the wide type a wire format
// actually carries, clamping before the narrowing to uint16 rather than after.
//
// This is the setter a decoder wants, and it takes a uint64 so that there is no
// order left to get wrong: assigning MaxResponseSize directly means narrowing
// first, which is the silent wrap ClampResponseSize describes.
//
// Zero is stored as zero rather than replaced with the default, exactly as
// ClampResponseSize leaves it alone — Normalize is what supplies the default,
// and it reads a zero page size as an absent one.
func (qf *QueryFilter) SetMaxResponseSize(size uint64) {
	qf.MaxResponseSize = new(ClampResponseSize(size))
}

// ToValues returns a url.Values from a QueryFilter.
func (qf *QueryFilter) ToValues() url.Values {
	if qf == nil {
		return DefaultQueryFilter().ToValues()
	}

	v := url.Values{}

	if qf.Cursor != nil {
		v.Set(QueryKeyCursor, *qf.Cursor)
	}

	if qf.MaxResponseSize != nil {
		v.Set(QueryKeyLimit, strconv.FormatUint(uint64(*qf.MaxResponseSize), 10))
	}

	if qf.SortBy != nil {
		v.Set(QueryKeySortBy, *qf.SortBy)
	}

	if qf.CreatedBefore != nil {
		v.Set(QueryKeyCreatedBefore, qf.CreatedBefore.Format(time.RFC3339Nano))
	}

	if qf.CreatedAfter != nil {
		v.Set(QueryKeyCreatedAfter, qf.CreatedAfter.Format(time.RFC3339Nano))
	}

	if qf.UpdatedBefore != nil {
		v.Set(QueryKeyUpdatedBefore, qf.UpdatedBefore.Format(time.RFC3339Nano))
	}

	if qf.UpdatedAfter != nil {
		v.Set(QueryKeyUpdatedAfter, qf.UpdatedAfter.Format(time.RFC3339Nano))
	}

	if qf.IncludeArchived != nil {
		v.Set(QueryKeyIncludeArchived, strconv.FormatBool(*qf.IncludeArchived))
	}

	return v
}

// ToPagination returns a Pagination from a QueryFilter.
//
// The Cursor it carries is the requested one, because a filter on its own does
// not know where the next page starts. NewQueryFilteredResult is what moves it
// to PreviousCursor and fills Cursor from the data, so a Pagination built here
// and returned directly reports the request rather than the result.
//
// It leaves CountsKnown false for the same reason: a request has no counts on
// it, and the zeroes this returns are the absence of an answer rather than one.
func (qf *QueryFilter) ToPagination() Pagination {
	if qf == nil {
		return DefaultQueryFilter().ToPagination()
	}

	x := Pagination{}

	if qf.Cursor != nil {
		x.Cursor = *qf.Cursor
	}

	if qf.MaxResponseSize != nil {
		x.MaxResponseSize = *qf.MaxResponseSize
	}

	return x
}

// ExtractQueryFilterFromRequest extracts a QueryFilter from a request,
// reporting any query parameter that was supplied and could not be read.
//
// The filter is always usable — it starts from DefaultQueryFilter and holds
// whatever parsed — so a handler that wants the old lenient behavior can log
// the error and list anyway. One that would rather not answer a mistyped filter
// with a plausible-looking page has an error wrapping
// errors.ErrUnrecognizedInputValue, which errors/http already renders as a 400.
func ExtractQueryFilterFromRequest(req *http.Request) (*QueryFilter, error) {
	qf := DefaultQueryFilter()
	err := qf.FromParams(req.URL.Query())

	// FromParams has already reported an unrecognized sort direction and left the
	// default in place, so Normalize finds nothing to report here; it is called
	// for the page-size defaulting, and joined rather than discarded so that
	// stays true if either side gains a check the other does not have.
	return qf, platformerrors.Join(err, qf.Normalize())
}

// Counts returns how many rows matched the filter, how many were in scope
// regardless of it, and whether either number was answered at all.
//
// The third value is the reason this method exists. FilteredCount and
// TotalCount are plain integers, so a caller reading them off the struct gets 0
// and 0 whether the collection is empty or the counts were never answered, and
// has no prompt to wonder which. Taking them from here makes that a value the
// caller has to name, and an unanswered pair comes back as zeroes rather than
// as whatever happens to be sitting in the fields.
//
// A nil Pagination has no counts, like every other nil in this package.
func (p *Pagination) Counts() (filtered, total uint64, known bool) {
	if p == nil || !p.CountsKnown {
		return 0, 0, false
	}

	return p.FilteredCount, p.TotalCount, true
}

// NewQueryFilteredResult creates a new QueryFilteredResult from a page and the
// counts describing the collection it was cut from.
//
// Passing the counts in is the caller answering them, so the result reports
// CountsKnown. That is the contract of this constructor rather than an
// inference from the data: a store that ran its own COUNT knows the collection
// holds nothing and says 0 to mean it, and the empty page it returns alongside
// must not read as an unanswered one.
//
// A caller that cannot answer them — a store whose counts ride along on the
// rows, handed a page with no rows — has NewQueryFilteredResultWithoutCounts,
// and should reach for it rather than passing 0 to mean "no idea".
func NewQueryFilteredResult[T any](
	data []*T,
	filteredCount,
	totalCount uint64,
	idExtractor func(*T) string,
	filter *QueryFilter,
) *QueryFilteredResult[T] {
	return newQueryFilteredResult(data, filteredCount, totalCount, true, idExtractor, filter)
}

// NewQueryFilteredResultWithoutCounts creates a QueryFilteredResult for a
// caller with no counts to report, leaving CountsKnown false.
//
// The page, the cursors and the applied filter are assembled exactly as
// NewQueryFilteredResult assembles them; only the two numbers are withheld. It
// is for the store that reads its counts off the rows — the shape
// database/querygen emits, where both counts are scalar subqueries in the
// SELECT list so that the page and the numbers describing it come from one
// statement at one moment. That store has nothing to scan when the page comes
// back empty, and this is how it says so instead of reporting a zero the caller
// would read as "there are none".
func NewQueryFilteredResultWithoutCounts[T any](
	data []*T,
	idExtractor func(*T) string,
	filter *QueryFilter,
) *QueryFilteredResult[T] {
	return newQueryFilteredResult(data, 0, 0, false, idExtractor, filter)
}

// newQueryFilteredResult is the assembly both constructors share, so the cursor
// contract — which cursor is echoed, which is derived, and what an empty page
// does to each — is written once and cannot come to differ between them.
func newQueryFilteredResult[T any](
	data []*T,
	filteredCount,
	totalCount uint64,
	countsKnown bool,
	idExtractor func(*T) string,
	filter *QueryFilter,
) *QueryFilteredResult[T] {
	x := &QueryFilteredResult[T]{
		Data:       data,
		Pagination: filter.ToPagination(),
	}

	x.FilteredCount = filteredCount
	x.TotalCount = totalCount
	x.CountsKnown = countsKnown
	x.AppliedQueryFilter = filter

	// Preserve the input cursor as PreviousCursor before overwriting with next cursor
	if filter != nil && filter.Cursor != nil {
		x.PreviousCursor = *filter.Cursor
	}

	if len(data) > 0 {
		x.Cursor = idExtractor(data[len(data)-1])
	} else {
		x.Cursor = ""
	}

	return x
}

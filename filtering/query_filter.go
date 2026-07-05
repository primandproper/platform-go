package filtering

import (
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v3/observability/keys"
	"github.com/primandproper/platform-go/v3/observability/logging"
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

const (
	// MaxQueryFilterLimit is the maximum value for list queries.
	MaxQueryFilterLimit = 250
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
		_                  struct{}     `json:"-"`
		AppliedQueryFilter *QueryFilter `json:"appliedQueryFilter"`
		Cursor             string       `json:"cursor"`
		PreviousCursor     string       `json:"previousCursor"`
		FilteredCount      uint64       `json:"filteredCount"`
		TotalCount         uint64       `json:"totalCount"`
		MaxResponseSize    uint8        `json:"maxResponseSize"`
	}

	// QueryFilter represents all the filters a User could apply to a list query.
	QueryFilter struct {
		_ struct{} `json:"-"`

		SortBy          *string    `json:"sortBy,omitempty"`
		CreatedAfter    *time.Time `json:"createdAfter,omitempty"`
		CreatedBefore   *time.Time `json:"createdBefore,omitempty"`
		UpdatedAfter    *time.Time `json:"updatedAfter,omitempty"`
		UpdatedBefore   *time.Time `json:"updatedBefore,omitempty"`
		MaxResponseSize *uint8     `json:"maxResponseSize,omitempty"`
		IncludeArchived *bool      `json:"includeArchived,omitempty"`
		Cursor          *string    `json:"cursor,omitempty"`
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
		MaxResponseSize: new(uint8(DefaultQueryFilterLimit)),
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

// FromParams overrides the core QueryFilter values with values retrieved from url.Params.
func (qf *QueryFilter) FromParams(params url.Values) {
	if i := params.Get(QueryKeyCursor); i != "" {
		qf.Cursor = &i
	}

	if i, err := strconv.ParseUint(params.Get(QueryKeyLimit), 10, 64); err == nil {
		qf.MaxResponseSize = new(uint8(math.Min(math.Max(float64(i), 0), MaxQueryFilterLimit)))
	}

	if t, err := time.Parse(time.RFC3339Nano, params.Get(QueryKeyCreatedBefore)); err == nil {
		qf.CreatedBefore = &t
	}

	if t, err := time.Parse(time.RFC3339Nano, params.Get(QueryKeyCreatedAfter)); err == nil {
		qf.CreatedAfter = &t
	}

	if t, err := time.Parse(time.RFC3339Nano, params.Get(QueryKeyUpdatedBefore)); err == nil {
		qf.UpdatedBefore = &t
	}

	if t, err := time.Parse(time.RFC3339Nano, params.Get(QueryKeyUpdatedAfter)); err == nil {
		qf.UpdatedAfter = &t
	}

	if i, err := strconv.ParseBool(params.Get(QueryKeyIncludeArchived)); err == nil {
		qf.IncludeArchived = &i
	}

	switch strings.ToLower(params.Get(QueryKeySortBy)) {
	case sortAscendingString:
		qf.SortBy = SortAscending
	case sortDescendingString:
		qf.SortBy = SortDescending
	}
}

// SetCursor sets the current page with certain constraints.
func (qf *QueryFilter) SetCursor(cursor *string) {
	if cursor != nil {
		qf.Cursor = cursor
	}
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

// ExtractQueryFilterFromRequest can extract a QueryFilter from a request.
func ExtractQueryFilterFromRequest(req *http.Request) *QueryFilter {
	qf := DefaultQueryFilter()
	qf.FromParams(req.URL.Query())

	if qf.MaxResponseSize != nil {
		if *qf.MaxResponseSize == 0 {
			qf.MaxResponseSize = new(uint8(DefaultQueryFilterLimit))
		}
	}

	return qf
}

// NewQueryFilteredResult creates a new QueryFilteredResult.
func NewQueryFilteredResult[T any](
	data []*T,
	filteredCount,
	totalCount uint64,
	idExtractor func(*T) string,
	filter *QueryFilter,
) *QueryFilteredResult[T] {
	x := &QueryFilteredResult[T]{
		Data:       data,
		Pagination: filter.ToPagination(),
	}

	x.FilteredCount = filteredCount
	x.TotalCount = totalCount
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

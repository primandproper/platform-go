package filtering

import (
	"encoding/json"
	"maps"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v12/errors"
	"github.com/primandproper/platform-go/v12/observability/keys"
	"github.com/primandproper/platform-go/v12/observability/logging"
	textsearch "github.com/primandproper/platform-go/v12/search/text"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

func TestDefaultQueryFilter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		qf := DefaultQueryFilter()

		must.NotNil(t, qf)
		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, uint16(DefaultQueryFilterLimit), *qf.MaxResponseSize)
		must.NotNil(t, qf.SortBy)
		test.EqOp(t, SortAscending, qf.SortBy)
	})
}

func TestQueryFilter_AttachToLogger(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{
			Cursor:          new(t.Name()),
			MaxResponseSize: new(MaxQueryFilterLimit),
			CreatedAfter:    new(time.Now().Truncate(time.Second)),
			CreatedBefore:   new(time.Now().Truncate(time.Second)),
			UpdatedAfter:    new(time.Now().Truncate(time.Second)),
			UpdatedBefore:   new(time.Now().Truncate(time.Second)),
			SortBy:          SortDescending,
			IncludeArchived: new(true),
		}

		// Every set field, and only the set fields: what a nil guard is for is
		// deciding which of these reach the line, so asserting that the returned
		// logger is non-nil asserts nothing about any of them.
		test.MapEq(t, map[string]any{
			QueryKeyCursor:        qf.Cursor,
			QueryKeyLimit:         qf.MaxResponseSize,
			QueryKeySortBy:        qf.SortBy,
			QueryKeyCreatedBefore: qf.CreatedBefore,
			QueryKeyCreatedAfter:  qf.CreatedAfter,
			QueryKeyUpdatedBefore: qf.UpdatedBefore,
			QueryKeyUpdatedAfter:  qf.UpdatedAfter,
		}, attachedValues(t, qf))
	})

	T.Run("attaches nothing for a filter that holds nothing", func(t *testing.T) {
		t.Parallel()

		// The other side of every guard: an unset field is absent from the line
		// rather than present and nil, which is what makes a log of a filter
		// readable as the filter that was applied.
		test.MapLen(t, 0, attachedValues(t, &QueryFilter{}))
	})

	T.Run("with nil", func(t *testing.T) {
		t.Parallel()

		// A nil filter says so and stops. Reading the fields of one would panic,
		// so the early return is the whole method for this case.
		test.MapEq(t, map[string]any{keys.FilterIsNilKey: true}, attachedValues(t, nil))
	})

	T.Run("with a nil logger", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, DefaultQueryFilter().AttachToLogger(nil))
	})
}

// attachedValues runs qf.AttachToLogger against a recording logger and returns
// the values it attached.
func attachedValues(t *testing.T, qf *QueryFilter) map[string]any {
	t.Helper()

	attached, ok := qf.AttachToLogger(newRecordingLogger()).(*recordingLogger)
	must.True(t, ok)

	return attached.values
}

// recordingLogger keeps the values it is handed, so a test can assert what
// AttachToLogger put on the line. Deriving a logger returns a new recorder
// carrying the values accumulated so far, which is the shape the real backends
// have: WithValue does not mutate the logger it was called on.
type recordingLogger struct {
	values map[string]any
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{values: map[string]any{}}
}

func (l *recordingLogger) with(values map[string]any) logging.Logger {
	merged := make(map[string]any, len(l.values)+len(values))
	maps.Copy(merged, l.values)
	maps.Copy(merged, values)

	return &recordingLogger{values: merged}
}

func (l *recordingLogger) Info(string)         {}
func (l *recordingLogger) Debug(string)        {}
func (l *recordingLogger) Warn(string)         {}
func (l *recordingLogger) Error(string, error) {}

func (l *recordingLogger) SetRequestIDFunc(logging.RequestIDFunc) {}

func (l *recordingLogger) Clone() logging.Logger                      { return l.with(nil) }
func (l *recordingLogger) WithName(string) logging.Logger             { return l.with(nil) }
func (l *recordingLogger) WithValues(v map[string]any) logging.Logger { return l.with(v) }
func (l *recordingLogger) WithValue(k string, v any) logging.Logger {
	return l.with(map[string]any{k: v})
}
func (l *recordingLogger) WithRequest(*http.Request) logging.Logger   { return l.with(nil) }
func (l *recordingLogger) WithResponse(*http.Response) logging.Logger { return l.with(nil) }
func (l *recordingLogger) WithError(error) logging.Logger             { return l.with(nil) }
func (l *recordingLogger) WithSpan(trace.Span) logging.Logger         { return l.with(nil) }

func TestQueryFilter_FromParams(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		tt, err := time.Parse(time.RFC3339Nano, time.Now().UTC().Truncate(time.Second).Format(time.RFC3339Nano))
		must.NoError(t, err)

		actual := &QueryFilter{}
		expected := &QueryFilter{
			Cursor:          new(t.Name()),
			MaxResponseSize: new(MaxQueryFilterLimit),
			CreatedAfter:    new(tt),
			CreatedBefore:   new(tt),
			UpdatedAfter:    new(tt),
			UpdatedBefore:   new(tt),
			SortBy:          SortDescending,
			IncludeArchived: new(true),
		}

		exampleInput := url.Values{
			textsearch.QueryKeySearch: []string{t.Name()},
			QueryKeyCursor:            []string{*expected.Cursor},
			QueryKeyLimit:             []string{strconv.Itoa(int(*expected.MaxResponseSize))},
			QueryKeyCreatedBefore:     []string{expected.CreatedAfter.Format(time.RFC3339Nano)},
			QueryKeyCreatedAfter:      []string{expected.CreatedBefore.Format(time.RFC3339Nano)},
			QueryKeyUpdatedBefore:     []string{expected.UpdatedAfter.Format(time.RFC3339Nano)},
			QueryKeyUpdatedAfter:      []string{expected.UpdatedBefore.Format(time.RFC3339Nano)},
			QueryKeySortBy:            []string{*expected.SortBy},
			QueryKeyIncludeArchived:   []string{strconv.FormatBool(true)},
		}

		must.NoError(t, actual.FromParams(exampleInput))

		test.Eq(t, expected, actual)

		exampleInput[QueryKeySortBy] = []string{*SortAscending}

		must.NoError(t, actual.FromParams(exampleInput))
		test.EqOp(t, SortAscending, actual.SortBy)
	})
}

func TestQueryFilter_FromParams_parseFailures(T *testing.T) {
	T.Parallel()

	T.Run("an absent parameter is not a failure", func(t *testing.T) {
		t.Parallel()

		qf := DefaultQueryFilter()

		// Every key present and empty, which is what a client sending `?limit=`
		// produces. None of them is a value that failed to parse.
		must.NoError(t, qf.FromParams(url.Values{
			QueryKeyLimit:           []string{""},
			QueryKeyCreatedBefore:   []string{""},
			QueryKeyCreatedAfter:    []string{""},
			QueryKeyUpdatedBefore:   []string{""},
			QueryKeyUpdatedAfter:    []string{""},
			QueryKeyIncludeArchived: []string{""},
			QueryKeySortBy:          []string{""},
		}))

		test.Eq(t, DefaultQueryFilter(), qf)
	})

	unreadable := map[string]url.Values{
		"limit":           {QueryKeyLimit: []string{"fifty"}},
		"negative limit":  {QueryKeyLimit: []string{"-5"}},
		"createdBefore":   {QueryKeyCreatedBefore: []string{"yesterday"}},
		"createdAfter":    {QueryKeyCreatedAfter: []string{"yesterday"}},
		"updatedBefore":   {QueryKeyUpdatedBefore: []string{"yesterday"}},
		"updatedAfter":    {QueryKeyUpdatedAfter: []string{"yesterday"}},
		"includeArchived": {QueryKeyIncludeArchived: []string{"yes please"}},
		"sortBy":          {QueryKeySortBy: []string{"sideways"}},
	}

	for name, params := range unreadable {
		T.Run("reports an unreadable "+name, func(t *testing.T) {
			t.Parallel()

			err := DefaultQueryFilter().FromParams(params)
			must.Error(t, err)
			test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)
		})
	}

	T.Run("reports every unreadable parameter, not just the first", func(t *testing.T) {
		t.Parallel()

		err := DefaultQueryFilter().FromParams(url.Values{
			QueryKeyLimit:        []string{"fifty"},
			QueryKeyCreatedAfter: []string{"yesterday"},
			QueryKeySortBy:       []string{"sideways"},
		})

		must.Error(t, err)
		test.StrContains(t, err.Error(), QueryKeyLimit)
		test.StrContains(t, err.Error(), QueryKeyCreatedAfter)
		test.StrContains(t, err.Error(), QueryKeySortBy)
	})

	T.Run("applies what did parse alongside the failure", func(t *testing.T) {
		t.Parallel()

		qf := DefaultQueryFilter()

		err := qf.FromParams(url.Values{
			QueryKeyCursor: []string{"abc"},
			QueryKeyLimit:  []string{"fifty"},
			QueryKeySortBy: []string{*SortDescending},
		})

		must.Error(t, err)
		must.NotNil(t, qf.Cursor)
		test.EqOp(t, "abc", *qf.Cursor)
		test.EqOp(t, SortDescending, qf.SortBy)
	})

	T.Run("an over-large limit still clamps rather than failing", func(t *testing.T) {
		t.Parallel()

		qf := DefaultQueryFilter()

		must.NoError(t, qf.FromParams(url.Values{
			QueryKeyLimit: []string{strconv.Itoa(int(MaxQueryFilterLimit) * 10)},
		}))

		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, MaxQueryFilterLimit, *qf.MaxResponseSize)
	})

	T.Run("ExtractQueryFilterFromRequest reports and still returns a usable filter", func(t *testing.T) {
		t.Parallel()

		req, reqErr := http.NewRequestWithContext(
			t.Context(), http.MethodGet, "https://verygoodsoftwarenotvirus.ru", http.NoBody)
		must.NoError(t, reqErr)

		req.URL.RawQuery = url.Values{QueryKeyLimit: []string{"fifty"}}.Encode()

		qf, err := ExtractQueryFilterFromRequest(req)
		must.Error(t, err)
		test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)
		test.Eq(t, DefaultQueryFilter(), qf)
	})
}

func TestQueryFilter_SetCursor(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		expected := t.Name()
		qf := &QueryFilter{}
		qf.SetCursor(&expected)

		test.EqOp(t, expected, *qf.Cursor)
	})

	T.Run("with nil", func(t *testing.T) {
		t.Parallel()

		original := t.Name()
		qf := &QueryFilter{Cursor: &original}
		qf.SetCursor(nil)

		test.EqOp(t, original, *qf.Cursor)
	})
}

func TestClampResponseSize(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, uint16(123), ClampResponseSize(123))
	})

	T.Run("over the ceiling", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, MaxQueryFilterLimit, ClampResponseSize(uint64(MaxQueryFilterLimit)+1))
	})

	T.Run("zero is left alone", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, uint16(0), ClampResponseSize(0))
	})

	T.Run("values that would wrap when narrowed first", func(t *testing.T) {
		t.Parallel()

		// The reason the parameter is uint64: narrowing 70000 to uint16 first
		// yields 4464, which clamps to a legible-looking page size nobody asked
		// for. Clamping first cannot produce anything but the ceiling.
		for _, size := range []uint64{
			70000,
			1 << 16,
			(1 << 16) + uint64(MaxQueryFilterLimit),
			1 << 32,
			math.MaxUint32,
			math.MaxUint64,
		} {
			test.EqOp(t, MaxQueryFilterLimit, ClampResponseSize(size))
		}
	})
}

func TestQueryFilter_SetMaxResponseSize(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{}
		qf.SetMaxResponseSize(123)

		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, uint16(123), *qf.MaxResponseSize)
	})

	T.Run("over the ceiling", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{}
		qf.SetMaxResponseSize(uint64(MaxQueryFilterLimit) + 1)

		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, MaxQueryFilterLimit, *qf.MaxResponseSize)
	})

	T.Run("a value that would wrap when narrowed first", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{}
		qf.SetMaxResponseSize(70000)

		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, MaxQueryFilterLimit, *qf.MaxResponseSize)

		// And Normalize leaves the clamped value where it is, rather than the
		// 4464 a narrow-first decoder would have handed it.
		must.NoError(t, qf.Normalize())
		test.EqOp(t, MaxQueryFilterLimit, *qf.MaxResponseSize)
	})

	T.Run("zero is stored rather than defaulted", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{}
		qf.SetMaxResponseSize(0)

		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, uint16(0), *qf.MaxResponseSize)

		// Normalize is what supplies the default, and reads the zero as absent.
		must.NoError(t, qf.Normalize())
		test.EqOp(t, uint16(DefaultQueryFilterLimit), *qf.MaxResponseSize)
	})

	T.Run("overwrites a previously set page size", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{MaxResponseSize: new(uint16(10))}
		qf.SetMaxResponseSize(20)

		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, uint16(20), *qf.MaxResponseSize)
	})

	T.Run("matches what FromParams produces for the same value", func(t *testing.T) {
		t.Parallel()

		parsed := &QueryFilter{}
		must.NoError(t, parsed.FromParams(url.Values{QueryKeyLimit: []string{"70000"}}))

		set := &QueryFilter{}
		set.SetMaxResponseSize(70000)

		must.NotNil(t, parsed.MaxResponseSize)
		must.NotNil(t, set.MaxResponseSize)
		test.EqOp(t, *parsed.MaxResponseSize, *set.MaxResponseSize)
	})
}

func TestQueryFilter_ToValues(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		tt, err := time.Parse(time.RFC3339Nano, time.Now().UTC().Truncate(time.Second).Format(time.RFC3339Nano))
		must.NoError(t, err)

		qf := &QueryFilter{
			Cursor:          new(t.Name()),
			MaxResponseSize: new(MaxQueryFilterLimit),
			CreatedAfter:    new(tt),
			CreatedBefore:   new(tt),
			UpdatedAfter:    new(tt),
			UpdatedBefore:   new(tt),
			SortBy:          SortDescending,
			IncludeArchived: new(true),
		}

		expected := url.Values{
			QueryKeyCursor:          []string{*qf.Cursor},
			QueryKeyLimit:           []string{strconv.Itoa(int(*qf.MaxResponseSize))},
			QueryKeyCreatedBefore:   []string{qf.CreatedAfter.Format(time.RFC3339Nano)},
			QueryKeyCreatedAfter:    []string{qf.CreatedBefore.Format(time.RFC3339Nano)},
			QueryKeyUpdatedBefore:   []string{qf.UpdatedAfter.Format(time.RFC3339Nano)},
			QueryKeyUpdatedAfter:    []string{qf.UpdatedBefore.Format(time.RFC3339Nano)},
			QueryKeyIncludeArchived: []string{strconv.FormatBool(*qf.IncludeArchived)},
			QueryKeySortBy:          []string{*qf.SortBy},
		}

		actual := qf.ToValues()
		test.Eq(t, expected, actual)
	})

	T.Run("with nil", func(t *testing.T) {
		t.Parallel()
		qf := (*QueryFilter)(nil)
		expected := DefaultQueryFilter().ToValues()
		actual := qf.ToValues()
		test.Eq(t, expected, actual)
	})
}

func TestExtractQueryFilter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		tt, err := time.Parse(time.RFC3339Nano, time.Now().UTC().Truncate(time.Second).Format(time.RFC3339Nano))
		must.NoError(t, err)

		expected := &QueryFilter{
			Cursor:          new(t.Name()),
			MaxResponseSize: new(MaxQueryFilterLimit),
			CreatedAfter:    new(tt),
			CreatedBefore:   new(tt),
			UpdatedAfter:    new(tt),
			UpdatedBefore:   new(tt),
			SortBy:          SortDescending,
		}
		exampleInput := url.Values{
			textsearch.QueryKeySearch: []string{t.Name()},
			QueryKeyCursor:            []string{*expected.Cursor},
			QueryKeyLimit:             []string{strconv.Itoa(int(*expected.MaxResponseSize))},
			QueryKeyCreatedBefore:     []string{expected.CreatedAfter.Format(time.RFC3339Nano)},
			QueryKeyCreatedAfter:      []string{expected.CreatedBefore.Format(time.RFC3339Nano)},
			QueryKeyUpdatedBefore:     []string{expected.UpdatedAfter.Format(time.RFC3339Nano)},
			QueryKeyUpdatedAfter:      []string{expected.UpdatedBefore.Format(time.RFC3339Nano)},
			QueryKeySortBy:            []string{*expected.SortBy},
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://verygoodsoftwarenotvirus.ru", http.NoBody)
		test.NoError(t, err)
		must.NotNil(t, req)

		req.URL.RawQuery = exampleInput.Encode()
		actual, err := ExtractQueryFilterFromRequest(req)
		test.NoError(t, err)
		test.Eq(t, expected, actual)
	})

	T.Run("with missing values", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		expected := &QueryFilter{
			Cursor:          new(t.Name()),
			MaxResponseSize: new(uint16(DefaultQueryFilterLimit)),
			SortBy:          SortAscending,
		}
		exampleInput := url.Values{
			QueryKeyCursor: []string{*expected.Cursor},
			QueryKeyLimit:  []string{"0"},
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://verygoodsoftwarenotvirus.ru", http.NoBody)
		test.NoError(t, err)
		must.NotNil(t, req)

		req.URL.RawQuery = exampleInput.Encode()
		actual, err := ExtractQueryFilterFromRequest(req)
		test.NoError(t, err)
		test.Eq(t, expected, actual)
	})
}

func TestQueryFilter_ToPagination(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{
			Cursor:          new(t.Name()),
			MaxResponseSize: new(MaxQueryFilterLimit),
		}

		expected := Pagination{
			Cursor:          *qf.Cursor,
			MaxResponseSize: *qf.MaxResponseSize,
		}

		actual := qf.ToPagination()
		test.Eq(t, expected, actual)
	})

	T.Run("with nil value", func(t *testing.T) {
		t.Parallel()

		qf := (*QueryFilter)(nil)

		actual := qf.ToPagination()
		test.NotNil(t, actual)
	})
}

func TestNewQueryFilteredResult(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{
			Cursor:          new(t.Name()),
			MaxResponseSize: new(MaxQueryFilterLimit),
		}

		data := []*string{new("a"), new("b")}
		filteredCount := uint64(len(data))
		totalCount := uint64(len(data))
		idExtractor := func(s *string) string { return *s }

		expected := &QueryFilteredResult[string]{
			Data: data,
			Pagination: Pagination{
				Cursor:             *data[1],
				PreviousCursor:     *qf.Cursor,
				MaxResponseSize:    *qf.MaxResponseSize,
				FilteredCount:      filteredCount,
				TotalCount:         totalCount,
				CountsKnown:        true,
				AppliedQueryFilter: qf,
			},
		}

		actual := NewQueryFilteredResult(data, filteredCount, totalCount, idExtractor, qf)
		test.Eq(t, expected, actual)
	})

	T.Run("with empty data", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{
			Cursor:          new(t.Name()),
			MaxResponseSize: new(MaxQueryFilterLimit),
		}

		data := []*string{}
		filteredCount := uint64(0)
		totalCount := uint64(0)
		idExtractor := func(s *string) string { return *s }

		expected := &QueryFilteredResult[string]{
			Data: data,
			Pagination: Pagination{
				Cursor:             "",
				PreviousCursor:     *qf.Cursor,
				MaxResponseSize:    *qf.MaxResponseSize,
				FilteredCount:      filteredCount,
				TotalCount:         totalCount,
				CountsKnown:        true,
				AppliedQueryFilter: qf,
			},
		}

		actual := NewQueryFilteredResult(data, filteredCount, totalCount, idExtractor, qf)
		test.Eq(t, expected, actual)
	})

	T.Run("with no cursor", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{
			MaxResponseSize: new(MaxQueryFilterLimit),
		}

		data := []*string{new("a"), new("b")}
		filteredCount := uint64(len(data))
		totalCount := uint64(len(data))
		idExtractor := func(s *string) string { return *s }

		expected := &QueryFilteredResult[string]{
			Data: data,
			Pagination: Pagination{
				Cursor:             *data[1],
				PreviousCursor:     "",
				MaxResponseSize:    *qf.MaxResponseSize,
				FilteredCount:      filteredCount,
				TotalCount:         totalCount,
				CountsKnown:        true,
				AppliedQueryFilter: qf,
			},
		}

		actual := NewQueryFilteredResult(data, filteredCount, totalCount, idExtractor, qf)
		test.Eq(t, expected, actual)
	})
}

func TestQueryFilter_Normalize(T *testing.T) {
	T.Parallel()

	T.Run("a nil filter normalizes to nothing", func(t *testing.T) {
		t.Parallel()

		var qf *QueryFilter

		must.NoError(t, qf.Normalize())
	})

	T.Run("an absent page size gets the default", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{}

		must.NoError(t, qf.Normalize())
		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, uint16(DefaultQueryFilterLimit), *qf.MaxResponseSize)
	})

	T.Run("a zero page size gets the default", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{MaxResponseSize: new(uint16(0))}

		must.NoError(t, qf.Normalize())
		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, uint16(DefaultQueryFilterLimit), *qf.MaxResponseSize)
	})

	T.Run("an over-large page size clamps", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{MaxResponseSize: new(MaxQueryFilterLimit + 1)}

		must.NoError(t, qf.Normalize())
		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, MaxQueryFilterLimit, *qf.MaxResponseSize)
	})

	T.Run("the ceiling itself is not clamped past", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{MaxResponseSize: new(MaxQueryFilterLimit)}

		must.NoError(t, qf.Normalize())
		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, MaxQueryFilterLimit, *qf.MaxResponseSize)
	})

	T.Run("a page size between the default and the ceiling is left alone", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{MaxResponseSize: new(uint16(DefaultQueryFilterLimit + 1))}

		must.NoError(t, qf.Normalize())
		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, uint16(DefaultQueryFilterLimit+1), *qf.MaxResponseSize)
	})

	T.Run("an absent sort direction becomes ascending", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{}

		must.NoError(t, qf.Normalize())
		test.EqOp(t, SortAscending, qf.SortBy)
	})

	T.Run("a recognized sort direction becomes the canonical value", func(t *testing.T) {
		t.Parallel()

		// A wire format that carries a bare string can supply a casing the
		// exported pointers do not use, and a caller comparing pointers should
		// still find the one this package exports.
		qf := &QueryFilter{SortBy: new("DESC")}

		must.NoError(t, qf.Normalize())
		test.EqOp(t, SortDescending, qf.SortBy)
	})

	T.Run("an unrecognized sort direction is reported, not corrected into silence", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{SortBy: new("name")}

		err := qf.Normalize()
		must.Error(t, err)
		test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)
		test.StrContains(t, err.Error(), QueryKeySortBy)

		// Still usable, and still normalized in every other respect: the caller
		// that logs the error and lists anyway gets the ascending page.
		test.EqOp(t, SortAscending, qf.SortBy)
		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, uint16(DefaultQueryFilterLimit), *qf.MaxResponseSize)
	})

	T.Run("a filter that arrived by any route reaches what the HTTP path produces", func(t *testing.T) {
		t.Parallel()

		// The reason this method exists: a decoder that is not FromParams should
		// not have to restate DefaultQueryFilterLimit or MaxQueryFilterLimit to
		// land on the same filter.
		decoded := &QueryFilter{}

		must.NoError(t, decoded.Normalize())
		test.Eq(t, DefaultQueryFilter(), decoded)
	})
}

func TestNewQueryFilteredResult_cursorContract(T *testing.T) {
	T.Parallel()

	T.Run("the first page is an empty PreviousCursor, not a cursor comparison", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{MaxResponseSize: new(uint16(DefaultQueryFilterLimit))}
		data := []*string{new("a"), new("b")}

		actual := NewQueryFilteredResult(data, 2, 2, func(s *string) string { return *s }, qf)

		test.EqOp(t, "", actual.PreviousCursor)
		test.EqOp(t, "b", actual.Cursor)
	})

	T.Run("a later page echoes the cursor that reached it", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{
			Cursor:          new("a"),
			MaxResponseSize: new(uint16(DefaultQueryFilterLimit)),
		}
		data := []*string{new("b"), new("c")}

		actual := NewQueryFilteredResult(data, 2, 2, func(s *string) string { return *s }, qf)

		test.EqOp(t, "a", actual.PreviousCursor)
		test.EqOp(t, "c", actual.Cursor)
	})

	T.Run("a page whose last row is the cursor that reached it is still a later page", func(t *testing.T) {
		t.Parallel()

		// The degenerate case a consumer's heuristic guarded by comparing the
		// applied cursor against the result's: equality does not mean page one,
		// and PreviousCursor reports the truth without the comparison.
		qf := &QueryFilter{
			Cursor:          new("z"),
			MaxResponseSize: new(uint16(DefaultQueryFilterLimit)),
		}
		data := []*string{new("y"), new("z")}

		actual := NewQueryFilteredResult(data, 2, 2, func(s *string) string { return *s }, qf)

		test.EqOp(t, "z", actual.PreviousCursor)
		test.EqOp(t, "z", actual.Cursor)
	})
}

func TestPagination_Counts(T *testing.T) {
	T.Parallel()

	T.Run("an answered pair comes back with the numbers", func(t *testing.T) {
		t.Parallel()

		p := &Pagination{FilteredCount: 5, TotalCount: 9, CountsKnown: true}

		filtered, total, known := p.Counts()

		test.True(t, known)
		test.EqOp(t, uint64(5), filtered)
		test.EqOp(t, uint64(9), total)
	})

	T.Run("an answered pair of zeroes is still an answer", func(t *testing.T) {
		t.Parallel()

		// The store ran its own COUNT and the collection is empty. This is the
		// case CountsKnown exists to keep distinguishable from the next one.
		p := &Pagination{CountsKnown: true}

		filtered, total, known := p.Counts()

		test.True(t, known)
		test.Zero(t, filtered)
		test.Zero(t, total)
	})

	T.Run("an unanswered pair reports nothing and vouches for nothing", func(t *testing.T) {
		t.Parallel()

		// The numbers are deliberately non-zero: whatever is sitting in the
		// fields is not an answer, so it does not come back out.
		p := &Pagination{FilteredCount: 5, TotalCount: 9}

		filtered, total, known := p.Counts()

		test.False(t, known)
		test.Zero(t, filtered)
		test.Zero(t, total)
	})

	T.Run("with nil value", func(t *testing.T) {
		t.Parallel()

		filtered, total, known := (*Pagination)(nil).Counts()

		test.False(t, known)
		test.Zero(t, filtered)
		test.Zero(t, total)
	})
}

func TestQueryFilter_ToPagination_countsAreNotAnswered(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		// A request has no counts on it, so the zeroes a fresh Pagination
		// carries are the absence of an answer rather than one.
		p := (&QueryFilter{Cursor: new(t.Name())}).ToPagination()

		test.False(t, p.CountsKnown)

		_, _, known := p.Counts()
		test.False(t, known)
	})

	T.Run("with nil value", func(t *testing.T) {
		t.Parallel()

		test.False(t, (*QueryFilter)(nil).ToPagination().CountsKnown)
	})
}

func TestNewQueryFilteredResultWithoutCounts(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{
			Cursor:          new("a"),
			MaxResponseSize: new(MaxQueryFilterLimit),
		}

		data := []*string{new("b"), new("c")}

		expected := &QueryFilteredResult[string]{
			Data: data,
			Pagination: Pagination{
				Cursor:             "c",
				PreviousCursor:     "a",
				MaxResponseSize:    *qf.MaxResponseSize,
				AppliedQueryFilter: qf,
			},
		}

		actual := NewQueryFilteredResultWithoutCounts(data, func(s *string) string { return *s }, qf)
		test.Eq(t, expected, actual)
	})

	T.Run("the empty page it exists for", func(t *testing.T) {
		t.Parallel()

		qf := &QueryFilter{
			Cursor:          new("z"),
			MaxResponseSize: new(uint16(DefaultQueryFilterLimit)),
		}

		actual := NewQueryFilteredResultWithoutCounts([]*string{}, func(s *string) string { return *s }, qf)

		test.EqOp(t, "", actual.Cursor)
		test.EqOp(t, "z", actual.PreviousCursor)
		test.False(t, actual.CountsKnown)

		_, _, known := actual.Counts()
		test.False(t, known)
	})

	T.Run("with nil filter", func(t *testing.T) {
		t.Parallel()

		actual := NewQueryFilteredResultWithoutCounts(
			[]*string{new("a")}, func(s *string) string { return *s }, nil,
		)

		test.EqOp(t, "a", actual.Cursor)
		test.EqOp(t, "", actual.PreviousCursor)
		test.False(t, actual.CountsKnown)
	})
}

func TestQueryFilteredResult_countsContract(T *testing.T) {
	T.Parallel()

	T.Run("supplied counts are an answer, empty page or not", func(t *testing.T) {
		t.Parallel()

		// The store that runs its own COUNT is entitled to say zero and mean it,
		// which is why the constructor's contract is "the caller answered them"
		// rather than "there were rows to read them off".
		actual := NewQueryFilteredResult(
			[]*string{}, 0, 0, func(s *string) string { return *s }, DefaultQueryFilter(),
		)

		filtered, total, known := actual.Counts()

		test.True(t, known)
		test.Zero(t, filtered)
		test.Zero(t, total)
	})

	T.Run("the final page of a walk is not a count of zero", func(t *testing.T) {
		t.Parallel()

		// The failure the flag exists for, walked end to end: a store whose
		// counts ride on the rows reports 5, 5, and then nothing at all. Read
		// off the fields, the third page is indistinguishable from an empty
		// collection; read through Counts, it declines to be one.
		extract := func(s *string) string { return *s }

		var pages []*QueryFilteredResult[string]

		for _, data := range [][]*string{{new("a"), new("b")}, {new("c"), new("d")}} {
			pages = append(pages, NewQueryFilteredResult(data, 5, 5, extract, DefaultQueryFilter()))
		}

		pages = append(pages, NewQueryFilteredResultWithoutCounts(nil, extract, DefaultQueryFilter()))

		var reported []uint64

		for _, page := range pages {
			filtered, _, known := page.Counts()
			if !known {
				continue
			}

			reported = append(reported, filtered)
		}

		test.Eq(t, []uint64{5, 5}, reported)
		test.EqOp(t, uint64(0), pages[2].FilteredCount)
	})
}

func TestPagination_countsKnownOnTheWire(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		// The flag is only useful to a client that receives it, so the key it
		// travels under is part of the contract rather than an implementation
		// detail of the struct.
		answered, err := json.Marshal(NewQueryFilteredResult(
			[]*string{}, 0, 0, func(s *string) string { return *s }, DefaultQueryFilter(),
		))
		must.NoError(t, err)
		test.StrContains(t, string(answered), `"countsKnown":true`)

		unanswered, err := json.Marshal(NewQueryFilteredResultWithoutCounts(
			[]*string{}, func(s *string) string { return *s }, DefaultQueryFilter(),
		))
		must.NoError(t, err)
		test.StrContains(t, string(unanswered), `"countsKnown":false`)
	})
}

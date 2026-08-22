package filtering

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// Every list endpoint in every service parses a QueryFilter off the request
// before it does anything else, so FromParams is on the hot path of the most
// common request shape there is.
//
// The rows are chosen to separate the cost of the params that were sent from
// the cost of the ones that were not. FromParams used to attempt every field
// unconditionally — four RFC3339Nano parses, two strconv parses, and a
// ToLower — so a request carrying no filters at all paid for all of them. It
// now skips what was not sent, and the "empty" row is what that is worth: it is
// the shape most requests are.

// benchParams is a fully populated query string: every field present and
// parseable, which is the most work FromParams can be asked to do.
func benchParams() url.Values {
	v := url.Values{}
	v.Set(QueryKeyCursor, "cursor_01HZY0000000000000")
	v.Set(QueryKeyLimit, "100")
	v.Set(QueryKeyCreatedBefore, "2026-08-03T12:30:45.123456789Z")
	v.Set(QueryKeyCreatedAfter, "2026-07-03T12:30:45.123456789Z")
	v.Set(QueryKeyUpdatedBefore, "2026-08-03T12:30:45.123456789Z")
	v.Set(QueryKeyUpdatedAfter, "2026-07-03T12:30:45.123456789Z")
	v.Set(QueryKeyIncludeArchived, "true")
	v.Set(QueryKeySortBy, "desc")

	return v
}

func BenchmarkQueryFilter_FromParams(b *testing.B) {
	full := benchParams()
	empty := url.Values{}

	// The shape a paginating client actually sends: a cursor and a limit, and
	// none of the four timestamps.
	typical := url.Values{}
	typical.Set(QueryKeyCursor, "cursor_01HZY0000000000000")
	typical.Set(QueryKeyLimit, "50")

	cases := []struct {
		params url.Values
		name   string
	}{
		{name: "empty", params: empty},
		{name: "typical", params: typical},
		{name: "full", params: full},
	}

	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				qf := &QueryFilter{}
				errSink = qf.FromParams(c.params)
				filterSink = qf
			}
		})
	}
}

// BenchmarkQueryFilter_ExtractFromRequest is the whole per-request path,
// including the URL query parse FromParams is handed the result of.
func BenchmarkQueryFilter_ExtractFromRequest(b *testing.B) {
	ctx := b.Context()

	build := func(b *testing.B, rawQuery string) *http.Request {
		b.Helper()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/v1/things?"+rawQuery, http.NoBody)
		if err != nil {
			b.Fatal(err)
		}

		return req
	}

	b.Run("noQuery", func(b *testing.B) {
		req := build(b, "")
		for b.Loop() {
			filterSink, errSink = ExtractQueryFilterFromRequest(req)
		}
	})

	b.Run("typical", func(b *testing.B) {
		req := build(b, "cursor=cursor_01HZY0000000000000&limit=50")
		for b.Loop() {
			filterSink, errSink = ExtractQueryFilterFromRequest(req)
		}
	})

	b.Run("full", func(b *testing.B) {
		req := build(b, benchParams().Encode())
		for b.Loop() {
			filterSink, errSink = ExtractQueryFilterFromRequest(req)
		}
	})
}

// BenchmarkQueryFilter_ToValues is the reverse trip, paid by anything that
// builds a next-page link.
func BenchmarkQueryFilter_ToValues(b *testing.B) {
	full := &QueryFilter{}
	full.FromParams(benchParams())

	b.Run("default", func(b *testing.B) {
		qf := DefaultQueryFilter()
		for b.Loop() {
			valuesSink = qf.ToValues()
		}
	})

	b.Run("full", func(b *testing.B) {
		for b.Loop() {
			valuesSink = full.ToValues()
		}
	})

	// A nil receiver builds a default filter and encodes that, so it is the
	// costlier path despite looking like the cheapest call.
	b.Run("nilReceiver", func(b *testing.B) {
		var qf *QueryFilter
		for b.Loop() {
			valuesSink = qf.ToValues()
		}
	})
}

// BenchmarkQueryFilter_ToPagination prices the envelope every list response is
// wrapped in.
func BenchmarkQueryFilter_ToPagination(b *testing.B) {
	qf := DefaultQueryFilter()

	for b.Loop() {
		paginationSink = qf.ToPagination()
	}
}

// BenchmarkQueryFilterSchema prices the reflection QueryFilterSchema does per
// call, plus the decode that gives its caller a map of its own.
//
// Neither is cached, and the reflection stopped being cached when the page-size
// ceiling became a var: a cached document freezes whichever ceiling was in
// place when it was first asked for. Nothing calls this per request — a tool
// definition or an OpenAPI document is built once, at startup — so this is here
// to confirm that always-current is cheap enough never to need revisiting, not
// because anything is tuning it.
func BenchmarkQueryFilterSchema(b *testing.B) {
	for b.Loop() {
		schemaSink = QueryFilterSchema()
	}
}

var (
	filterSink     *QueryFilter
	valuesSink     url.Values
	paginationSink Pagination
	errSink        error
	schemaSink     map[string]any
	_              = time.RFC3339Nano
)

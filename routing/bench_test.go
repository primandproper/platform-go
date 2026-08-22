package routing_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/encoding"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/routing/backends/chi"

	"github.com/shoenig/test/must"
)

// A typed route does four things per request that a plain http.HandlerFunc does
// not: match the path, bind the path and query parameters onto a struct, decode
// the body into the same struct, and encode the result into the response
// envelope. These benchmarks separate those so the convenience can be priced
// against what it replaces.
//
// The untyped rows are the control. They run through the same router and the
// same backend, so the delta against them is binding and encoding alone rather
// than routing plus binding plus encoding.

type benchInput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	OrgID uint64 `path:"orgID"`
	Page  uint64 `query:"page"`
}

type benchOutput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	ID    uint64 `json:"id"`
}

func benchRouter(b *testing.B) *routing.Router {
	b.Helper()

	backend := chi.NewBackend(&chi.Config{ServiceName: "bench"})
	enc := encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON)

	return routing.New(backend, enc)
}

// serve drives one request through the router, which is what every row below
// measures. The recorder and request are rebuilt per iteration because a
// handler may consume the body and a recorder accumulates.
func serve(b *testing.B, r *routing.Router, method, target, body string) {
	b.Helper()

	h := r.Handler()

	for b.Loop() {
		req := httptest.NewRequestWithContext(b.Context(), method, target, strings.NewReader(body))
		req.Header.Set(encoding.ContentTypeHeaderKey, "application/json")

		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// BenchmarkRouter_Typed prices the typed handler path at the shapes a real API
// uses: a GET binding one path parameter, a GET binding a path and a query
// parameter, and a POST that also decodes a body.
func BenchmarkRouter_Typed(b *testing.B) {
	b.Run("GET/pathParam", func(b *testing.B) {
		r := benchRouter(b)
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, in benchInput) (benchOutput, error) {
			return benchOutput{ID: in.OrgID}, nil
		})
		must.NoError(b, r.Err())

		serve(b, r, http.MethodGet, "/orgs/99", "")
	})

	b.Run("GET/pathAndQuery", func(b *testing.B) {
		r := benchRouter(b)
		routing.Get(r, "/orgs/{orgID:uint64}/users", func(_ context.Context, in benchInput) (benchOutput, error) {
			return benchOutput{ID: in.OrgID + in.Page}, nil
		})
		must.NoError(b, r.Err())

		serve(b, r, http.MethodGet, "/orgs/99/users?page=3", "")
	})

	b.Run("POST/withBody", func(b *testing.B) {
		r := benchRouter(b)
		routing.Post(r, "/orgs/{orgID:uint64}/users", func(_ context.Context, in benchInput) (benchOutput, error) {
			return benchOutput{ID: in.OrgID, Name: in.Name, Email: in.Email}, nil
		})
		must.NoError(b, r.Err())

		serve(b, r, http.MethodPost, "/orgs/7/users", `{"name":"Ada","email":"ada@example.com"}`)
	})

	// A path parameter that fails its type constraint never reaches the
	// handler, and the refusal is encoded through the same envelope. This is
	// the cost of a malformed request, which is chosen by the client.
	b.Run("GET/badPathParam", func(b *testing.B) {
		r := benchRouter(b)
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, in benchInput) (benchOutput, error) {
			return benchOutput{ID: in.OrgID}, nil
		})

		serve(b, r, http.MethodGet, "/orgs/not-a-number", "")
	})
}

// BenchmarkRouter_Untyped is the control: the same router and backend, with a
// handler that does its own reading and writing. The difference against the
// typed rows is what binding and enveloping cost.
func BenchmarkRouter_Untyped(b *testing.B) {
	b.Run("GET", func(b *testing.B) {
		r := benchRouter(b)
		r.Handle(http.MethodGet, "/orgs/{orgID}", http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			res.WriteHeader(http.StatusOK)
			_, _ = res.Write([]byte(`{"id":99}`))
		}))
		must.NoError(b, r.Err())

		serve(b, r, http.MethodGet, "/orgs/99", "")
	})
}

// BenchmarkRouter_NotFound prices the miss. It is worth its own row because an
// unmatched path is the most common thing a public endpoint sees that it did
// not ask for, and because it exercises the backend's matcher without any of
// this package's handling after it.
func BenchmarkRouter_NotFound(b *testing.B) {
	r := benchRouter(b)
	routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, in benchInput) (benchOutput, error) {
		return benchOutput{ID: in.OrgID}, nil
	})
	must.NoError(b, r.Err())

	serve(b, r, http.MethodGet, "/nothing/here", "")
}

// benchPath builds a distinct literal route prefix for index i, so a table of
// them cannot be collapsed by the matcher into a shared node.
func benchPath(i int) string {
	return "/r" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))
}

// BenchmarkRouter_RouteScale prices matching against route tables of different
// sizes, so the rows above can be read knowing whether the matcher or the
// binding is the term that grows with a service's surface area.
func BenchmarkRouter_RouteScale(b *testing.B) {
	for _, routes := range []int{1, 10, 100} {
		b.Run("routes="+strconv.Itoa(routes), func(b *testing.B) {
			r := benchRouter(b)

			for i := range routes {
				routing.Get(r, benchPath(i)+"/{orgID:uint64}", func(_ context.Context, in benchInput) (benchOutput, error) {
					return benchOutput{ID: in.OrgID}, nil
				})
			}

			must.NoError(b, r.Err())

			// Match the last route registered, which is the deepest a linear
			// table would have to look.
			serve(b, r, http.MethodGet, benchPath(routes-1)+"/99", "")
		})
	}
}

// BenchmarkRouter_Harness is the floor under every row above: building the
// request and the recorder, with no router involved at all. Subtract it before
// reading any other row in this file as a cost of routing.
//
// Most of its bytes are the recorder's response buffer, but it is a small
// share of the allocation counts above — which is the useful part. It means the
// counts in this file really are the router's work rather than the harness's,
// and in particular that the NotFound row is not cheap: refusing an unmatched
// path costs most of what serving a matched one does, because the refusal is
// still encoded through the error envelope. A service taking scanner traffic
// pays nearly full price for it.
func BenchmarkRouter_Harness(b *testing.B) {
	for b.Loop() {
		req := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "/orgs/99", strings.NewReader(""))
		req.Header.Set(encoding.ContentTypeHeaderKey, "application/json")

		recorderSink = httptest.NewRecorder()
		requestSink = req
	}
}

var (
	recorderSink *httptest.ResponseRecorder
	requestSink  *http.Request
)

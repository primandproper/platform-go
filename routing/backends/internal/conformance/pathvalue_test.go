package conformance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v13/encoding"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/routing/backends/chi"
	"github.com/primandproper/platform-go/v13/routing/backends/gin"
	"github.com/primandproper/platform-go/v13/routing/backends/httprouter"
	"github.com/primandproper/platform-go/v13/routing/backends/stdlib"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// implementation is one routing.Backend, named so a failure says which router
// disagreed.
type implementation struct {
	newBackend func(serviceName string) routing.Backend
	name       string
}

// implementations is every backend this repository ships. A new one belongs
// here on the day it is written, not the day it is found to differ.
var implementations = []implementation{
	{
		name: "stdlib",
		newBackend: func(serviceName string) routing.Backend {
			return stdlib.NewBackend(&stdlib.Config{ServiceName: serviceName})
		},
	},
	{
		name:       "chi",
		newBackend: func(serviceName string) routing.Backend { return chi.NewBackend(&chi.Config{ServiceName: serviceName}) },
	},
	{
		name:       "gin",
		newBackend: func(serviceName string) routing.Backend { return gin.NewBackend(&gin.Config{ServiceName: serviceName}) },
	},
	{
		name: "httprouter",
		newBackend: func(serviceName string) routing.Backend {
			return httprouter.NewBackend(&httprouter.Config{ServiceName: serviceName})
		},
	},
}

// pathValueCases are request paths against "/things/{slug}" and the slug the
// handler must be handed. Every one of them is a value a client can only put in
// a path by percent-escaping it, plus a control with nothing to escape.
var pathValueCases = []struct {
	name     string
	target   string
	expected string
}{
	{name: "nothing to escape", target: "/things/plain", expected: "plain"},
	// The case from the report: an escaped separator and an escaped space in one
	// value. "a/b c" is one segment because it was escaped, and must come back
	// as the caller wrote it.
	{name: "escaped separator and space", target: "/things/a%2Fb%20c", expected: "a/b c"},
	{name: "escaped separator alone", target: "/things/a%2Fb", expected: "a/b"},
	// url.QueryUnescape reads this as "a b". A path is not a query.
	{name: "a literal plus is not a space", target: "/things/a+b", expected: "a+b"},
	{name: "escaped escape", target: "/things/a%25b", expected: "a%b"},
	// A value that is itself percent-escaped text. Exactly one decode is owed:
	// net/url leaves URL.RawPath empty here, because escaping "a%2Fb" reproduces
	// what arrived, so a backend reading the decoded path already holds the
	// answer and must not decode a second time.
	{name: "a value that looks escaped", target: "/things/a%252Fb", expected: "a%2Fb"},
	{name: "an email address", target: "/things/user%40example.com", expected: "user@example.com"},
	{name: "a URL as a key", target: "/things/https%3A%2F%2Fexample.com%2Fx", expected: "https://example.com/x"},
}

// TestBackend_PathValueRoundTrip is the backend seam: what Handle matched, and
// what PathValue then reported.
func TestBackend_PathValueRoundTrip(T *testing.T) {
	T.Parallel()

	for _, impl := range implementations {
		T.Run(impl.name, func(t *testing.T) {
			t.Parallel()

			for _, tc := range pathValueCases {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()

					b := impl.newBackend(t.Name())

					var got string
					var served bool

					b.Handle(http.MethodGet, "/things/{slug}", http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
						got, served = b.PathValue(req, "slug"), true
						res.WriteHeader(http.StatusNoContent)
					}))

					rec := httptest.NewRecorder()
					b.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.target, http.NoBody))

					// Asserted before the value, because the two failure modes
					// are different: a backend that routes on the decoded path
					// never reaches the handler at all.
					must.True(t, served, must.Sprintf("%s did not route %s to the handler (status %d)", impl.name, tc.target, rec.Code))
					test.EqOp(t, http.StatusNoContent, rec.Code)
					test.EqOp(t, tc.expected, got)
				})
			}
		})
	}
}

// TestBackend_EscapingDoesNotChangeTheAnswer holds each backend to its own
// behavior rather than to a shared one, which is the only way to state this
// without freezing policy the backends have always disagreed about: whether an
// unmatched verb is a 405 or a 404 differs between them for reasons that have
// nothing to do with escaping.
//
// What must not differ is the effect of escaping. A request for "/things/a%2Fb"
// asks the same question of the router as "/things/plain" does — one segment,
// one route — so it has to come back with the same answer. It did not: matching
// on the decoded path turned the escaped request into a different, unregistered
// shape, and "wrong verb" quietly became "no such thing".
func TestBackend_EscapingDoesNotChangeTheAnswer(T *testing.T) {
	T.Parallel()

	for _, impl := range implementations {
		T.Run(impl.name, func(t *testing.T) {
			t.Parallel()

			for _, method := range []string{http.MethodPost, http.MethodOptions} {
				t.Run(method, func(t *testing.T) {
					t.Parallel()

					b := impl.newBackend(t.Name())
					b.Handle(http.MethodGet, "/things/{slug}", http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
						res.WriteHeader(http.StatusNoContent)
					}))

					answer := func(target string) (int, string) {
						rec := httptest.NewRecorder()
						b.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody))

						return rec.Code, rec.Header().Get("Allow")
					}

					plainCode, plainAllow := answer("/things/plain")
					escapedCode, escapedAllow := answer("/things/a%2Fb")

					test.EqOp(t, plainCode, escapedCode)
					test.EqOp(t, plainAllow, escapedAllow)
				})
			}
		})
	}
}

// slugInput is a typed input whose only field is the path parameter.
type slugInput struct {
	Slug string `path:"slug"`
}

// TestRouter_PathValueRoundTrip drives the same requests through a real
// routing.Router, so the assertion covers the binder reading PathValue and not
// just the backend seam. This is the shape a service actually registers.
func TestRouter_PathValueRoundTrip(T *testing.T) {
	T.Parallel()

	for _, impl := range implementations {
		T.Run(impl.name, func(t *testing.T) {
			t.Parallel()

			for _, tc := range pathValueCases {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()

					r := routing.New(impl.newBackend(t.Name()), encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

					var seen slugInput
					var served bool

					routing.Get(r, "/things/{slug}", func(_ context.Context, in slugInput) (routing.Empty, error) {
						seen, served = in, true

						return routing.Empty{}, nil
					}, routing.WithEnvelope(false))

					rec := httptest.NewRecorder()
					r.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.target, http.NoBody))

					must.True(t, served, must.Sprintf("%s did not route %s to the handler (status %d)", impl.name, tc.target, rec.Code))
					test.EqOp(t, http.StatusOK, rec.Code)
					test.EqOp(t, tc.expected, seen.Slug)
				})
			}
		})
	}
}

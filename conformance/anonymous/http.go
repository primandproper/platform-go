package anonymous

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// httpRoute is one route an HTTP surface mounts, as routing.Route records it.
type httpRoute struct {
	method string
	path   string
}

// httpSurface is one surface this module serves over HTTP, how to tell it was
// mounted, and every route it mounts.
//
// Unlike the gRPC roster it lists the routes rather than reading them, because
// there is no registry to read an HTTP surface out of: routing.Route is what
// registration returns, not something a package can declare without a router.
// TestHTTPRosterMatchesWhatEachSurfaceMounts is what stops the list drifting —
// it mounts each surface's real handlers and compares, in both directions.
//
// None of the three has a route reachable without a caller. dataprivacy's
// confirm is a link in a mail, and it is still a browser arriving with its
// session: the handler resolves the subject before it reads the request.
type httpSurface struct {
	mounted func(*conformance.HTTPSurfaces) bool
	name    string
	routes  []httpRoute
}

func httpRoster() []httpSurface {
	privacy := dataprivacyhttp.BasePath
	ops := operationshttp.BasePath

	return []httpSurface{
		{
			name:    "dataprivacy",
			mounted: func(h *conformance.HTTPSurfaces) bool { return h.DataPrivacy },
			routes: []httpRoute{
				{http.MethodPost, privacy},
				{http.MethodGet, privacy},
				{http.MethodGet, privacy + "/{requestID}"},
				{http.MethodGet, privacy + "/{requestID}/confirm"},
				{http.MethodPost, privacy + "/{requestID}/cancel"},
			},
		},
		{
			name:    "mediaregistry",
			mounted: func(h *conformance.HTTPSurfaces) bool { return h.MediaRegistry },
			routes: []httpRoute{
				{http.MethodGet, mediaregistryhttp.BasePath + "/{objectID}"},
			},
		},
		{
			name:    "operations",
			mounted: func(h *conformance.HTTPSurfaces) bool { return h.Operations },
			routes: []httpRoute{
				{http.MethodGet, ops},
				{http.MethodGet, ops + "/{operationID}"},
				{http.MethodPost, ops + "/{operationID}/cancel"},
				{http.MethodGet, ops + "/{operationID}/events"},
			},
		},
	}
}

// subtestName is a route as a subtest name: its method and its path with the
// slashes turned to spaces, "GET operations {operationID} events".
//
// Not the path as written, because a slash in a subtest name is nesting to go
// test: "GET /operations" would read as the parent of "GET
// /operations/{operationID}" rather than its sibling, in -run patterns and in
// every tool that reads test2json — the conformance job's per-suite tally
// counted five of these routes as parents rather than assertions until they
// stopped being named that way.
func subtestName(route httpRoute) string {
	return route.method + " " + strings.ReplaceAll(strings.TrimPrefix(route.path, "/"), "/", " ")
}

// pathParam matches a route's placeholders, which the suite fills with an
// identifier nothing holds.
var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// absentID is what a placeholder becomes. It names nothing, which is fine: a
// request with nobody on it must be refused before anything is looked up, so
// a 404 here would be a surface reading a row for a caller it never resolved.
const absentID = "conformance-absent"

// runHTTP is the HTTP half: every route on every mounted HTTP surface refuses a
// request with nobody on it, as 401.
//
// One direction only, because none of the three has an anonymous route. The
// request carries a well-formed empty body where the method takes one, so a
// refusal cannot be about a body the router failed to decode — what is asserted
// is that the surface refused for want of a caller, and refused as that.
func runHTTP(t *testing.T, s *conformance.Session, probe *conformance.Subject) {
	t.Helper()

	anonymousHTTP := s.Seams().AnonymousHTTP

	switch {
	case probe.HTTP == nil:
		t.Skip("conformance: this subject serves no HTTP surfaces")
	case anonymousHTTP == nil:
		t.Skip("conformance: this subject supplies no callerless HTTP client, and one cannot be synthesized from an authenticated one")
	}

	client, err := anonymousHTTP(t.Context())
	must.NoError(t, err, must.Sprint("opening an HTTP client carrying no caller"))
	must.NotNil(t, client, must.Sprint("the subject returned no HTTP client and no error"))

	base := strings.TrimSuffix(probe.HTTP.BaseURL, "/")

	surfaces := httpRoster()

	for i := range surfaces {
		surf := &surfaces[i]

		if !surf.mounted(probe.HTTP) {
			continue
		}

		t.Run(surf.name, func(t *testing.T) {
			t.Parallel()

			for j := range surf.routes {
				route := surf.routes[j]

				t.Run(subtestName(route), func(t *testing.T) {
					t.Parallel()

					url := base + pathParam.ReplaceAllString(route.path, absentID)

					var body io.Reader
					if route.method == http.MethodPost {
						body = strings.NewReader("{}")
					}

					req, reqErr := http.NewRequestWithContext(t.Context(), route.method, url, body)
					must.NoError(t, reqErr)

					if body != nil {
						req.Header.Set("Content-Type", "application/json")
					}

					res, doErr := client.Do(req)
					must.NoError(t, doErr, must.Sprintf("%s %s", route.method, url))

					test.NoError(t, res.Body.Close())

					test.EqOp(t, http.StatusUnauthorized, res.StatusCode,
						test.Sprintf("%s %s answered a request carrying no caller with %d rather than refusing it as unauthenticated",
							route.method, route.path, res.StatusCode))
				})
			}
		})
	}
}

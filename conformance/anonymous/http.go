package anonymous

import (
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"

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
// The authorization server is the one surface with routes reachable without a
// caller, and it names them per route in public. dataprivacy's confirm is not
// among them: it is a link in a mail, and it is still a browser arriving with
// its session, since the handler resolves the subject before it reads the
// request.
type httpSurface struct {
	mounted func(*conformance.HTTPSurfaces) bool

	// routeMounted, where set, is whether one of routes is mounted on a
	// surface that is: operations' event stream is registered only where the
	// deployment runs a watcher, and the deployment says so.
	routeMounted func(*conformance.HTTPSurfaces, httpRoute) bool

	// why says what the public routes are for, on the surface that has some.
	why string

	name   string
	routes []httpRoute

	// public are the routes among routes that a request with nobody on it
	// must reach rather than be refused at.
	public []httpRoute
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
				{http.MethodGet, privacy + "/{requestID}/artifact"},
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
			// Listed by hand like the rest, from the constants oauth2server
			// fixes them at: Mount registers them and returns nothing to read
			// them back from. /register is not here, because a deployment
			// whose clients come from the registry builds the server without
			// it; see the roster test.
			//
			// /token and /revoke are not public, and that is not a statement
			// that a person must be signed in to reach them: they are reached
			// by a client authenticating as itself, and a request carrying no
			// credential at all is refused there as 401 like anywhere else.
			name:    "oauth2server",
			mounted: func(h *conformance.HTTPSurfaces) bool { return h.OAuth2Server },
			why:     "a client discovers the server and sends a person to sign in before anybody is signed in",
			routes: []httpRoute{
				{http.MethodGet, oauth2server.PathAuthorizationServerMetadata},
				{http.MethodGet, oauth2server.PathAuthorize},
				{http.MethodPost, oauth2server.PathAuthorize},
				{http.MethodPost, oauth2server.PathToken},
				{http.MethodPost, oauth2server.PathRevoke},
			},
			public: []httpRoute{
				{http.MethodGet, oauth2server.PathAuthorizationServerMetadata},
				{http.MethodGet, oauth2server.PathAuthorize},
				{http.MethodPost, oauth2server.PathAuthorize},
			},
		},
		{
			name:    "operations",
			mounted: func(h *conformance.HTTPSurfaces) bool { return h.Operations },
			routeMounted: func(h *conformance.HTTPSurfaces, route httpRoute) bool {
				return route.path != ops+"/{operationID}"+operationshttp.EventsSuffix || h.OperationEvents
			},
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
// request with nobody on it, as 401, except the routes a surface names public,
// which must not be refused that way.
//
// The request carries a well-formed empty body where the method takes one, so a
// refusal cannot be about a body the router failed to decode — what is asserted
// is that the surface refused for want of a caller, and refused as that. A
// public route is asserted only to be reachable, never to succeed, for the
// reason an anonymous RPC is: an empty request will usually fail on its input.
func runHTTP(t *testing.T, s *conformance.Session, probe *conformance.Subject) {
	t.Helper()

	anonymousHTTP := s.Seams().AnonymousHTTP

	switch {
	case probe.HTTP == nil:
		conformance.Skip(t, "conformance: this subject serves no HTTP surfaces")
	case anonymousHTTP == nil:
		conformance.Skip(t, "conformance: this subject supplies no callerless HTTP client, and one cannot be synthesized from an authenticated one")
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

				if surf.routeMounted != nil && !surf.routeMounted(probe.HTTP, route) {
					continue
				}

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

					if slices.Contains(surf.public, route) {
						test.NotEqOp(t, http.StatusUnauthorized, res.StatusCode,
							test.Sprintf("%s %s is reachable without a caller (%s) and was refused for want of one",
								route.method, route.path, surf.why))

						return
					}

					test.EqOp(t, http.StatusUnauthorized, res.StatusCode,
						test.Sprintf("%s %s answered a request carrying no caller with %d rather than refusing it as unauthenticated",
							route.method, route.path, res.StatusCode))
				})
			}
		})
	}
}

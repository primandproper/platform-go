package conformance

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	"github.com/primandproper/primitives-go/v2/authorization"
)

// httpRoster is every route this module's HTTP surfaces mount, as the key
// Seams.OperatorRoutes and Making name it by, and which of them each surface
// exports as reached on the caller's own standing.
//
// It is read from the surfaces rather than listed here: each package's
// Permissions and OwnStandingRoutes between them name every route it mounts,
// which each package's own tests check against the routes it really mounts.
func httpRoster() (routes, ownStanding []string) {
	surfaces := []struct {
		guarded map[string][]authorization.Permission
		own     []string
	}{
		{guarded: dataprivacyhttp.Permissions(), own: dataprivacyhttp.OwnStandingRoutes()},
		{guarded: mediaregistryhttp.Permissions(), own: mediaregistryhttp.OwnStandingRoutes()},
		{guarded: operationshttp.Permissions(), own: operationshttp.OwnStandingRoutes()},
	}

	for i := range surfaces {
		surface := &surfaces[i]

		routes = append(routes, slices.Collect(maps.Keys(surface.guarded))...)
		routes = append(routes, surface.own...)
		ownStanding = append(ownStanding, surface.own...)
	}

	return routes, ownStanding
}

// checkOperatorRoutes fails a run whose Seams.OperatorRoutes names something
// that is not a route this module's HTTP surfaces mount, or names one a surface
// promises to serve on the caller's own standing.
//
// The first is a spelling check, for the reason checkOperatorMethods is one: an
// entry that names no route reserves nothing, and the suites would make the
// call it meant as a member while the deployment refuses one. The second is a
// contradiction in the seams rather than a spelling. A route reached on the
// caller's own standing — a privacy subject following their own erasure, the
// confirmation link in their mail — asks for no grant, so a deployment cannot
// keep it from its members by withholding one, and a list claiming it does is
// a list no assertion could be made against.
func checkOperatorRoutes(t *testing.T, reserved []string) {
	t.Helper()

	for _, route := range reserved {
		if problem := routeProblem(route); problem != "" {
			t.Fatal("conformance: " + problem)
		}
	}
}

// routeProblem is why Run refuses a reservation of route, or empty where it
// does not.
func routeProblem(route string) string {
	routes, own := httpRoster()

	switch {
	case slices.Contains(own, route):
		return fmt.Sprintf("Seams.OperatorRoutes names %q, which its surface serves on the caller's own standing "+
			"rather than on a grant; a deployment cannot reserve it to an operator, so remove it", route)
	case !slices.Contains(routes, route):
		return fmt.Sprintf("Seams.OperatorRoutes names %q, which is not a route this module's HTTP surfaces mount; "+
			"name one as METHOD /path, the path at its surface's default base path, e.g. %q", route, operationshttp.RouteCancel)
	default:
		return ""
	}
}

// isRouteKey reports whether a declared call reads as a route rather than as a
// full method name: a method, a space, and a path.
func isRouteKey(call string) bool {
	method, path, ok := strings.Cut(call, " ")

	return ok && method != "" && strings.HasPrefix(path, "/")
}

// routeMatches reports whether a request's method and path, relative to the
// router's base, are the route key's.
//
// A braced segment of the key matches any one non-empty segment of the path,
// which is all routing's patterns here use.
func routeMatches(route, method, path string) bool {
	routeMethod, pattern, ok := strings.Cut(route, " ")
	if !ok || routeMethod != method {
		return false
	}

	want := strings.Split(strings.Trim(pattern, "/"), "/")
	got := strings.Split(strings.Trim(path, "/"), "/")

	if len(want) != len(got) {
		return false
	}

	for i, segment := range want {
		switch {
		case strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}"):
			if got[i] == "" {
				return false
			}
		case segment != got[i]:
			return false
		}
	}

	return true
}

// declaredTransport is a caller's HTTP client, admitting only the routes the
// caller was minted to call.
//
// It is declaredConn's HTTP half, for the same reason: which caller a suite
// mints is decided by what it declares, so a route it did not declare is one
// called by a caller chosen for something else.
type declaredTransport struct {
	next     http.RoundTripper
	t        errorf
	prefix   string
	declared []string
}

func (d *declaredTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	path := strings.TrimPrefix(req.URL.Path, d.prefix)

	for _, route := range d.declared {
		if isRouteKey(route) && routeMatches(route, req.Method, path) {
			return d.next.RoundTrip(req)
		}
	}

	d.t.Errorf("conformance: %s %s was called by a caller that did not declare it; "+
		"add its route to the conformance.Making the caller was minted with (it declared %s)",
		req.Method, path, declaredList(d.declared))

	// A response is the one thing a RoundTripper must not hand back beside an
	// error, and the body of the request is its to close.
	if req.Body != nil {
		if err := req.Body.Close(); err != nil {
			d.t.Errorf("conformance: closing the body of a refused request: %v", err)
		}
	}

	return nil, &undeclaredRouteError{method: req.Method, path: path}
}

// undeclaredRouteError is what an undeclared call answers with, so an
// assertion expecting a refusal cannot read it as one.
type undeclaredRouteError struct {
	method, path string
}

func (e *undeclaredRouteError) Error() string {
	return "conformance: " + e.method + " " + e.path + " is not among the routes this caller declared"
}

// declareHTTP puts h's client behind a transport admitting only the routes in
// declared, or returns h unchanged where it has no client to wrap.
func declareHTTP(t errorf, h *HTTPSurfaces, declared []string) *HTTPSurfaces {
	if h == nil || h.Client == nil {
		return h
	}

	next := h.Client.Transport
	if next == nil {
		next = http.DefaultTransport
	}

	prefix := ""
	if base, err := url.Parse(h.BaseURL); err == nil {
		prefix = strings.TrimSuffix(base.Path, "/")
	}

	client := *h.Client
	client.Transport = &declaredTransport{next: next, t: t, prefix: prefix, declared: slices.Clone(declared)}

	checked := *h
	checked.Client = &client

	return &checked
}

package reservations

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test/must"
)

// httpSurface is one of this module's HTTP surfaces, how to tell a subject
// mounted it, and the routes it guards with a grant.
type httpSurface struct {
	mounted func(*conformance.HTTPSurfaces) bool
	name    string
	guarded []string
}

// httpSurfaces are this module's three HTTP surfaces, each read from its own
// package's Permissions rather than listed here, so a route a package starts
// guarding is one this suite can hold a reservation of without an edit.
func httpSurfaces() []httpSurface {
	return []httpSurface{
		{
			name:    "dataprivacy",
			mounted: func(h *conformance.HTTPSurfaces) bool { return h.DataPrivacy },
			guarded: slices.Collect(maps.Keys(dataprivacyhttp.Permissions())),
		},
		{
			name:    "mediaregistry",
			mounted: func(h *conformance.HTTPSurfaces) bool { return h.MediaRegistry },
			guarded: slices.Collect(maps.Keys(mediaregistryhttp.Permissions())),
		},
		{
			name:    "operations",
			mounted: func(h *conformance.HTTPSurfaces) bool { return h.Operations },
			guarded: slices.Collect(maps.Keys(operationshttp.Permissions())),
		},
	}
}

// surfaceOf is the surface guarding route, or nil where none does.
//
// Run has already refused a reservation naming a route no surface mounts and
// one a surface serves on the caller's own standing, so nil here is this
// package's roster disagreeing with conformance's, not a deployment's mistake.
func surfaceOf(route string) *httpSurface {
	surfaces := httpSurfaces()

	for i := range surfaces {
		if slices.Contains(surfaces[i].guarded, route) {
			return &surfaces[i]
		}
	}

	return nil
}

// routeName is a route as a subtest name, with its slashes turned to spaces:
// "POST operations {operationID} cancel". A slash in a subtest name is nesting
// to go test, and each route is its own entry, not the parent of another.
func routeName(route string) string {
	method, path, _ := strings.Cut(route, " ")

	return method + " " + strings.ReplaceAll(strings.TrimPrefix(path, "/"), "/", " ")
}

// routeParam matches a route's placeholders.
var routeParam = regexp.MustCompile(`\{[^}]+\}`)

// assertRouteReserved holds the subject to one route of its reservation: an
// administrator's request is not refused for who is asking, and a member's is,
// as 403, before anything is looked up.
//
// Every placeholder is a fresh identifier nothing holds. That is what makes the
// ordering observable: a surface that checks the grant first answers the
// member 403 whatever the identifier, and one that reads the row first finds
// none and answers 404, telling a caller it refuses whether a row exists.
func assertRouteReserved(t *testing.T, s *conformance.Session, probe *conformance.Subject, route string) {
	t.Helper()

	if probe.HTTP == nil {
		conformance.Skipf(t, "conformance: this subject reserves %s and serves no HTTP surfaces to make it on", route)
	}

	surface := surfaceOf(route)
	if surface == nil {
		t.Fatalf("conformance: %s is a route Run accepted and no surface this suite knows guards; "+
			"this package's roster of HTTP surfaces is behind conformance's", route)
	}

	if !surface.mounted(probe.HTTP) {
		conformance.Skipf(t, "conformance: this subject reserves %s and mounts no %s surface to make it on", route, surface.name)
	}

	admin := s.Subject(t, conformance.AsAdmin(), conformance.Making(route))
	must.NotNil(t, admin.HTTP, must.Sprint("the subject minted an administrator with no HTTP client"))

	control, err := request(t.Context(), admin.HTTP, route)
	must.NoError(t, err, must.Sprintf("an administrator requesting %s", route))

	if problem := controlProblem(route, control); problem != "" {
		t.Fatal(problem)
	}

	member := s.Subject(t, conformance.Attempting(route))
	must.NotNil(t, member.HTTP, must.Sprint("the subject minted a member with no HTTP client"))

	refused, err := request(t.Context(), member.HTTP, route)
	must.NoError(t, err, must.Sprintf("a member requesting %s", route))

	if problem := memberProblem(route, refused); problem != "" {
		t.Error(problem)
	}
}

// request makes route through h's client, every placeholder a fresh
// identifier and a well-formed empty body where the method takes one, and
// returns the status it was answered with.
//
// The body is there so that no answer can be about a body the router failed to
// decode: a reservation is refused before the request is read, and an
// administrator's control should reach whatever the handler says next.
func request(ctx context.Context, h *conformance.HTTPSurfaces, route string) (int, error) {
	method, path, _ := strings.Cut(route, " ")
	path = routeParam.ReplaceAllStringFunc(path, func(string) string { return identifiers.New() })

	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(h.BaseURL, "/")+path, body)
	if err != nil {
		return 0, err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := h.Client.Do(req)
	if err != nil {
		return 0, err
	}

	// Only the status is read, and the connection is only reusable drained.
	_, drainErr := io.Copy(io.Discard, res.Body)
	if closeErr := res.Body.Close(); drainErr == nil {
		drainErr = closeErr
	}

	return res.StatusCode, drainErr
}

// controlProblem is why an administrator's answer on route fails the entry,
// or empty where it does not.
//
// Anything but a refusal for who is asking passes. For a made-up identifier
// that is usually 404, and that is the control doing its job: the grant was
// checked, it held, and the handler went on to look.
func controlProblem(route string, code int) string {
	switch code {
	case http.StatusForbidden:
		return fmt.Sprintf("%s refused an administrator as 403; the subject reserves it to its operators and refuses them it, "+
			"so a member's refusal proves nothing about the reservation", route)
	case http.StatusUnauthorized:
		return fmt.Sprintf("%s refused an administrator as 401; the subject's administrator credential is not reaching the surface", route)
	default:
		return ""
	}
}

// memberProblem is why a member's answer on route fails the entry, or empty
// where the member was refused as a reservation is.
func memberProblem(route string, code int) string {
	switch {
	case code == http.StatusForbidden:
		return ""
	case code == http.StatusNotFound:
		return fmt.Sprintf("%s answered a member 404 for an identifier nothing holds; the surface read the row before it checked "+
			"the grant, and a reserved route must refuse before the ownership read, or it tells non-staff which rows exist", route)
	case code >= 200 && code < 300:
		return fmt.Sprintf("%s answered a member %d; the subject names it in Seams.OperatorRoutes, and a member holds a permission "+
			"the surface's Permissions says it requires", route, code)
	case code == http.StatusBadRequest || code == http.StatusUnprocessableEntity:
		return fmt.Sprintf("%s answered a member %d; a reserved route must be refused before its request is read, "+
			"or its shape is disclosed to non-staff", route, code)
	case code == http.StatusUnauthorized:
		return fmt.Sprintf("%s refused a member as 401; the subject's member credential is not reaching the surface", route)
	default:
		return fmt.Sprintf("%s answered a member %d rather than refusing it as 403", route, code)
	}
}

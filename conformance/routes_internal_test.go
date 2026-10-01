package conformance

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/billing/billingpb"
	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestHTTPRoster(t *testing.T) {
	t.Parallel()

	routes, own := httpRoster()

	for _, route := range []string{
		dataprivacyhttp.RouteSubmit, dataprivacyhttp.RouteList, dataprivacyhttp.RouteGet,
		dataprivacyhttp.RouteConfirm, dataprivacyhttp.RouteCancel, dataprivacyhttp.RouteArtifact,
		mediaregistryhttp.RouteServe,
		operationshttp.RouteList, operationshttp.RouteGet, operationshttp.RouteCancel, operationshttp.RouteEvents,
	} {
		test.SliceContains(t, routes, route)
	}

	sorted := slices.Clone(routes)
	slices.Sort(sorted)
	test.Eq(t, slices.Compact(slices.Clone(sorted)), sorted, test.Sprint("a route is in the roster twice"))

	for _, route := range own {
		test.SliceContains(t, routes, route)
	}
}

func TestRouteProblem(t *testing.T) {
	t.Parallel()

	for _, reservable := range []string{
		dataprivacyhttp.RouteCancel,
		mediaregistryhttp.RouteServe,
		operationshttp.RouteList,
		operationshttp.RouteCancel,
	} {
		test.EqOp(t, "", routeProblem(reservable), test.Sprintf("%q", reservable))
	}

	for _, own := range []string{
		dataprivacyhttp.RouteConfirm, dataprivacyhttp.RouteArtifact,
		operationshttp.RouteGet, operationshttp.RouteEvents,
	} {
		test.StrContains(t, routeProblem(own), "own standing", test.Sprintf("%q", own))
	}

	for _, misspelled := range []string{
		"",
		"POST /operations/{id}/cancel",
		"/operations",
		"GET /operations/",
		"DELETE " + operationshttp.BasePath,
		billingpb.BillingService_ListProducts_FullMethodName,
	} {
		test.StrContains(t, routeProblem(misspelled), "not a route", test.Sprintf("%q", misspelled))
	}
}

func TestRouteMatches(t *testing.T) {
	t.Parallel()

	test.True(t, routeMatches(operationshttp.RouteCancel, http.MethodPost, "/operations/op_1/cancel"))
	test.True(t, routeMatches(operationshttp.RouteList, http.MethodGet, "/operations"))
	test.True(t, routeMatches(operationshttp.RouteList, http.MethodGet, "/operations/"))

	test.False(t, routeMatches(operationshttp.RouteCancel, http.MethodGet, "/operations/op_1/cancel"), test.Sprint("the method is part of the route"))
	test.False(t, routeMatches(operationshttp.RouteGet, http.MethodGet, "/operations/op_1/cancel"))
	test.False(t, routeMatches(operationshttp.RouteGet, http.MethodGet, "/operations"))
	test.False(t, routeMatches(operationshttp.RouteGet, http.MethodGet, "/privacy-requests/r_1"))
	test.False(t, routeMatches(billingpb.BillingService_ListProducts_FullMethodName, http.MethodGet, "/operations"))
}

// roundTrips is a transport that answers every request, so what a
// declaredTransport in front of it refuses is the declaredTransport's doing.
type roundTrips struct{ paths []string }

func (r *roundTrips) RoundTrip(req *http.Request) (*http.Response, error) {
	r.paths = append(r.paths, req.Method+" "+req.URL.Path)

	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
}

func TestDeclareHTTP(T *testing.T) {
	T.Parallel()

	get := func(t *testing.T, client *http.Client, method, url string) error {
		t.Helper()

		req, err := http.NewRequestWithContext(t.Context(), method, url, http.NoBody)
		must.NoError(t, err)

		res, err := client.Do(req)
		if err == nil {
			test.NoError(t, res.Body.Close())
		}

		return err
	}

	T.Run("a declared route reaches the transport", func(t *testing.T) {
		t.Parallel()

		inner, told := &roundTrips{}, &recorder{}
		h := declareHTTP(told, &HTTPSurfaces{Client: &http.Client{Transport: inner}, BaseURL: "http://example.invalid"},
			[]string{billingpb.BillingService_ListProducts_FullMethodName, operationshttp.RouteGet})

		test.NoError(t, get(t, h.Client, http.MethodGet, "http://example.invalid/operations/op_1"))
		test.Eq(t, []string{"GET /operations/op_1"}, inner.paths)
		test.SliceEmpty(t, told.errors)
	})

	T.Run("the base URL's own path is not part of the route", func(t *testing.T) {
		t.Parallel()

		inner, told := &roundTrips{}, &recorder{}
		h := declareHTTP(told, &HTTPSurfaces{Client: &http.Client{Transport: inner}, BaseURL: "http://example.invalid/api/"},
			[]string{operationshttp.RouteGet})

		test.NoError(t, get(t, h.Client, http.MethodGet, "http://example.invalid/api/operations/op_1"))
		test.SliceEmpty(t, told.errors)
	})

	T.Run("an undeclared route fails the test, names the route and the fix, and reaches nothing", func(t *testing.T) {
		t.Parallel()

		inner, told := &roundTrips{}, &recorder{}
		h := declareHTTP(told, &HTTPSurfaces{Client: &http.Client{Transport: inner}, BaseURL: "http://example.invalid"},
			[]string{operationshttp.RouteGet})

		err := get(t, h.Client, http.MethodPost, "http://example.invalid/operations/op_1/cancel")
		test.Error(t, err)
		test.SliceEmpty(t, inner.paths)
		must.SliceLen(t, 1, told.errors)
		test.StrContains(t, told.errors[0], "POST /operations/op_1/cancel")
		test.StrContains(t, told.errors[0], "conformance.Making")
	})

	T.Run("the subject's own client is left as it was", func(t *testing.T) {
		t.Parallel()

		inner := &roundTrips{}
		original := &HTTPSurfaces{Client: &http.Client{Transport: inner}}

		declareHTTP(&recorder{}, original, nil)

		test.EqOp(t, http.RoundTripper(inner), original.Client.Transport)
	})
}

func TestSession_Subject_routesByRouteReservation(T *testing.T) {
	T.Parallel()

	session := func(reserved ...string) *Session {
		s := minting(true)
		s.seams.OperatorRoutes = reserved

		return s
	}

	T.Run("a caller declaring a reserved route is an administrator", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "admin", session(operationshttp.RouteCancel).Subject(t, Making(operationshttp.RouteCancel)).UserID)
	})

	T.Run("a caller declaring only unreserved routes is an ordinary caller", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", session(operationshttp.RouteCancel).Subject(t, Making(operationshttp.RouteGet)).UserID)
	})

	T.Run("a reserved route is reserved", func(t *testing.T) {
		t.Parallel()

		s := session(operationshttp.RouteList)

		test.True(t, s.Reserves(operationshttp.RouteList))
		test.False(t, s.Reserves(operationshttp.RouteCancel))
	})
}

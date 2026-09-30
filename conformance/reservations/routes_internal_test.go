package reservations

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestSurfaceOf(T *testing.T) {
	T.Parallel()

	T.Run("every guarded route on every surface is found on its own", func(t *testing.T) {
		t.Parallel()

		for route, surface := range map[string]string{
			dataprivacyhttp.RouteSubmit:  "dataprivacy",
			dataprivacyhttp.RouteCancel:  "dataprivacy",
			mediaregistryhttp.RouteServe: "mediaregistry",
			operationshttp.RouteList:     "operations",
			operationshttp.RouteCancel:   "operations",
		} {
			found := surfaceOf(route)
			must.NotNil(t, found, must.Sprintf("%s is guarded by no surface", route))
			test.EqOp(t, surface, found.name, test.Sprintf("%s", route))
		}
	})

	T.Run("a route reached on the caller's own standing is guarded by none", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, surfaceOf(dataprivacyhttp.RouteConfirm))
		test.Nil(t, surfaceOf(operationshttp.RouteGet))
	})
}

func TestRouteName(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "POST operations {operationID} cancel", routeName(operationshttp.RouteCancel))
	test.EqOp(t, "GET operations", routeName(operationshttp.RouteList))
}

func TestControlProblem(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "", controlProblem(operationshttp.RouteCancel, http.StatusNotFound))
	test.EqOp(t, "", controlProblem(operationshttp.RouteList, http.StatusOK))
	test.StrContains(t, controlProblem(operationshttp.RouteCancel, http.StatusForbidden), "refused an administrator as 403")
	test.StrContains(t, controlProblem(operationshttp.RouteCancel, http.StatusUnauthorized), "administrator credential")
}

func TestMemberProblem(T *testing.T) {
	T.Parallel()

	T.Run("403 is the reservation", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", memberProblem(operationshttp.RouteCancel, http.StatusForbidden))
	})

	T.Run("404 names the ordering", func(t *testing.T) {
		t.Parallel()

		problem := memberProblem(operationshttp.RouteCancel, http.StatusNotFound)
		test.StrContains(t, problem, operationshttp.RouteCancel)
		test.StrContains(t, problem, "before the ownership read")
	})

	T.Run("an answer names the seam", func(t *testing.T) {
		t.Parallel()

		test.StrContains(t, memberProblem(operationshttp.RouteList, http.StatusOK), "Seams.OperatorRoutes")
	})

	T.Run("a validation answer names the ordering", func(t *testing.T) {
		t.Parallel()

		test.StrContains(t, memberProblem(operationshttp.RouteCancel, http.StatusBadRequest), "before its request is read")
	})

	T.Run("anything else is not a refusal", func(t *testing.T) {
		t.Parallel()

		test.StrContains(t, memberProblem(operationshttp.RouteCancel, http.StatusUnauthorized), "member credential")
		test.StrContains(t, memberProblem(operationshttp.RouteCancel, http.StatusInternalServerError), "rather than refusing it as 403")
	})
}

func TestRequest(T *testing.T) {
	T.Parallel()

	T.Run("placeholders are fresh identifiers and a POST carries an empty JSON body", func(t *testing.T) {
		t.Parallel()

		var (
			path, body, contentType string
		)

		srv := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			read, err := io.ReadAll(req.Body)
			test.NoError(t, err)

			path, body, contentType = req.URL.Path, string(read), req.Header.Get("Content-Type")
			res.WriteHeader(http.StatusForbidden)
		}))
		t.Cleanup(srv.Close)

		code, err := request(t.Context(), &conformance.HTTPSurfaces{Client: srv.Client(), BaseURL: srv.URL + "/"}, operationshttp.RouteCancel)
		must.NoError(t, err)
		test.EqOp(t, http.StatusForbidden, code)

		segments := strings.Split(strings.Trim(path, "/"), "/")
		must.SliceLen(t, 3, segments)
		test.EqOp(t, "operations", segments[0])
		test.StrNotContains(t, segments[1], "{")
		test.NotEq(t, "", segments[1])
		test.EqOp(t, "cancel", segments[2])
		test.EqOp(t, "{}", body)
		test.EqOp(t, "application/json", contentType)
	})

	T.Run("a GET carries no body", func(t *testing.T) {
		t.Parallel()

		var length int64

		srv := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			length = req.ContentLength
			res.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		code, err := request(t.Context(), &conformance.HTTPSurfaces{Client: srv.Client(), BaseURL: srv.URL}, operationshttp.RouteList)
		must.NoError(t, err)
		test.EqOp(t, http.StatusOK, code)
		test.EqOp(t, int64(0), length)
	})
}

// headerAdmin is how reservingServer's subjects say who they are.
const headerAdmin = "Conformance-Test-Admin"

// adminTransport marks every request with whether its caller is an
// administrator.
type adminTransport struct {
	next  http.RoundTripper
	admin bool
}

func (a *adminTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if a.admin {
		req.Header.Set(headerAdmin, "true")
	}

	return a.next.RoundTrip(req)
}

// reservingServer serves the three HTTP surfaces' paths the way a deployment
// reserving every guarded route does: an administrator is answered 404 for
// the identifier nothing holds, or 200 for a route that names none, and a
// member is refused 403 before anything is looked up. It keeps each request's
// method and path.
func reservingServer(t *testing.T) (srv *httptest.Server, seen func() []string) {
	t.Helper()

	var (
		mu    sync.Mutex
		paths []string
	)

	srv = httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		mu.Lock()
		paths = append(paths, req.Method+" "+req.URL.Path)
		mu.Unlock()

		switch {
		case req.Header.Get(headerAdmin) != "true":
			res.WriteHeader(http.StatusForbidden)
		case strings.Count(strings.Trim(req.URL.Path, "/"), "/") == 0:
			res.WriteHeader(http.StatusOK)
		default:
			res.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()

		return append([]string(nil), paths...)
	}
}

func TestSuite_Routes(t *testing.T) {
	t.Parallel()

	srv, seen := reservingServer(t)

	reserved := []string{dataprivacyhttp.RouteCancel, mediaregistryhttp.RouteServe, operationshttp.RouteList}

	conformance.Run(t, conformance.Seams{
		OperatorRoutes: reserved,
		NewSubject: func(_ context.Context, opts ...conformance.SubjectOption) (*conformance.Subject, error) {
			req := conformance.NewSubjectRequest(opts...)

			return &conformance.Subject{
				UserID: "caller",
				HTTP: &conformance.HTTPSurfaces{
					Client:        &http.Client{Transport: &adminTransport{next: srv.Client().Transport, admin: req.Admin}},
					BaseURL:       srv.URL,
					DataPrivacy:   true,
					MediaRegistry: true,
					Operations:    true,
				},
			}, nil
		},
	}, Suite())

	t.Cleanup(func() {
		// Two requests per route, an administrator's and a member's.
		test.SliceLen(t, 2*len(reserved), seen())
		test.SliceEmpty(t, conformance.Skips(t))
	})
}

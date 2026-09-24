package dataprivacy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// Suite is the privacy-request surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name: "dataprivacy",

		// The HTTP surfaces are not in Surfaces; whether this one is served is
		// read off the probe caller's HTTP inside, and skipped with the reason.
		Mounted: func(conformance.Surfaces) bool { return true },
		Run:     run,
	}
}

// envelope is how the router answers every route: the handler's value under
// data, beside details a deployment fills in.
type envelope[T any] struct {
	Data T `json:"data"`
}

// receipt is the part of a submission's answer the assertions read.
type receipt struct {
	Request struct {
		ID          string `json:"id"`
		OperationID string `json:"operationID"`
	} `json:"request"`
}

// page is a listing, reduced to the identifiers in it.
type page struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	probe := s.Subject(t)
	if probe.HTTP == nil || !probe.HTTP.DataPrivacy {
		t.Skip("conformance: this subject does not serve the privacy-request surface")
	}

	t.Run("a request is listed for its subject, and not for a neighbor", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoPeople(t, s)
		submitted := submit(t, mine, "export")

		test.SliceContains(t, listed(t, mine), submitted.Request.ID,
			test.Sprint("a caller's own request was missing from their listing; the absence below proves nothing"))
		test.SliceNotContains(t, listed(t, theirs), submitted.Request.ID,
			test.Sprint("a neighbor's privacy request reached this caller's listing"))
	})

	t.Run("a request is read by its subject, and absent to a neighbor", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoPeople(t, s)
		submitted := submit(t, mine, "export")
		path := dataprivacyhttp.BasePath + "/" + submitted.Request.ID

		status, _ := call(t, mine, http.MethodGet, path, nil)
		must.EqOp(t, http.StatusOK, status, must.Sprint("a caller could not read their own request"))

		// Absent rather than forbidden: a refusal would confirm that the
		// identifier names somebody's request.
		status, _ = call(t, theirs, http.MethodGet, path, nil)
		test.EqOp(t, http.StatusNotFound, status)
	})

	t.Run("a neighbor cannot cancel a request, and it survives their attempt", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoPeople(t, s)
		submitted := submit(t, mine, "export")
		path := dataprivacyhttp.BasePath + "/" + submitted.Request.ID

		status, _ := call(t, theirs, http.MethodPost, path+"/cancel", []byte("{}"))
		test.EqOp(t, http.StatusNotFound, status)

		status, _ = call(t, mine, http.MethodGet, path, nil)
		test.EqOp(t, http.StatusOK, status, test.Sprint("a neighbor's refused cancellation took the request with it"))
	})

	t.Run("the operation fulfilling a request is its subject's alone", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoPeople(t, s)
		if !mine.HTTP.Operations {
			t.Skip("conformance: this subject serves privacy requests but not the operations that fulfill them")
		}

		submitted := submit(t, mine, "export")
		if submitted.Request.OperationID == "" {
			// An erasure waiting on confirmation has no operation yet; an
			// export opens one at once, so an empty one here is the service
			// having opened nothing.
			t.Fatal("an export was accepted with no operation to fulfill it")
		}

		path := operationshttp.BasePath + "/" + submitted.Request.OperationID

		status, body := call(t, mine, http.MethodGet, path, nil)
		must.EqOp(t, http.StatusOK, status, must.Sprint("a caller could not read the operation fulfilling their own request"))
		test.StrContains(t, string(body), submitted.Request.OperationID)

		status, _ = call(t, theirs, http.MethodGet, path, nil)
		test.EqOp(t, http.StatusNotFound, status)

		// A colleague shares the tenant but not the person, and the operation
		// is the person's: following somebody's export is not something being
		// in their tenant grants.
		colleague := s.Subject(t, conformance.InTenant(mine.Scope))

		status, _ = call(t, colleague, http.MethodGet, path, nil)
		test.EqOp(t, http.StatusNotFound, status,
			test.Sprint("a colleague in the same tenant could follow somebody's privacy request"))
	})

	t.Run("a request of a kind nobody offers is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		status, _ := call(t, caller, http.MethodPost, dataprivacyhttp.BasePath, []byte(`{"type":"sideways"}`))
		test.EqOp(t, http.StatusBadRequest, status)
	})
}

// twoPeople mints two callers in two tenants, refusing to proceed if the
// subject handed back one caller twice — every confinement assertion here
// would then compare a person with themselves and pass.
func twoPeople(t *testing.T, s *conformance.Session) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine, theirs = s.Subject(t), s.Subject(t)

	must.StrNotEqFold(t, mine.UserID, theirs.UserID, must.Sprint("the subject minted two callers as one user"))
	must.StrNotEqFold(t, mine.Scope.String(), theirs.Scope.String(), must.Sprint("the subject minted two callers in one tenant"))

	return mine, theirs
}

func submit(t *testing.T, caller *conformance.Subject, kind string) *receipt {
	t.Helper()

	status, body := call(t, caller, http.MethodPost, dataprivacyhttp.BasePath, []byte(`{"type":"`+kind+`"}`))
	must.True(t, status >= 200 && status < 300, must.Sprintf("submitting a %s request answered %d: %s", kind, status, body))

	out := &envelope[receipt]{}
	must.NoError(t, json.Unmarshal(body, out))
	must.StrNotEqFold(t, "", out.Data.Request.ID, must.Sprintf("a submission's receipt named no request: %s", body))

	return &out.Data
}

func listed(t *testing.T, caller *conformance.Subject) []string {
	t.Helper()

	status, body := call(t, caller, http.MethodGet, dataprivacyhttp.BasePath, nil)
	must.EqOp(t, http.StatusOK, status, must.Sprintf("listing privacy requests answered %d: %s", status, body))

	out := &envelope[page]{}
	must.NoError(t, json.Unmarshal(body, out))

	ids := make([]string, 0, len(out.Data.Data))
	for _, r := range out.Data.Data {
		ids = append(ids, r.ID)
	}

	return ids
}

func call(t *testing.T, caller *conformance.Subject, method, path string, body []byte) (status int, response []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, strings.TrimSuffix(caller.HTTP.BaseURL, "/")+path, reader)
	must.NoError(t, err)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := caller.HTTP.Client.Do(req)
	must.NoError(t, err)

	defer func() { test.NoError(t, res.Body.Close()) }()

	response, err = io.ReadAll(res.Body)
	must.NoError(t, err)

	return res.StatusCode, response
}

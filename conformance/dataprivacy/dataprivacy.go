package dataprivacy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/conformance"
	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "dataprivacy"

// Suite is the privacy-request surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name: surface,

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

// receipt is the part of a submission's answer, and of a read, the assertions
// read.
type receipt struct {
	Request request `json:"request"`
}

// request is a privacy request as its subject is shown it.
type request struct {
	ExpiresAt   time.Time         `json:"expiresAt"`
	CompletedAt *time.Time        `json:"completedAt"`
	Failures    map[string]string `json:"failures"`
	ID          string            `json:"id"`
	OperationID string            `json:"operationID"`
	ArtifactRef string            `json:"artifactRef"`
	LastError   string            `json:"lastError"`
	Status      string            `json:"status"`
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

		// The neighbor shares the tenant but not the person, and the operation
		// is the person's: following somebody's export is not something being
		// in their tenant grants.
		status, _ = call(t, theirs, http.MethodGet, path, nil)
		test.EqOp(t, http.StatusNotFound, status,
			test.Sprint("a colleague in the same tenant could follow somebody's privacy request"))
	})

	t.Run("a request is absent to a caller in another tenant", func(t *testing.T) {
		t.Parallel()

		// The per-person assertions above hold inside one directory. This is
		// the wall between directories, for a deployment that has more than
		// one: a request's scope is a second confinement beside its subject,
		// and a caller elsewhere is refused by both.
		mine, theirs := s.TwoTenants(t, surface)
		submitted := submit(t, mine, "export")

		test.SliceContains(t, listed(t, mine), submitted.Request.ID,
			test.Sprint("a caller's own request was missing from their listing; the absence below proves nothing"))
		test.SliceNotContains(t, listed(t, theirs), submitted.Request.ID,
			test.Sprint("a privacy request in one tenant reached a listing in another"))

		status, _ := call(t, theirs, http.MethodGet, dataprivacyhttp.BasePath+"/"+submitted.Request.ID, nil)
		test.EqOp(t, http.StatusNotFound, status)

		if submitted.Request.OperationID != "" && mine.HTTP.Operations {
			status, _ = call(t, theirs, http.MethodGet, operationshttp.BasePath+"/"+submitted.Request.OperationID, nil)
			test.EqOp(t, http.StatusNotFound, status,
				test.Sprint("a caller in another tenant could follow somebody's privacy request"))
		}
	})

	t.Run("a request of a kind nobody offers is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		status, _ := call(t, caller, http.MethodPost, dataprivacyhttp.BasePath, []byte(`{"type":"sideways"}`))
		test.EqOp(t, http.StatusBadRequest, status)
	})

	t.Run("fulfillment", func(t *testing.T) {
		t.Parallel()

		fulfillment(t, s)
	})
}

// twoPeople mints two different people in one directory: a caller, and a
// colleague in the caller's tenant — or beside them in the global scope, where
// a deployment serves this surface from one.
//
// Not two tenants. A privacy request is confined to the person it is about, so
// the neighbor this surface owes a refusal is the one sharing everything but
// the person; minting them apart would let a tenant wall answer for a subject
// check that is missing, and would skip every assertion on a deployment with
// no tenants at all, which is where two users in one directory are commonest.
// The wall between tenants is asserted on its own, with TwoTenants.
//
// It refuses to proceed if the subject handed back one caller twice — every
// confinement assertion here would then compare a person with themselves and
// pass.
func twoPeople(t *testing.T, s *conformance.Session) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine = s.Subject(t)
	theirs = s.Subject(t, conformance.InTenant(surface, mine.ScopeFor(surface)))

	must.StrNotEqFold(t, mine.UserID, theirs.UserID, must.Sprint("the subject minted two callers as one user"))

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

package dataprivacy

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/conformance/internal/httpcall"
	"github.com/primandproper/platform-go/v15/conformance/internal/people"
	dataprivacyhttp "github.com/primandproper/platform-go/v15/dataprivacy/http"
	operationshttp "github.com/primandproper/platform-go/v15/operations/http"

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

// receipt is the part of a submission's answer, and of a read, the assertions
// read.
type receipt struct {
	Request  request `json:"request"`
	Artifact string  `json:"artifact"`
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
		conformance.Skip(t, "conformance: this subject does not serve the privacy-request surface")
	}

	t.Run("a request is listed for its subject, and not for a neighbor", func(t *testing.T) {
		t.Parallel()

		mine, theirs := people.Two(t, s, surface,
			[]string{dataprivacyhttp.RouteSubmit, dataprivacyhttp.RouteList},
			[]string{dataprivacyhttp.RouteList})
		submitted := submit(t, mine, "export")

		test.SliceContains(t, listed(t, mine), submitted.Request.ID,
			test.Sprint("a caller's own request was missing from their listing; the absence below proves nothing"))
		test.SliceNotContains(t, listed(t, theirs), submitted.Request.ID,
			test.Sprint("a neighbor's privacy request reached this caller's listing"))
	})

	t.Run("a request is read by its subject, and absent to a neighbor", func(t *testing.T) {
		t.Parallel()

		mine, theirs := people.Two(t, s, surface,
			[]string{dataprivacyhttp.RouteSubmit, dataprivacyhttp.RouteGet},
			[]string{dataprivacyhttp.RouteGet})
		submitted := submit(t, mine, "export")
		path := dataprivacyhttp.BasePath + "/" + submitted.Request.ID

		status, _ := httpcall.Call(t, mine, http.MethodGet, path, nil)
		must.EqOp(t, http.StatusOK, status, must.Sprint("a caller could not read their own request"))

		// Absent rather than forbidden: a refusal would confirm that the
		// identifier names somebody's request.
		status, _ = httpcall.Call(t, theirs, http.MethodGet, path, nil)
		test.EqOp(t, http.StatusNotFound, status)
	})

	t.Run("a neighbor cannot cancel a request, and it survives their attempt", func(t *testing.T) {
		t.Parallel()

		mine, theirs := people.Two(t, s, surface,
			[]string{dataprivacyhttp.RouteSubmit, dataprivacyhttp.RouteGet},
			[]string{dataprivacyhttp.RouteCancel})
		submitted := submit(t, mine, "export")
		path := dataprivacyhttp.BasePath + "/" + submitted.Request.ID

		status, _ := httpcall.Call(t, theirs, http.MethodPost, path+"/cancel", []byte("{}"))
		test.EqOp(t, http.StatusNotFound, status)

		status, _ = httpcall.Call(t, mine, http.MethodGet, path, nil)
		test.EqOp(t, http.StatusOK, status, test.Sprint("a neighbor's refused cancellation took the request with it"))
	})

	t.Run("the operation fulfilling a request is its subject's alone", func(t *testing.T) {
		t.Parallel()

		mine, theirs := people.Two(t, s, surface,
			[]string{dataprivacyhttp.RouteSubmit, operationshttp.RouteGet},
			[]string{operationshttp.RouteGet})
		if !mine.HTTP.Operations {
			conformance.Skip(t, "conformance: this subject serves privacy requests but not the operations that fulfill them")
		}

		submitted := submit(t, mine, "export")
		if submitted.Request.OperationID == "" {
			// An erasure waiting on confirmation has no operation yet; an
			// export opens one at once, so an empty one here is the service
			// having opened nothing.
			t.Fatal("an export was accepted with no operation to fulfill it")
		}

		path := operationshttp.BasePath + "/" + submitted.Request.OperationID

		status, body := httpcall.Call(t, mine, http.MethodGet, path, nil)
		must.EqOp(t, http.StatusOK, status, must.Sprint("a caller could not read the operation fulfilling their own request"))
		test.StrContains(t, string(body), submitted.Request.OperationID)

		// The neighbor shares the tenant but not the person, and the operation
		// is the person's: following somebody's export is not something being
		// in their tenant grants.
		status, _ = httpcall.Call(t, theirs, http.MethodGet, path, nil)
		test.EqOp(t, http.StatusNotFound, status,
			test.Sprint("a colleague in the same tenant could follow somebody's privacy request"))
	})

	t.Run("a request is absent to a caller in another tenant", func(t *testing.T) {
		t.Parallel()

		// The per-person assertions above hold inside one directory. This is
		// the wall between directories, for a deployment that has more than
		// one: a request's scope is a second confinement beside its subject,
		// and a caller elsewhere is refused by both.
		mine, theirs := s.TwoTenants(t, surface, conformance.Making(
			dataprivacyhttp.RouteSubmit, dataprivacyhttp.RouteList, dataprivacyhttp.RouteGet, operationshttp.RouteGet))
		submitted := submit(t, mine, "export")

		test.SliceContains(t, listed(t, mine), submitted.Request.ID,
			test.Sprint("a caller's own request was missing from their listing; the absence below proves nothing"))
		test.SliceNotContains(t, listed(t, theirs), submitted.Request.ID,
			test.Sprint("a privacy request in one tenant reached a listing in another"))

		status, _ := httpcall.Call(t, theirs, http.MethodGet, dataprivacyhttp.BasePath+"/"+submitted.Request.ID, nil)
		test.EqOp(t, http.StatusNotFound, status)

		if submitted.Request.OperationID != "" && mine.HTTP.Operations {
			status, _ = httpcall.Call(t, theirs, http.MethodGet, operationshttp.BasePath+"/"+submitted.Request.OperationID, nil)
			test.EqOp(t, http.StatusNotFound, status,
				test.Sprint("a caller in another tenant could follow somebody's privacy request"))
		}
	})

	t.Run("a request of a kind nobody offers is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(dataprivacyhttp.RouteSubmit))

		status, _ := httpcall.Call(t, caller, http.MethodPost, dataprivacyhttp.BasePath, []byte(`{"type":"sideways"}`))
		test.EqOp(t, http.StatusBadRequest, status)
	})

	t.Run("fulfillment", func(t *testing.T) {
		t.Parallel()

		fulfillment(t, s)
	})
}

func submit(t *testing.T, caller *conformance.Subject, kind string) *receipt {
	t.Helper()

	status, body := httpcall.Call(t, caller, http.MethodPost, dataprivacyhttp.BasePath, []byte(`{"type":"`+kind+`"}`))
	must.True(t, status >= 200 && status < 300, must.Sprintf("submitting a %s request answered %d: %s", kind, status, body))

	out := &httpcall.Envelope[receipt]{}
	must.NoError(t, json.Unmarshal(body, out))
	must.StrNotEqFold(t, "", out.Data.Request.ID, must.Sprintf("a submission's receipt named no request: %s", body))

	return &out.Data
}

func listed(t *testing.T, caller *conformance.Subject) []string {
	t.Helper()

	status, body := httpcall.Call(t, caller, http.MethodGet, dataprivacyhttp.BasePath, nil)
	must.EqOp(t, http.StatusOK, status, must.Sprintf("listing privacy requests answered %d: %s", status, body))

	out := &httpcall.Envelope[page]{}
	must.NoError(t, json.Unmarshal(body, out))

	ids := make([]string, 0, len(out.Data.Data))
	for _, r := range out.Data.Data {
		ids = append(ids, r.ID)
	}

	return ids
}

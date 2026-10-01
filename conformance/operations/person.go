package operations

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/conformance/internal/httpcall"
	"github.com/primandproper/platform-go/v14/conformance/internal/people"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// privacy is the privacy-request suite's surface, whose tenancy the person
// owning an export's operation is minted in.
const privacy = "dataprivacy"

// personOwned asserts the person-owned half: an export's operation, which
// belongs to the person the export is about, is listed, cancelled and streamed
// for them and refused to a colleague in their tenant.
//
// The read is not here. The dataprivacy suite asserts it, for the same pair, as
// the promise that crosses the two packages; a second copy would be a second
// place for it to drift.
func personOwned(t *testing.T, s *conformance.Session, probe *conformance.Subject) {
	t.Helper()

	if !probe.HTTP.DataPrivacy {
		conformance.Skip(t, "conformance: this subject serves operations but no privacy requests, "+
			"which are the only operations a client of this module's surfaces can start")
	}

	t.Run("a person's operation is listed for them, and not for a colleague in their tenant", func(t *testing.T) {
		t.Parallel()

		mine, theirs := people.Two(t, s, privacy,
			[]string{dataprivacyhttp.RouteSubmit, operationshttp.RouteList},
			[]string{operationshttp.RouteList})
		op := exported(t, mine)

		must.True(t, listed(t, mine, dataprivacy.KindExport, op),
			must.Sprint("a person's own operation was missing from their listing; the absence below proves nothing"))
		test.False(t, listed(t, theirs, dataprivacy.KindExport, op),
			test.Sprint("a colleague in the same tenant could list somebody's privacy request's operation"))
	})

	t.Run("a colleague cannot cancel a person's operation, and it survives their attempt", func(t *testing.T) {
		t.Parallel()

		mine, theirs := people.Two(t, s, privacy,
			[]string{dataprivacyhttp.RouteSubmit, operationshttp.RouteGet, operationshttp.RouteCancel},
			[]string{operationshttp.RouteCancel})
		op := exported(t, mine)

		// Absent rather than forbidden: the owners are bound into the write,
		// so somebody else's operation is one it does not find.
		status, _ := cancel(t, theirs, op)
		test.EqOp(t, http.StatusNotFound, status)

		// Nothing but a cancellation writes cancelled, so this holds whatever
		// the worker has done meanwhile.
		status, got := read(t, mine, op)
		must.EqOp(t, http.StatusOK, status, must.Sprint("a person could not read their own operation"))
		test.NotEqOp(t, stateCancelled, got.State, test.Sprint("a colleague's refused cancellation cancelled the operation anyway"))

		// The positive control, after the attempt: the route does cancel, for
		// the owner. The state is not asserted, because cancelling a finished
		// operation returns it unchanged and this one may have finished.
		status, cancelled := cancel(t, mine, op)
		must.EqOp(t, http.StatusOK, status, must.Sprint("a person could not cancel their own operation; the refusal above proves nothing"))
		test.EqOp(t, op, cancelled.ID)
	})

	t.Run("a colleague cannot follow a person's operation", func(t *testing.T) {
		t.Parallel()

		if !probe.HTTP.OperationEvents {
			conformance.Skip(t, "conformance: this subject runs no watcher, so it mounts no operation event stream")
		}

		mine, theirs := people.Two(t, s, privacy,
			[]string{dataprivacyhttp.RouteSubmit, operationshttp.RouteEvents},
			[]string{operationshttp.RouteEvents})
		op := exported(t, mine)

		status, _ := subscribe(t, theirs, op)
		test.EqOp(t, http.StatusNotFound, status,
			test.Sprint("a colleague in the same tenant could subscribe to somebody's privacy request's operation"))

		status, snapshot := subscribe(t, mine, op)
		must.EqOp(t, http.StatusOK, status, must.Sprint("a person could not follow their own operation; the refusal above proves nothing"))
		test.StrContains(t, snapshot, op, test.Sprint("the first snapshot was not of the operation subscribed to"))
	})
}

// exported submits an export as caller, and returns the identifier of the
// operation fulfilling it, which belongs to caller.
//
// One per assertion, never shared: cancelling the operation cancels the
// request, and an assertion reading either would read the other's doing.
func exported(t *testing.T, caller *conformance.Subject) string {
	t.Helper()

	status, body := httpcall.Call(t, caller, http.MethodPost, dataprivacyhttp.BasePath, []byte(`{"type":"export"}`))
	must.True(t, status >= 200 && status < 300, must.Sprintf("submitting an export answered %d: %s", status, body))

	out := &httpcall.Envelope[struct {
		Request struct {
			OperationID string `json:"operationID"`
		} `json:"request"`
	}]{}
	must.NoError(t, json.Unmarshal(body, out))
	must.NotEqOp(t, "", out.Data.Request.OperationID, must.Sprintf("an export was accepted with no operation to fulfill it: %s", body))

	return out.Data.Request.OperationID
}

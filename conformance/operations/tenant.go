package operations

import (
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// tenantOwned asserts the tenant-owned half: an operation the application
// started for a tenant is its members' to read and list, and absent on every
// route to a caller in another tenant.
//
// It is the half where a colleague is admitted rather than refused, which is
// what makes it the positive control for the owners fan-out: a deployment
// whose surface resolved only the person would refuse the colleague here, and
// the person-owned half could not tell.
func tenantOwned(t *testing.T, s *conformance.Session) {
	t.Helper()

	operated := s.Seams().Actions.Operated
	s.NeedsAction(t, operated != nil, "Operated")

	routes := []string{operationshttp.RouteGet, operationshttp.RouteList, operationshttp.RouteCancel, operationshttp.RouteEvents}

	mine, theirs := s.TwoTenants(t, surface, conformance.Making(routes...))
	colleague := s.Subject(t, conformance.Making(operationshttp.RouteGet, operationshttp.RouteList),
		conformance.InTenant(surface, mine.ScopeFor(surface)))

	op, err := operated(t.Context(), mine.ScopeFor(surface))
	must.NoError(t, err, must.Sprint("starting the application's own work"))
	must.NotEqOp(t, "", op, must.Sprint("the Operated action reported no operation"))

	status, got := read(t, colleague, op)
	must.EqOp(t, http.StatusOK, status,
		must.Sprint("a member of the tenant could not read its operation; the absences below prove nothing"))
	must.EqOp(t, op, got.ID)

	must.True(t, listed(t, colleague, got.Kind, op),
		must.Sprint("a member of the tenant could not list its operation; the absence below proves nothing"))

	status, _ = read(t, theirs, op)
	test.EqOp(t, http.StatusNotFound, status, test.Sprint("a caller in another tenant could read its operation"))

	test.False(t, listed(t, theirs, got.Kind, op), test.Sprint("a tenant's operation reached a listing in another"))

	status, _ = cancel(t, theirs, op)
	test.EqOp(t, http.StatusNotFound, status, test.Sprint("a caller in another tenant could cancel its operation"))

	if mine.HTTP.OperationEvents {
		status, _ = subscribe(t, theirs, op)
		test.EqOp(t, http.StatusNotFound, status, test.Sprint("a caller in another tenant could follow its operation"))
	}

	status, got = read(t, colleague, op)
	must.EqOp(t, http.StatusOK, status)
	test.NotEqOp(t, stateCancelled, got.State, test.Sprint("a refused cancellation from another tenant cancelled the operation anyway"))
}

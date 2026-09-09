package grpc_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file is the roster of which store methods cross onto the wire, and it is
// the ruling rather than a description of it. waitlists.Store has seventeen
// methods and this service serves all seventeen; the absence list is empty, and
// an eighteenth method is in neither list until somebody says which.
//
// The mechanism is billing/grpc's, adopted rather than re-derived, and it is
// internal/sentinelmatrix's applied to methods instead of sentinels — for the
// same reason in all three places: the failure worth catching is not a wrong
// row, it is a method added later that nobody classified. An empty absence list
// is the least interesting state this mechanism can be in and is exactly why it
// belongs here: it is the state that stops being true silently.

// absent is every waitlists.Store method this service deliberately does not
// serve, with the reason recorded beside it so the roster is the argument and
// not the paperwork.
//
// It is empty, and that is the ruling for this package rather than an oversight.
// Every carve-out on this lane is one test applied to different machinery — is
// the realistic caller a worker on a timer, a processor callback, or the
// consumer's own code inside its own transaction — and a waitlist has no queue
// protocol, no fan-out and no provider callback. Every one of the seventeen is a
// form somebody submitted or a console somebody is looking at.
//
// The nearest thing to an entry is WithdrawSignupsForSubject, which is the
// erasure path waitlists/privacy builds a dataprivacy.Eraser on. It is served
// because its realistic caller is an operator honoring a request out of band,
// and it is behind a grant of its own rather than behind the ordinary write.
var absent = map[string]string{}

// storeMethods is every method on waitlists.Store, read off the interface rather
// than listed, so a method added to it fails the tests below.
func storeMethods() []string {
	t := reflect.TypeFor[waitlists.Store]()

	out := make([]string, 0, t.NumMethod())
	for method := range t.Methods() {
		out = append(out, method.Name)
	}

	return out
}

// rpcNames is every RPC on the service, by the bare method name the store method
// it serves also carries.
func rpcNames() []string {
	out := make([]string, 0, len(waitlistspb.WaitlistsService_ServiceDesc.Methods))
	for _, m := range waitlistspb.WaitlistsService_ServiceDesc.Methods {
		out = append(out, m.MethodName)
	}

	return out
}

// TestEveryStoreMethodIsEitherServedOrRuledOut is the roster's forward
// direction: a store method in neither list is a decision nobody made.
//
// The expensive direction to be wrong in is publishing something. A method added
// to the store and reflexively given an RPC is how a write that has to commit
// with its caller's transaction arrives on a wire without anybody arguing for
// it — and this package's empty absence list makes that the easy mistake here,
// because "everything crosses" is the habit the seventeen establish.
func TestEveryStoreMethodIsEitherServedOrRuledOut(T *testing.T) {
	T.Parallel()

	methods := storeMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("read no methods off waitlists.Store, so this asserted nothing"))

	served := rpcNames()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, ruledOut := absent[method]
			onTheWire := slices.Contains(served, method)

			test.True(t, ruledOut != onTheWire, test.Sprintf(
				"waitlists.Store.%s is %s; it has to be exactly one of served and ruled out",
				method, servedAndRuledOut(onTheWire, ruledOut)))
		})
	}
}

func servedAndRuledOut(onTheWire, ruledOut bool) string {
	switch {
	case onTheWire && ruledOut:
		return "both an RPC and recorded as absent"
	default:
		return "neither an RPC nor recorded as absent"
	}
}

// TestNoRulingOutlivesItsMethod is the other direction: a row naming a store
// method that no longer exists is a roster describing a tree that has moved, and
// it reads live.
func TestNoRulingOutlivesItsMethod(T *testing.T) {
	T.Parallel()

	methods := storeMethods()

	for method := range absent {
		test.SliceContains(T, methods, method, test.Sprintf(
			"the roster rules out waitlists.Store.%s, which waitlists.Store does not declare", method))
	}
}

// TestNoRPCIsWithoutAStoreMethod is the direction the other two do not cover,
// and it is the one worth having where the absence list is empty: an RPC whose
// name matches nothing on the Store is a method this surface orchestrates rather
// than serves, which is a different kind of package and a decision to make out
// loud.
func TestNoRPCIsWithoutAStoreMethod(T *testing.T) {
	T.Parallel()

	methods := storeMethods()

	for _, rpc := range rpcNames() {
		test.SliceContains(T, methods, rpc, test.Sprintf(
			"the service serves %q, which waitlists.Store does not declare", rpc))
	}
}

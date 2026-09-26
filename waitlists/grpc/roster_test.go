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
// the ruling rather than a description of it. waitlists.Store has eighteen
// methods and this service serves all eighteen; the absence list is empty, and
// a nineteenth method is in neither list until somebody says which.
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
// protocol, no fan-out and no provider callback. Every one of the eighteen is a
// form somebody submitted, a link somebody followed, or a console somebody is
// looking at.
//
// The nearest thing to an entry is WithdrawSignupsForSubject, which is the
// erasure path waitlists/privacy builds a dataprivacy.Eraser on. comments,
// issuereports and settings each rule their equivalent out, and what separates
// this one is not its realistic caller — an operator honoring a request out of
// band is theirs too — but that it is re-runnable where a hard delete committed
// in a transaction of its own is not: it blanks the subject reference it matched
// on, so a second call finds nothing the first left and answers zero. The
// package documentation carries the argument, and it is served behind a grant of
// its own rather than behind the ordinary write.
var absent = map[string]string{}

// orchestrated is every RPC this service serves that is not a store method of
// the same name, with the store method it reaches and the reason it is a door
// of its own.
//
// It is the ruling TestNoRPCIsWithoutAStoreMethod asks to be made out loud.
var orchestrated = map[string]string{
	// Withdraw's second door. Withdraw's request names a signup, which is a row
	// identifier and not a credential, so its standing is the consumer's
	// SignupAuthorizer; this one names only an unsubscribe link minted against
	// one signup and mailed to its address, and the link is the standing. Two
	// requests with two different proofs are two RPCs, rather than one request
	// whose proof is whichever field happens to be set.
	"Unsubscribe": "Withdraw",
}

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
// because "everything crosses" is the habit the eighteen establish.
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
		if reaches, ok := orchestrated[rpc]; ok {
			test.SliceContains(T, methods, reaches, test.Sprintf(
				"the roster says %q reaches waitlists.Store.%s, which waitlists.Store does not declare", rpc, reaches))
			test.SliceNotContains(T, methods, rpc, test.Sprintf(
				"the roster says %q is orchestrated, and waitlists.Store declares a method of that name", rpc))

			continue
		}

		test.SliceContains(T, methods, rpc, test.Sprintf(
			"the service serves %q, which waitlists.Store does not declare", rpc))
	}

	served := rpcNames()
	for rpc := range orchestrated {
		test.SliceContains(T, served, rpc, test.Sprintf(
			"the roster names %q as orchestrated, which the service does not serve", rpc))
	}
}

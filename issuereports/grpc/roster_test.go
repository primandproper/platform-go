package grpc_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/issuereports"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file is the roster of which store methods cross onto the wire, and it is
// the ruling rather than a description of it. issuereports.Store has eleven
// methods and this service serves ten; the one absence is a decision, and a
// twelfth method is in neither list until somebody says which.
//
// The mechanism is webhooks/grpc's and billing/grpc's, adopted rather than
// re-derived, and it is internal/sentinelmatrix's applied to methods instead of
// sentinels — for the same reason in all of them: the failure worth catching is
// not a wrong row, it is a method added later that nobody classified. On this
// surface that means either an RPC nobody meant to publish, or a write whose
// whole property is that it commits inside its caller's transaction.

// absent is every issuereports.Store method this service deliberately does not
// serve, with the reason recorded beside it so the roster is the argument and
// not the paperwork.
//
// The reason is the short form of the one the Store method itself carries; that
// is where the long version lives, because a reader of the Store should find the
// answer where they are standing.
var absent = map[string]string{
	// The one. It destroys every report one person filed, and it runs inside the
	// caller's transaction so that a subject's reports and the rest of their
	// footprint commit or roll back together. An RPC is exactly a caller
	// choosing when that commit happens, and an erasure run that failed halfway
	// leaves a subject who has been told they were forgotten and half was.
	"DeleteReportsByReporter": "erasure machinery, committing with the rest of a subject's footprint",
}

// storeMethods is every method on issuereports.Store, read off the interface
// rather than listed, so a method added to it fails the two tests below.
func storeMethods() []string {
	t := reflect.TypeFor[issuereports.Store]()

	out := make([]string, 0, t.NumMethod())
	for method := range t.Methods() {
		out = append(out, method.Name)
	}

	return out
}

// rpcNames is every RPC on the service, by the bare method name the store method
// it serves also carries.
func rpcNames() []string {
	out := make([]string, 0, len(issuereportspb.IssueReportsService_ServiceDesc.Methods))
	for _, m := range issuereportspb.IssueReportsService_ServiceDesc.Methods {
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
// it.
func TestEveryStoreMethodIsEitherServedOrRuledOut(T *testing.T) {
	T.Parallel()

	methods := storeMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("read no methods off issuereports.Store, so this asserted nothing"))

	served := rpcNames()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, ruledOut := absent[method]
			onTheWire := slices.Contains(served, method)

			test.True(t, ruledOut != onTheWire, test.Sprintf(
				"issuereports.Store.%s is %s; it has to be exactly one of served and ruled out",
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
			"the roster rules out issuereports.Store.%s, which issuereports.Store does not declare", method))
	}
}

// TestErasureIsAbsent names the one the lane's ruling for this package turned
// on, because a roster that only counted would let it be swapped for something
// else.
//
// It is the only absence here, so it is also the only thing somebody would add
// in good faith — a console that wants to honor a deletion request from the
// same screen it triages on is exactly the caller who would ask for it, and the
// answer is that the erasure belongs in the run that erases everything else
// about that person.
func TestErasureIsAbsent(T *testing.T) {
	T.Parallel()

	test.SliceNotContains(T, rpcNames(), "DeleteReportsByReporter", test.Sprint(
		"DeleteReportsByReporter is an RPC; its delete exists to commit inside the caller's transaction"))

	_, ruledOut := absent["DeleteReportsByReporter"]
	test.True(T, ruledOut, test.Sprint("DeleteReportsByReporter is not recorded as absent"))
}

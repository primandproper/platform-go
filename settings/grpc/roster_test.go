package grpc_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/settings"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file is the roster of which store methods cross onto the wire, and it is
// the ruling rather than a description of it. settings.Store has fourteen
// methods and this service serves thirteen; the one absence is a decision, and
// a fifteenth method is in neither list until somebody says which.
//
// The mechanism is billing/grpc's, adopted rather than re-derived, and it is
// internal/sentinelmatrix's applied to methods instead of sentinels — for the
// same reason in all three places: the failure worth catching is not a wrong
// row, it is a method added later that nobody classified. On this surface that
// means either an RPC nobody meant to publish, or a write whose whole property
// is that it commits inside its caller's transaction.
//
// The count being thirteen to one rather than nine to nine is the point rather
// than a weakness of the mechanism. A roster that has one thing to say is still
// the thing that fails when a fifteenth method arrives.

// absent is every settings.Store method this service deliberately does not
// serve, with the reason recorded beside it so the roster is the argument and
// not the paperwork.
//
// The reason is the short form of the one the Store method itself carries; that
// is where the long version lives, because a reader of the Store should find
// the answer where they are standing.
var absent = map[string]string{
	// The one absence, and it is erasure. It destroys everything one subject
	// answered, cleared answers included, and its caller is a dataprivacy.Eraser
	// or a retention sweep running inside the transaction that removes the rest
	// of the person. An RPC moves the write out of that transaction, which
	// leaves somebody erased from one table and present in the others.
	"DeleteValuesForSubject": "erasure, which commits with the rest of the person or not at all",
}

// storeMethods is every method on settings.Store, read off the interface rather
// than listed, so a method added to it fails the two tests below.
func storeMethods() []string {
	t := reflect.TypeFor[settings.Store]()

	out := make([]string, 0, t.NumMethod())
	for method := range t.Methods() {
		out = append(out, method.Name)
	}

	return out
}

// rpcNames is every RPC on the service, by the bare method name the store
// method it serves also carries.
func rpcNames() []string {
	out := make([]string, 0, len(settingspb.SettingsService_ServiceDesc.Methods))
	for _, m := range settingspb.SettingsService_ServiceDesc.Methods {
		out = append(out, m.MethodName)
	}

	return out
}

// TestEveryStoreMethodIsEitherServedOrRuledOut is the roster's forward
// direction: a store method in neither list is a decision nobody made.
//
// The expensive direction to be wrong in is publishing something. A method
// added to the store and reflexively given an RPC is how a write that has to
// commit with its caller's transaction arrives on a wire without anybody
// arguing for it.
func TestEveryStoreMethodIsEitherServedOrRuledOut(T *testing.T) {
	T.Parallel()

	methods := storeMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("read no methods off settings.Store, so this asserted nothing"))

	served := rpcNames()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, ruledOut := absent[method]
			onTheWire := slices.Contains(served, method)

			test.True(t, ruledOut != onTheWire, test.Sprintf(
				"settings.Store.%s is %s; it has to be exactly one of served and ruled out",
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
// method that no longer exists is a roster describing a tree that has moved,
// and it reads live.
func TestNoRulingOutlivesItsMethod(T *testing.T) {
	T.Parallel()

	methods := storeMethods()

	for method := range absent {
		test.SliceContains(T, methods, method, test.Sprintf(
			"the roster rules out settings.Store.%s, which settings.Store does not declare", method))
	}
}

// TestDeleteValuesForSubjectIsAbsent names the one the lane's ruling for this
// package turned on, because a roster that only counted would let it be swapped
// for something else.
//
// It is the absence somebody would add in good faith: it is consumer-facing, it
// reads like the erasure half of a settings API, and the reason it must not be
// an RPC is a property of its caller rather than of the method.
func TestDeleteValuesForSubjectIsAbsent(T *testing.T) {
	T.Parallel()

	test.SliceNotContains(T, rpcNames(), "DeleteValuesForSubject", test.Sprint(
		"DeleteValuesForSubject is an RPC; its write exists to commit inside the caller's transaction"))

	_, ruledOut := absent["DeleteValuesForSubject"]
	test.True(T, ruledOut, test.Sprint("DeleteValuesForSubject is not recorded as absent"))
}

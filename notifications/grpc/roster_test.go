package grpc_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/notifications"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file is the roster of which store methods cross onto the wire, and it is
// the ruling rather than a description of it. notifications.Inbox and
// notifications.Registry declare twelve methods between them and this service
// serves nine; the three absences are each a decision, and a thirteenth method
// is in neither list until somebody says which.
//
// The mechanism is webhooks/grpc's, adopted rather than re-derived, and it is
// internal/sentinelmatrix's applied to methods instead of sentinels — for the
// same reason in both places: the failure worth catching is not a wrong row, it
// is a method added later that nobody classified. On this surface that means
// either an RPC nobody meant to publish, or a write whose whole property is that
// it commits inside its caller's transaction.

// absent is every method on the two seams this service deliberately does not
// serve, with the shape of machinery it is recorded beside it.
//
// Three entries and three different shapes, which is what makes this package the
// place the distinction is worth reading. Each reason is the short form of one
// the Store method itself carries; that is where the long version lives, because
// a reader of the Go API should find the answer where they are standing.
var absent = map[string]string{
	// The transactional companion. It commits with the thing the notification is
	// about, and an RPC is a caller choosing a different moment for that commit —
	// so a refused order still tells somebody their order was placed.
	"CreateNotification": "the write whose whole property is committing inside the caller's transaction",

	// The internal fan-out: a sender asking itself where to push. It names other
	// people's principals by construction, which is the one request field this
	// schema reserves the name of.
	"ListDevicesByPrincipals": "the sender's own read, across principals no caller may name",

	// The provider callback hook, and the one absence here that is a security
	// property rather than a shape. It removes a token whoever it belongs to, on
	// the word of APNs or FCM; as an RPC it is a caller claiming a provider said
	// so, about any handset in any tenant.
	"InvalidateDeviceToken": "the hook a push provider's verdict reaches, scoped to nobody",
}

// storeMethods is every method on the two seams, read off the interfaces rather
// than listed, so a method added to either fails the two tests below.
func storeMethods() []string {
	var out []string

	for _, t := range []reflect.Type{
		reflect.TypeFor[notifications.Inbox](),
		reflect.TypeFor[notifications.Registry](),
	} {
		for method := range t.Methods() {
			out = append(out, method.Name)
		}
	}

	return out
}

// rpcNames is every RPC on the service, by the bare method name the store method
// it serves also carries.
func rpcNames() []string {
	out := make([]string, 0, len(notificationspb.NotificationsService_ServiceDesc.Methods))
	for _, m := range notificationspb.NotificationsService_ServiceDesc.Methods {
		out = append(out, m.MethodName)
	}

	return out
}

// TestEveryStoreMethodIsEitherServedOrRuledOut is the roster's forward
// direction: a store method in neither list is a decision nobody made.
//
// The expensive direction to be wrong in is publishing something. A method added
// to one of the seams and reflexively given an RPC is how a write that has to
// commit with its caller's transaction — or a hook that takes no scope at all —
// arrives on a wire without anybody arguing for it.
func TestEveryStoreMethodIsEitherServedOrRuledOut(T *testing.T) {
	T.Parallel()

	methods := storeMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("read no methods off the two seams, so this asserted nothing"))

	served := rpcNames()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, ruledOut := absent[method]
			onTheWire := slices.Contains(served, method)

			test.True(t, ruledOut != onTheWire, test.Sprintf(
				"notifications.%s is %s; it has to be exactly one of served and ruled out",
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

// TestNoRulingOutlivesItsMethod is the other direction: a row naming a method
// that no longer exists is a roster describing a tree that has moved, and it
// reads live.
func TestNoRulingOutlivesItsMethod(T *testing.T) {
	T.Parallel()

	methods := storeMethods()

	for method := range absent {
		test.SliceContains(T, methods, method, test.Sprintf(
			"the roster rules out notifications.%s, which neither seam declares", method))
	}
}

// TestInvalidateDeviceTokenIsAbsent names the absence whose reason is security
// rather than shape, because a roster that only counted would let it be swapped
// for something else.
//
// It is the one method here that an author could publish in good faith — it
// reads like "remove a dead token", and it is wired as
// mobile.WithTokenInvalidator precisely so that the only caller is a sender
// holding a provider's verdict.
func TestInvalidateDeviceTokenIsAbsent(T *testing.T) {
	T.Parallel()

	test.SliceNotContains(T, rpcNames(), "InvalidateDeviceToken", test.Sprint(
		"InvalidateDeviceToken is an RPC; it deletes any handset's registration in any tenant"))

	_, ruledOut := absent["InvalidateDeviceToken"]
	test.True(T, ruledOut, test.Sprint("InvalidateDeviceToken is not recorded as absent"))
}

// TestCreateNotificationIsAbsent names the one an author would add for the
// friendliest reason — a service ought to be able to tell somebody something —
// and the one the whole port of this package existed to make impossible.
func TestCreateNotificationIsAbsent(T *testing.T) {
	T.Parallel()

	test.SliceNotContains(T, rpcNames(), "CreateNotification", test.Sprint(
		"CreateNotification is an RPC; its write exists to commit inside the caller's transaction"))

	_, ruledOut := absent["CreateNotification"]
	test.True(T, ruledOut, test.Sprint("CreateNotification is not recorded as absent"))
}

// TestTheSurfaceIsNine pins the count the .proto's service comment argues for,
// so that a tenth arrives with a failing test naming the argument rather than as
// a diff nobody weighed against it.
func TestTheSurfaceIsNine(T *testing.T) {
	T.Parallel()

	test.SliceLen(T, 9, rpcNames())
	test.SliceLen(T, 12, storeMethods())
}

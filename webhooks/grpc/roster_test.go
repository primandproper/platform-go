package grpc_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/webhooks"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file is the roster of which store methods cross onto the wire, and it is
// the ruling rather than a description of it. webhooks.Store has eighteen
// methods and this service serves nine; the nine absences are each a decision,
// and a nineteenth method is in neither list until somebody says which.
//
// The mechanism is billing/grpc's, adopted rather than re-derived, and it is
// internal/sentinelmatrix's applied to methods instead of sentinels — for the
// same reason in all three places: the failure worth catching is not a wrong
// row, it is a method added later that nobody classified. On this surface that
// means either an RPC nobody meant to publish, or a write whose whole property
// is that it commits inside its caller's transaction.

// absent is every webhooks.Store method this service deliberately does not
// serve, with the reason recorded beside it so the roster is the argument and
// not the paperwork.
//
// Each reason is the short form of one the Store method itself carries; that is
// where the long version lives, because a reader of the Store should find the
// answer where they are standing.
var absent = map[string]string{
	// The delivery machinery, which webhooks.Store already groups under "The
	// delivery machinery takes neither". A claim has to commit before the request
	// goes out, and an RPC is a caller choosing when that commit happens; a lease
	// held open for the length of somebody else's transaction is a dispatch
	// nobody else may claim and this worker may never send.
	"Claim":         "the worker's lease, which commits before the request goes out",
	"MarkDelivered": "the worker reporting a request it has already made",
	"RecordFailure": "the worker releasing its own lease and scheduling the retry",
	"RecordAttempt": "the worker's log line, which must not roll back with somebody else's write",
	"Requeue":       "an operator asking the queue to move, not asking for a row that commits with theirs",
	"Backlog":       "the worker's own health gauges, across every scope",
	"Reap":          "the retention sweep, on a timer, answering no request",

	// The internal fan-out: the dispatcher asking itself who is subscribed on the
	// way to its own work.
	"EndpointsForEvent": "the dispatcher's own read; the query whose missing filter crosses tenants",

	// The one absence that is genuinely consumer-facing, and the reason this
	// roster is worth having. Enqueue writes a delivery and one dispatch per
	// endpoint in the caller's transaction so that both commit with whatever else
	// that transaction did. An RPC moves the write out of it, and what comes back
	// is a delivery for a row that rolled back or a committed row nobody was told
	// about.
	"Enqueue": "the write whose whole property is committing inside the caller's transaction",
}

// storeMethods is every method on webhooks.Store, read off the interface rather
// than listed, so a method added to it fails the two tests below.
func storeMethods() []string {
	t := reflect.TypeFor[webhooks.Store]()

	out := make([]string, 0, t.NumMethod())
	for method := range t.Methods() {
		out = append(out, method.Name)
	}

	return out
}

// rpcNames is every RPC on the service, by the bare method name the store method
// it serves also carries.
func rpcNames() []string {
	out := make([]string, 0, len(webhookspb.WebhooksService_ServiceDesc.Methods))
	for _, m := range webhookspb.WebhooksService_ServiceDesc.Methods {
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
	must.SliceNotEmpty(T, methods, must.Sprint("read no methods off webhooks.Store, so this asserted nothing"))

	served := rpcNames()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, ruledOut := absent[method]
			onTheWire := slices.Contains(served, method)

			test.True(t, ruledOut != onTheWire, test.Sprintf(
				"webhooks.Store.%s is %s; it has to be exactly one of served and ruled out",
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
			"the roster rules out webhooks.Store.%s, which webhooks.Store does not declare", method))
	}
}

// TestEnqueueIsAbsent names the one the lane's ruling for this package turned
// on, because a roster that only counted would let it be swapped for something
// else.
//
// It is the only method among the nine absences that is consumer-facing, so it
// is the one somebody would add in good faith.
func TestEnqueueIsAbsent(T *testing.T) {
	T.Parallel()

	test.SliceNotContains(T, rpcNames(), "Enqueue", test.Sprint(
		"Enqueue is an RPC; its write exists to commit inside the caller's transaction"))

	_, ruledOut := absent["Enqueue"]
	test.True(T, ruledOut, test.Sprint("Enqueue is not recorded as absent"))
}

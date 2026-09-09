package grpc_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/billingpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file is the roster of which store methods cross onto the wire, and it is
// the ruling rather than a description of it. billing.Store has thirty methods
// and this service serves eighteen; the twelve absences are each a decision, and
// a thirty-first method is in neither list until somebody says which.
//
// The mechanism is the one internal/sentinelmatrix uses on the sentinels, for
// the same reason: the failure worth catching is not a wrong row, it is a method
// added later that nobody classified — which on this surface means either an RPC
// nobody meant to publish or a write a callback needed and cannot reach.

// absent is every billing.Store method this service deliberately does not serve,
// with the reason recorded beside it so the roster is the argument and not the
// paperwork.
var absent = map[string]string{
	// The processor callback's four. Their caller is a Stripe or RevenueCat
	// receiver the consumer owns, writing inside the transaction that also holds
	// the audit entry naming who was billed and the outbox event somebody fans
	// out. An RPC moves the write out of the transaction that was the point.
	"SetSubscriptionStatus": "a callback's write, guarded, inside the consumer's transaction",
	"CompletePurchase":      "a callback's write, guarded on completed_at, inside the consumer's transaction",
	"SetTransactionStatus":  "a callback's write, guarded, inside the consumer's transaction",
	"RecordTransaction":     "a callback's write; the ledger row and its companions are one fact",

	// The same test one step earlier in the flow: the checkout handler and the
	// subscription sync are the consumer's own code, already holding a
	// transaction.
	"CreateSubscription": "the checkout flow's or a callback's, in its own transaction",
	"CreatePurchase":     "the checkout handler's, in the transaction that created the payment intent",
	"UpdateSubscription": "the sync write, whose caller holds the provider's event",

	// The lookups by a payment provider's identifier. The caller who holds one is
	// the callback that was handed it, and it already holds the store; billing/
	// grpc/doc.go carries the ruling.
	"GetProductByExternalID":      "a provider's identifier, held by the callback path",
	"GetSubscriptionByExternalID": "a provider's identifier, held by the callback path",
	"GetPurchaseByExternalID":     "a provider's identifier, held by the callback path",
	"GetTransactionByExternalID":  "a provider's identifier, held by the callback path",

	// The check a write makes, not a question a caller asks. A client wanting to
	// know whether a product is there reads it.
	"ProductExists": "the gate CreateSubscription and CreatePurchase run, not a caller's question",
}

// storeMethods is every method on billing.Store, read off the interface rather
// than listed, so a method added to it fails the two tests below.
func storeMethods() []string {
	t := reflect.TypeFor[billing.Store]()

	out := make([]string, 0, t.NumMethod())
	for method := range t.Methods() {
		out = append(out, method.Name)
	}

	return out
}

// rpcNames is every RPC on the service, by the bare method name the store method
// it serves also carries.
func rpcNames() []string {
	out := make([]string, 0, len(billingpb.BillingService_ServiceDesc.Methods))
	for _, m := range billingpb.BillingService_ServiceDesc.Methods {
		out = append(out, m.MethodName)
	}

	return out
}

// TestEveryStoreMethodIsEitherServedOrRuledOut is the roster's forward
// direction: a store method in neither list is a decision nobody made.
//
// The expensive direction to be wrong in is publishing something. A read added
// to the store and reflexively given an RPC is how a lookup by a provider's
// identifier, or a standing computed from a status, arrives on this surface
// without anybody arguing for it.
func TestEveryStoreMethodIsEitherServedOrRuledOut(T *testing.T) {
	T.Parallel()

	methods := storeMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("read no methods off billing.Store, so this asserted nothing"))

	served := rpcNames()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, ruledOut := absent[method]
			onTheWire := slices.Contains(served, method)

			test.True(t, ruledOut != onTheWire, test.Sprintf(
				"billing.Store.%s is %s; it has to be exactly one of served and ruled out",
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
			"the roster rules out billing.Store.%s, which billing.Store does not declare", method))
	}
}

// TestTheFourProcessorWritesAreAbsent names them, because they are the four the
// lane's ruling for this package turned on, and a roster that only counted would
// let one of them be swapped for something else.
func TestTheFourProcessorWritesAreAbsent(T *testing.T) {
	T.Parallel()

	served := rpcNames()

	for _, method := range []string{
		"SetSubscriptionStatus",
		"CompletePurchase",
		"SetTransactionStatus",
		"RecordTransaction",
	} {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			test.SliceNotContains(t, served, method, test.Sprintf(
				"%s is an RPC; its caller is a processor callback already inside a transaction", method))

			_, ruledOut := absent[method]
			test.True(t, ruledOut, test.Sprintf("%s is not recorded as absent", method))
		})
	}
}

// TestNothingOnTheWireComputesAStanding is the absence billing/doc.go is most
// insistent about, checked against the generated messages rather than against
// anybody's memory of the schema.
//
// No RPC and no field may say whether an account is entitled. That reading is
// policy, it differs between deployments selling the same thing, and the seam it
// belongs in is billing/plans. A field named is_active here would be that policy
// on this module's release cadence.
func TestNothingOnTheWireComputesAStanding(T *testing.T) {
	T.Parallel()

	forbidden := []string{
		"active", "entitled", "standing", "good_standing", "in_good_standing",
		"is_active", "entitlement", "delinquent", "past_due",
	}

	files := billingpb.File_primandproper_platform_billing_v1_billing_proto
	messages := files.Messages()

	must.Positive(T, messages.Len(), must.Sprint("read no messages off the file descriptor"))

	for i := range messages.Len() {
		message := messages.Get(i)
		fields := message.Fields()

		for j := range fields.Len() {
			name := string(fields.Get(j).Name())

			test.SliceNotContains(T, forbidden, name, test.Sprintf(
				"%s.%s reads as a computed standing, which this schema does not carry",
				message.Name(), name))
		}
	}

	for _, method := range rpcNames() {
		test.StrNotContainsFold(T, method, "standing", test.Sprintf(
			"%s reads as a standing RPC, which this service does not serve", method))
		test.StrNotContainsFold(T, method, "entitle", test.Sprintf(
			"%s reads as an entitlement RPC, which this service does not serve", method))
	}
}

// TestEveryMessageReservesScope is the other absence, and it is enforced by the
// schema rather than described by a comment.
//
// A comment saying a request must not name a scope is a comment; `reserved
// "scope"` makes it something protoc refuses, here and in a consumer's fork of
// the file. audit.proto established the pattern for this lane and every message
// in billing.proto carries it — the four entities as well as the requests, since
// a response that told a client its tenant would be answering with something the
// client supplied.
//
// It is asserted off the file descriptor rather than off the generated structs,
// because a reservation leaves no struct field behind to look at.
func TestEveryMessageReservesScope(T *testing.T) {
	T.Parallel()

	messages := billingpb.File_primandproper_platform_billing_v1_billing_proto.Messages()
	must.Positive(T, messages.Len(), must.Sprint("read no messages off the file descriptor"))

	for i := range messages.Len() {
		message := messages.Get(i)

		T.Run(string(message.Name()), func(t *testing.T) {
			t.Parallel()

			reserved := message.ReservedNames()

			var found bool
			for j := range reserved.Len() {
				if reserved.Get(j) == "scope" {
					found = true

					break
				}
			}

			test.True(t, found, test.Sprintf(
				`%s does not reserve "scope", so a field named one can be added to it`, message.Name()))

			// And nothing scope-shaped got in under another spelling.
			fields := message.Fields()
			for j := range fields.Len() {
				test.StrNotContainsFold(t, string(fields.Get(j).Name()), "scope", test.Sprintf(
					"%s carries a scope field, which comes off the principal", message.Name()))
			}
		})
	}
}

// TestEveryRPCIsExercised keeps the whole-surface suites in server_test.go
// honest. They assert their property about eighteen calls written out by hand,
// and a nineteenth RPC would otherwise be exempt from both.
func TestEveryRPCIsExercised(T *testing.T) {
	T.Parallel()

	exercised := everyRPC()

	for _, method := range rpcNames() {
		_, ok := exercised[method]
		test.True(T, ok, test.Sprintf(
			"%s is not in everyRPC, so the anonymous-caller and broken-store suites skip it", method))
	}

	test.MapLen(T, len(rpcNames()), exercised, test.Sprint(
		"everyRPC names a call the service descriptor does not declare"))
}

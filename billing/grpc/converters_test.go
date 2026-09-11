package grpc_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/billingpb"
	billinggrpc "github.com/primandproper/platform-go/v14/billing/grpc"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestANilValueRendersAsNil is what a converter composing a larger response
// needs: an absent row is an unset field rather than an empty message somebody
// renders as a product with no name.
func TestANilValueRendersAsNil(T *testing.T) {
	T.Parallel()

	test.Nil(T, billinggrpc.ProductToProto(nil))
	test.Nil(T, billinggrpc.SubscriptionToProto(nil))
	test.Nil(T, billinggrpc.PurchaseToProto(nil))
	test.Nil(T, billinggrpc.TransactionToProto(nil))
}

// TestAnEmptyPageRendersAsAnEmptySlice keeps a page of nothing from arriving as a
// null a client has to branch on.
func TestAnEmptyPageRendersAsAnEmptySlice(T *testing.T) {
	T.Parallel()

	test.SliceEmpty(T, billinggrpc.ProductsToProto(nil))
	test.SliceEmpty(T, billinggrpc.SubscriptionsToProto(nil))
	test.SliceEmpty(T, billinggrpc.PurchasesToProto(nil))
	test.SliceEmpty(T, billinggrpc.TransactionsToProto(nil))
}

// TestTheNullableTimesStayUnset is the rule that runs through every converter
// here. A client rendering "last updated" wants to know there was no update, and
// 1970 is not that answer.
func TestTheNullableTimesStayUnset(T *testing.T) {
	T.Parallel()

	created := time.Now().UTC()

	product := billinggrpc.ProductToProto(&billing.Product{CreatedAt: created})
	must.NotNil(T, product.GetCreatedAt())
	test.Nil(T, product.GetLastUpdatedAt())
	test.Nil(T, product.GetArchivedAt())

	purchase := billinggrpc.PurchaseToProto(&billing.Purchase{CreatedAt: created})
	test.Nil(T, purchase.GetCompletedAt())
	test.Nil(T, purchase.GetLastUpdatedAt())
	test.Nil(T, purchase.GetArchivedAt())

	transaction := billinggrpc.TransactionToProto(&billing.Transaction{CreatedAt: created})
	test.Nil(T, transaction.GetLastUpdatedAt())
	test.Nil(T, transaction.GetArchivedAt())

	subscription := billinggrpc.SubscriptionToProto(&billing.Subscription{CreatedAt: created})
	test.Nil(T, subscription.GetLastUpdatedAt())
	test.Nil(T, subscription.GetArchivedAt())
}

// TestTheNullableTimesCrossWhenSet is the other half, since an unset field that
// is always unset would pass the test above.
func TestTheNullableTimesCrossWhenSet(T *testing.T) {
	T.Parallel()

	at := time.Now().UTC().Truncate(time.Second)

	product := billinggrpc.ProductToProto(&billing.Product{LastUpdatedAt: &at, ArchivedAt: &at})
	test.EqOp(T, at, product.GetLastUpdatedAt().AsTime().UTC())
	test.EqOp(T, at, product.GetArchivedAt().AsTime().UTC())

	purchase := billinggrpc.PurchaseToProto(&billing.Purchase{CompletedAt: &at})
	test.EqOp(T, at, purchase.GetCompletedAt().AsTime().UTC())
}

// TestNoConverterRendersAScope is the schema's absence held at the conversion
// boundary as well as in the .proto: a value carrying one is rendered without it,
// so a message can never learn a tenant from a row.
func TestNoConverterRendersAScope(T *testing.T) {
	T.Parallel()

	scope := tenancy.Of("tenant_1")

	// The compile-time half is that there is no field to assign; what this
	// asserts is that nothing was smuggled into one of the string fields
	// instead.
	product := billinggrpc.ProductToProto(&billing.Product{ID: "p", Name: "a thing", Scope: scope})
	test.StrNotContains(T, product.String(), scope.String())

	subscription := billinggrpc.SubscriptionToProto(&billing.Subscription{ID: "s", Scope: scope})
	test.StrNotContains(T, subscription.String(), scope.String())

	purchase := billinggrpc.PurchaseToProto(&billing.Purchase{ID: "u", Scope: scope})
	test.StrNotContains(T, purchase.String(), scope.String())

	transaction := billinggrpc.TransactionToProto(&billing.Transaction{ID: "t", Scope: scope})
	test.StrNotContains(T, transaction.String(), scope.String())
}

// TestEveryCapitalismStatusCrossesVerbatim is the ruling billing.proto records,
// asserted against capitalism's own constants.
//
// The wire value is the string capitalism defines, so a status added to that set
// crosses without this module being rebuilt. A generated enum would have rendered
// it as UNSPECIFIED — a real stored row arriving at a client as "we don't know" —
// which is exactly the failure the string avoids.
func TestEveryCapitalismStatusCrossesVerbatim(T *testing.T) {
	T.Parallel()

	for _, status := range []capitalism.SubscriptionStatus{
		capitalism.SubscriptionStatusIncomplete,
		capitalism.SubscriptionStatusIncompleteExpired,
		capitalism.SubscriptionStatusTrialing,
		capitalism.SubscriptionStatusActive,
		capitalism.SubscriptionStatusPastDue,
		capitalism.SubscriptionStatusCanceled,
		capitalism.SubscriptionStatusUnpaid,
		capitalism.SubscriptionStatusPaused,
	} {
		T.Run(status.String(), func(t *testing.T) {
			t.Parallel()

			must.True(t, status.Known(), must.Sprint("capitalism no longer recognizes this status"))

			rendered := billinggrpc.SubscriptionToProto(&billing.Subscription{Status: status})
			test.EqOp(t, status.String(), rendered.GetStatus())
		})
	}
}

// TestAStoredStatusIsNeverEmptyOnTheWire is the property the .proto promises a
// client: the empty string is capitalism's unknown, which billing refuses to
// store, so a subscription that came from the store always names a standing.
//
// The converter is not what enforces it — the store's validation is — and this
// pins the two agreeing.
func TestAStoredStatusIsNeverEmptyOnTheWire(T *testing.T) {
	T.Parallel()

	h := newHarness(T)
	h.seedSubscription(T, testScope, testAccount)

	res, err := h.server.ListSubscriptionsForAccount(h.ctx(T, testUser, testAccount),
		&billingpb.ListSubscriptionsForAccountRequest{AccountId: testAccount})
	must.NoError(T, err)
	must.SliceLen(T, 1, res.GetResults())

	test.NotEq(T, "", res.GetResults()[0].GetStatus())
	test.NotEq(T, capitalism.SubscriptionStatusUnknown.String(), res.GetResults()[0].GetStatus())
}

// TestTheTwoClosedSetsCrossAsEnums is the other side of the ruling: the kind and
// the ledger status are sets billing defines and validates, so a value outside
// them is a row the store would have refused, and the generated constant is
// exactly as complete as the column.
func TestTheTwoClosedSetsCrossAsEnums(T *testing.T) {
	T.Parallel()

	T.Run("product kind", func(t *testing.T) {
		t.Parallel()

		for kind, expected := range map[billing.Kind]billingpb.ProductKind{
			billing.KindRecurring: billingpb.ProductKind_PRODUCT_KIND_RECURRING,
			billing.KindOneTime:   billingpb.ProductKind_PRODUCT_KIND_ONE_TIME,
		} {
			must.True(t, kind.Valid(), must.Sprintf("billing no longer recognizes %q", kind))
			test.EqOp(t, expected, billinggrpc.ProductToProto(&billing.Product{Kind: kind}).GetKind())
		}
	})

	T.Run("transaction status", func(t *testing.T) {
		t.Parallel()

		for status, expected := range map[billing.TransactionStatus]billingpb.TransactionStatus{
			billing.TransactionPending:   billingpb.TransactionStatus_TRANSACTION_STATUS_PENDING,
			billing.TransactionSucceeded: billingpb.TransactionStatus_TRANSACTION_STATUS_SUCCEEDED,
			billing.TransactionFailed:    billingpb.TransactionStatus_TRANSACTION_STATUS_FAILED,
			billing.TransactionRefunded:  billingpb.TransactionStatus_TRANSACTION_STATUS_REFUNDED,
		} {
			must.True(t, status.Valid(), must.Sprintf("billing no longer recognizes %q", status))
			test.EqOp(t, expected,
				billinggrpc.TransactionToProto(&billing.Transaction{Status: status}).GetStatus())
		}
	})

	T.Run("an unrecognized value is unspecified rather than a panic", func(t *testing.T) {
		t.Parallel()

		// Unreachable from a stored row, and reachable from a caller who
		// constructed one by hand. Rendering it as unspecified is the honest
		// answer; guessing would put a value on the wire nothing wrote.
		test.EqOp(t, billingpb.ProductKind_PRODUCT_KIND_UNSPECIFIED,
			billinggrpc.ProductToProto(&billing.Product{Kind: billing.Kind("subscription-ish")}).GetKind())
		test.EqOp(t, billingpb.TransactionStatus_TRANSACTION_STATUS_UNSPECIFIED,
			billinggrpc.TransactionToProto(&billing.Transaction{
				Status: billing.TransactionStatus("chargeback"),
			}).GetStatus())
	})
}

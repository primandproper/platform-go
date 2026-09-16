package standing

import (
	"testing"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/capitalism"

	"github.com/shoenig/test"
)

// known is capitalism's closed set, spelled out here so that a status added to
// that module shows up as a failure in this package rather than as a default
// arm nobody visited. TestClassify_CoversEveryKnownStatus is what pins it.
var known = []capitalism.SubscriptionStatus{
	capitalism.SubscriptionStatusIncomplete,
	capitalism.SubscriptionStatusIncompleteExpired,
	capitalism.SubscriptionStatusTrialing,
	capitalism.SubscriptionStatusActive,
	capitalism.SubscriptionStatusPastDue,
	capitalism.SubscriptionStatusCanceled,
	capitalism.SubscriptionStatusUnpaid,
	capitalism.SubscriptionStatusPaused,
}

func TestStrict(T *testing.T) {
	T.Parallel()

	T.Run("an active subscription is paid", func(t *testing.T) {
		t.Parallel()

		status, ok := Strict(capitalism.SubscriptionStatusActive)
		test.True(t, ok)
		test.EqOp(t, identity.BillingPaid, status)
	})

	T.Run("a trialing subscription is a trial", func(t *testing.T) {
		t.Parallel()

		// Not paid: identity keeps the two apart so an application gating on
		// "has paid us something" can read the difference.
		status, ok := Strict(capitalism.SubscriptionStatusTrialing)
		test.True(t, ok)
		test.EqOp(t, identity.BillingTrial, status)
	})

	T.Run("the other six are unpaid", func(t *testing.T) {
		t.Parallel()

		// past_due, unpaid and paused are the three a deployment wanting a
		// dunning window overrides; this is the strict reading, so none of them
		// keeps the account paid.
		for _, reported := range []capitalism.SubscriptionStatus{
			capitalism.SubscriptionStatusIncomplete,
			capitalism.SubscriptionStatusIncompleteExpired,
			capitalism.SubscriptionStatusPastDue,
			capitalism.SubscriptionStatusCanceled,
			capitalism.SubscriptionStatusUnpaid,
			capitalism.SubscriptionStatusPaused,
		} {
			status, ok := Strict(reported)
			test.True(t, ok, test.Sprintf("status %q", reported))
			test.EqOp(t, identity.BillingUnpaid, status, test.Sprintf("status %q", reported))
		}
	})

	T.Run("refuses an unknown status", func(t *testing.T) {
		t.Parallel()

		// The zero value, which is what an adapter reports when it could not
		// place what the provider said.
		status, ok := Strict(capitalism.SubscriptionStatusUnknown)
		test.False(t, ok)
		test.EqOp(t, identity.BillingStatus(""), status)
	})

	T.Run("refuses a status capitalism does not know", func(t *testing.T) {
		t.Parallel()

		// The ninth status, the provider's own spelling, and a case difference
		// each take the refusing path rather than the unpaid one — which is
		// what keeps a processor's new word for "fine" from locking a paying
		// customer out.
		for _, reported := range []capitalism.SubscriptionStatus{
			"gone_fishing",
			"cancelled",
			"ACTIVE",
		} {
			status, ok := Strict(reported)
			test.False(t, ok, test.Sprintf("status %q", reported))
			test.EqOp(t, identity.BillingStatus(""), status, test.Sprintf("status %q", reported))
		}
	})

	T.Run("never reports a suspension", func(t *testing.T) {
		t.Parallel()

		// No processor reports one, so no delivery may produce one — otherwise
		// a webhook undoes an operator's suspension.
		for _, reported := range known {
			status, ok := Strict(reported)
			requirePlaced(t, ok, reported)
			test.NotEqOp(t, identity.BillingSuspended, status, test.Sprintf("status %q", reported))
		}
	})

	T.Run("only ever reports a status identity stores", func(t *testing.T) {
		t.Parallel()

		for _, reported := range known {
			status, ok := Strict(reported)
			requirePlaced(t, ok, reported)
			test.True(t, status.Valid(), test.Sprintf("status %q", reported))
		}
	})
}

func TestClassify_CoversEveryKnownStatus(T *testing.T) {
	T.Parallel()

	T.Run("every status capitalism knows is placed", func(t *testing.T) {
		t.Parallel()

		for _, reported := range known {
			test.True(t, reported.Known(), test.Sprintf("status %q", reported))

			_, ok := Strict(reported)
			test.True(t, ok, test.Sprintf("status %q", reported))
		}
	})

	T.Run("every status Strict places is one capitalism knows", func(t *testing.T) {
		t.Parallel()

		// The other direction: a status this package placed but capitalism does
		// not recognize would be a mapping onto a word no adapter emits.
		for _, reported := range known {
			if _, ok := Strict(reported); ok {
				test.True(t, reported.Known(), test.Sprintf("status %q", reported))
			}
		}
	})
}

func TestEnded(T *testing.T) {
	T.Parallel()

	T.Run("a cancelled subscription has ended", func(t *testing.T) {
		t.Parallel()

		test.True(t, Ended(capitalism.SubscriptionStatusCanceled))
	})

	T.Run("a first payment that never succeeded has ended", func(t *testing.T) {
		t.Parallel()

		// Terminal per capitalism: nothing was collected and nothing will be.
		test.True(t, Ended(capitalism.SubscriptionStatusIncompleteExpired))
	})

	T.Run("the statuses the processor is still holding open have not", func(t *testing.T) {
		t.Parallel()

		// unpaid and paused are the two worth naming: the processor has stopped
		// collecting on both and ended neither, and an account whose plan was
		// cleared on a pause has nothing to resume onto.
		for _, reported := range []capitalism.SubscriptionStatus{
			capitalism.SubscriptionStatusIncomplete,
			capitalism.SubscriptionStatusTrialing,
			capitalism.SubscriptionStatusActive,
			capitalism.SubscriptionStatusPastDue,
			capitalism.SubscriptionStatusUnpaid,
			capitalism.SubscriptionStatusPaused,
		} {
			test.False(t, Ended(reported), test.Sprintf("status %q", reported))
		}
	})

	T.Run("a status nobody placed has not ended", func(t *testing.T) {
		t.Parallel()

		// Only meaningful to a caller that ignored Classify reporting false.
		// The point is that it does not read as a cancellation.
		test.False(t, Ended(capitalism.SubscriptionStatusUnknown))
		test.False(t, Ended("gone_fishing"))
	})

	T.Run("an ended status still classifies", func(t *testing.T) {
		t.Parallel()

		// The two seams answer independently: a handler reads the standing off
		// Classify and the method to call off Ended, so a terminal status has
		// to have both.
		for _, reported := range []capitalism.SubscriptionStatus{
			capitalism.SubscriptionStatusCanceled,
			capitalism.SubscriptionStatusIncompleteExpired,
		} {
			status, ok := Strict(reported)
			test.True(t, ok, test.Sprintf("status %q", reported))
			test.EqOp(t, identity.BillingUnpaid, status, test.Sprintf("status %q", reported))
		}
	})
}

// requirePlaced fails the test outright when a status the table says is known
// was not placed, so the assertions after it are not reading a zero value.
func requirePlaced(t *testing.T, ok bool, reported capitalism.SubscriptionStatus) {
	t.Helper()

	if !ok {
		t.Fatalf("status %q was not placed", reported)
	}
}

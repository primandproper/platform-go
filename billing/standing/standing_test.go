package standing

import (
	"testing"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/capitalism"

	"github.com/shoenig/test"
)

// known is this package's copy of capitalism's closed set, and what it catches
// is worth being exact about, because the two directions are not symmetrical.
//
// A status capitalism removes or renames fails here at compile time, which is
// the whole of what a spelled-out list can do: capitalism exports Known as a
// predicate and no enumeration, so nothing in a test can ask it for its set,
// and a ninth status added there will not red anything in this package.
//
// That asymmetry is survivable only because of how Strict is written. The
// unplaced status takes the refusing path rather than an arm somebody guessed
// at, so the cost of this list going stale is a standing this package declines
// to rule on — which is the behavior TestStrict pins under "refuses a status
// capitalism does not know", and the reason there is no default arm to sweep
// one up.
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

// TestClassify_CoversEveryKnownStatus is the one direction a list can check:
// every status capitalism documents is one Strict places.
//
// The converse — that Strict places nothing capitalism does not know — is not
// checkable by walking this same list, which would only ask whether the entries
// in it are in it. What stands in for it is TestStrict's refusal cases, which
// are the statuses Strict is handed that are deliberately absent here.
//
// The Known call is not redundant with the compile. It is what says the list is
// capitalism's closed set rather than eight strings that happen to be spelled
// like it, so a constant that survives a rename with its value changed reds
// here rather than silently becoming a ninth status nothing places.
func TestClassify_CoversEveryKnownStatus(T *testing.T) {
	T.Parallel()

	for _, reported := range known {
		test.True(T, reported.Known(), test.Sprintf("status %q", reported))

		_, ok := Strict(reported)
		test.True(T, ok, test.Sprintf("status %q", reported))
	}
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

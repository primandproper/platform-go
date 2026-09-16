/*
Package standing maps what a payment processor reported onto the standing an
account is stored with.

[github.com/primandproper/primitives-go/v2/capitalism] hands a webhook handler a
[capitalism.SubscriptionStatus], and
[github.com/primandproper/platform-go/v14/identity] stores an
[identity.BillingStatus]. Nothing joined the two, so every consumer wiring a
processor callback wrote the join by hand — eight statuses onto four, in a
switch next to its handler, which is the shape capitalism's own documentation
describes as the thing a closed vocabulary exists to make possible and does not
itself supply. Measured in the consumer this package was written against it was
twenty lines, and getting one arm of it wrong leaves an account paid after it
lapsed or locked out while the processor considers it current, until somebody
notices in support.

# What is here and what is yours

Two answers, and they are separated because only one of them is a judgement.

[Ended] is the fact: canceled and incomplete_expired are the two statuses that
mean there is no subscription any more, and capitalism says so about both. It is
here rather than folded into the judgement because it decides which
[identity.BillingWriter] method a handler calls — RecordAccountSubscription or
RecordAccountSubscriptionEnded — and a deployment that writes its own reading of
the standings should not have to restate which statuses are terminal in order to
get one.

[Classify] is the judgement, and it is the same seam plans.Choose already is:
whether a trial is paid, whether past_due keeps working through the dunning
window, whether a paused subscription still shows the data it bought. capitalism
is where the ruling that this is policy lives; this is where a deployment writes
its answer down once, instead of once per handler.

[Strict] is the answer most deployments want and none of them have to take:
active is paid, trialing is a trial, and the other six leave the account unpaid.
It is the same two statuses [github.com/primandproper/platform-go/v14/billing/plans.Entitled]
accepts, for the same reason — they are the two every reading agrees on.

# Why nothing here returns a suspension

[identity.BillingSuspended] is an operator's move and no processor reports it,
so no mapping from a reported status can produce it. A [Classify] that returned
one would be a delivery undoing a suspension the operator put there, which is
the one transition identity keeps on its own method
(identity.Store.SetAccountBillingStatus) precisely so that a webhook cannot make
it.

# Why an unrecognized status is refused rather than assumed

Both functions treat capitalism's eight statuses as the closed set they are, and
[Classify] reports false for anything else — the unknown status an adapter could
not place, and the ninth status a processor adds after this module was built. A
default arm that swept those into unpaid would lock out a paying customer the
week their provider shipped a new word for "fine", and it would do it silently.
A handler that sees false should log
capitalism.SubscriptionState.ProviderStatus, which carries what actually
arrived, and leave the account where it is: the fix is one entry in an adapter's
table, and until it lands the stored standing is stale rather than wrong.

# Why this is a package rather than a function in identity

identity would otherwise import capitalism, which would put a payments
dependency in front of every consumer that stores a user. It sits beside
[github.com/primandproper/platform-go/v14/billing/plans] because that is the
other place a deployment's payments judgement is written down, and because a
handler recording a delivery is already holding both.
*/
package standing

import (
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/capitalism"
)

// Classify decides what a reported subscription status means for an account's
// standing.
//
// Reporting false is how this says the status is not one it can place, which a
// handler should treat as "leave the account alone" rather than as any standing
// — see the package documentation. A Classify never returns
// [identity.BillingSuspended].
//
// It is a named type rather than a bare signature so that a deployment's own
// reading has something to be declared as, next to the plans.Choose it is
// almost certainly written beside.
type Classify func(status capitalism.SubscriptionStatus) (identity.BillingStatus, bool)

// Strict is the reading most deployments want: active is paid, trialing is a
// trial, and every other reported status leaves the account unpaid.
//
// It grants no grace. past_due, unpaid and paused are the three deployments
// genuinely differ about — a dunning window that keeps a customer working while
// the processor retries is a legitimate rule and a common one — and a
// deployment that wants any of them writes its own [Classify] rather than
// configuring this one. incomplete and incomplete_expired are a first payment
// that has not succeeded and one that never will, which no reading treats as
// paid.
//
// It is a function here rather than the behavior a caller gets by default,
// because passing it should be a deployment saying "yes, that is our rule"
// rather than a deployment not having thought about it. That is the shape
// [github.com/primandproper/platform-go/v14/billing/plans.Entitled] already
// takes.
func Strict(status capitalism.SubscriptionStatus) (identity.BillingStatus, bool) {
	switch status {
	case capitalism.SubscriptionStatusActive:
		return identity.BillingPaid, true
	case capitalism.SubscriptionStatusTrialing:
		return identity.BillingTrial, true
	// The remaining six are enumerated rather than swept up by the default
	// arm, so that a status capitalism adds later is unplaced here until
	// somebody rules on it rather than silently unpaid. It is the branch
	// capitalism's own SubscriptionStatus.Known documentation asks a caller to
	// write.
	case capitalism.SubscriptionStatusIncomplete,
		capitalism.SubscriptionStatusIncompleteExpired,
		capitalism.SubscriptionStatusPastDue,
		capitalism.SubscriptionStatusCanceled,
		capitalism.SubscriptionStatusUnpaid,
		capitalism.SubscriptionStatusPaused:
		return identity.BillingUnpaid, true
	default:
		return "", false
	}
}

// Strict satisfies the seam, which is the whole of what a deployment passing it
// somewhere relies on.
var _ Classify = Strict

// Ended reports whether a reported status means the subscription is over.
//
// It is the two capitalism documents as terminal — a subscription the customer
// cancelled or the processor gave up collecting on, and a first payment that
// never succeeded inside the processor's window — and it is what tells a
// handler which of identity's two delivery writes to make:
// RecordAccountSubscriptionEnded for these, RecordAccountSubscription for the
// rest. Getting that wrong is the difference between an account left on a plan
// it stopped paying for and one whose plan vanished while it was still paying.
//
// unpaid and paused are deliberately not ended. The processor has stopped
// collecting on both and has ended neither, and an account whose plan was
// cleared on a pause has nothing to resume onto.
//
// A status this package cannot place is not ended either, which is only
// meaningful to a caller that ignored [Classify] reporting false. Branch on
// that first; there is nothing to write for a status nobody has ruled on.
func Ended(status capitalism.SubscriptionStatus) bool {
	switch status {
	case capitalism.SubscriptionStatusCanceled, capitalism.SubscriptionStatusIncompleteExpired:
		return true
	default:
		return false
	}
}

/*
Package plans answers entitlements' plan question and metering's billing-period
question from the billing store.

[github.com/primandproper/platform-go/v14/entitlements] names PlanSource as the
one seam it cannot fill, on the grounds that the join between an account and a
purchased plan is application data. It is — and it is application data this
module now owns a table for, which is what this package is: the read, plus the
one decision that genuinely stays the consumer's.

[github.com/primandproper/platform-go/v14/metering] refuses PeriodBillingPeriod
without a resolver for exactly the same reason, and the same row answers it:
Subscription.CurrentPeriodStart and CurrentPeriodEnd are the window the provider
says is paid for, which is the window an invoice will be drawn against.

# What is here and what is yours

The read is [Source.PlanFor] and [PeriodResolver.Resolve]: the account's
subscriptions whose paid period covers the store's clock, which is one indexed
query and exactly what billing.SubscriptionStore.ListCurrentSubscriptions emits.
Both readers make it, which is why one function makes it for both.

The decision is [Choose], and it is a constructor argument rather than an option
with a default because there is no default that is right twice. Which of
capitalism's statuses leaves an account entitled is policy — whether a trial may
write, whether past_due keeps working through the dunning window, whether a
paused subscription still shows the data it bought — and it differs between
deployments selling the same thing. capitalism's documentation is where that
ruling lives; this is where a deployment writes its answer down once.

[Entitled] is the answer most deployments want and none of them have to take:
active or trialing, first current subscription wins, its product id as the plan.

The period decision has the same shape and is a second argument rather than the
same one. [Cycle] picks which of the account's current subscriptions a shared
meter's usage is billed against, and [Sole] is the reading most deployments
want: the one covering window, and a refusal where two of them disagree. It is
separate from [Choose] because the statuses that entitle an account are not the
statuses that draw its invoice — a past_due subscription mid-dunning is one many
deployments stop entitling and none stop billing.

A deployment metering but not gating builds only [PeriodResolver]; one gating
but not metering builds only [Source]. Neither pays for the other.

# Why the plan is a product id

entitlements identifies a plan by a string that its catalog also names. A product
id is a string this schema already has and already guarantees is unique, so a
deployment whose catalog keys match its product ids needs no mapping at all. One
whose catalog keys are words like "pro" writes the mapping inside its own Choose,
which is a switch over a closed set it controls rather than a column this package
would have had to add to a table for one consumer's naming.

# Where the executor comes from

Every read in billing runs on an executor its caller supplies, and
entitlements.PlanSource.PlanFor takes an account and nothing else — there is
nowhere in that signature to put one. So [New] takes it once, at construction,
and every plan lookup runs on it.

It is a database.SQLQueryExecutor rather than a database.Client because a plan
source reads and does nothing else, and the narrower type is also the one that
lets a consumer hand it a Tx where an entitlement check genuinely has to see a
subscription that transaction has written and not yet committed. Client.Reader()
is what a deployment ordinarily passes. It is the shape comments/privacy took
first.

# Why this is a package rather than a method on the store

billing would otherwise import entitlements, and a service selling one thing with
no feature gating would compile the checker, the quota reader and the catalog to
get a subscriptions table. It is the same reason comments/privacy is a package,
and it costs one constructor argument.
*/
package plans

import (
	"context"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/entitlements"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil billing.SubscriptionStore.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil subscription store")

	// ErrNilChoose indicates a nil Choose. It is required — see the package
	// documentation for why there is no default.
	ErrNilChoose = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil plan chooser")

	// ErrNilExecutor indicates a nil executor. The reads run on one somebody
	// else supplies, because billing keeps no connection of its own to fall
	// back to.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil billing plans query executor")
)

// pageSize is how many of an account's current subscriptions the two readers
// over this table are shown: Choose, and Cycle.
//
// An account holds one, or a small handful while an upgrade settles. The bound
// is here so that a deployment whose data has gone wrong — a sync loop that
// opened a thousand agreements — answers an entitlement check slowly rather than
// dragging the lot across the wire on a request path.
//
// The two readers want different things from it when it bites, which is why
// currentSubscriptions reports that it did rather than just honoring it. Choose
// picks from what it is shown and asks nothing of the rest, so a page that
// stopped early still answers correctly for every reading this package ships.
// Cycle is a question about the whole set — Sole answers only when nothing
// disagrees with the one it picked — and a disagreement that fell off the end of
// the page is one it would answer confidently and wrongly.
const pageSize = 25

// Choose picks the plan an account is on from its current subscriptions.
//
// The slice holds every subscription whose paid period covers now, in the order
// the store paged them: oldest first, which is the order they were opened in. It
// is never nil and may be empty, and reporting false is how this says the account
// is on no plan — which reaches entitlements as ErrNoPlan, an answer rather than
// a failure.
//
// It is where every judgement lives. See [Entitled] for the reading most
// deployments want.
type Choose func(subscriptions []*billing.Subscription) (string, bool)

// Entitled is the plan chooser most deployments want: the first current
// subscription whose status is active or trialing, identified by its product id.
//
// It is a function in this package rather than the default behavior of the
// constructor, because taking it should be a deployment saying "yes, that is our
// rule" rather than a deployment not having thought about it. The two statuses
// it accepts are the two every reading agrees on; past_due, paused and unpaid are
// the ones deployments genuinely differ about, and a deployment that wants any of
// them writes its own Choose.
func Entitled(subscriptions []*billing.Subscription) (string, bool) {
	for _, subscription := range subscriptions {
		switch subscription.Status {
		case capitalism.SubscriptionStatusActive, capitalism.SubscriptionStatusTrialing:
			return subscription.ProductID, true
		default:
		}
	}

	return "", false
}

var _ entitlements.PlanSource = (*Source)(nil)

// Source is an entitlements.PlanSource backed by a billing store.
//
// It is exported, and returned by New, so a caller can depend on the source it
// built rather than on the seam every source shares.
type Source struct {
	store  billing.SubscriptionStore
	reader database.SQLQueryExecutor
	choose Choose
	scope  tenancy.Scope
}

// New builds a PlanSource reading one scope's subscriptions, over the executor
// its reads run on.
//
// The scope and the executor are both fixed at construction because
// entitlements.PlanSource.PlanFor takes an account and nothing else. A
// deployment serving several scopes builds one Source per scope, alongside the
// entitlements checker that reads it — which is the same shape a per-scope
// catalog already has.
func New(
	store billing.SubscriptionStore,
	reader database.SQLQueryExecutor,
	scope tenancy.Scope,
	choose Choose,
) (*Source, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if reader == nil {
		return nil, ErrNilExecutor
	}

	if choose == nil {
		return nil, ErrNilChoose
	}

	if err := scope.Validate(); err != nil {
		return nil, err
	}

	return &Source{store: store, reader: reader, scope: scope, choose: choose}, nil
}

// currentSubscriptions reads one page of an account's subscriptions whose paid
// period covers the store's clock, and reports whether there were more than the
// page could hold.
//
// It is one function rather than a line in each of the two readers over it,
// because the page size and the filter that carries it are a bound on how much
// of an account's billing history reaches a request path — and a second copy of
// a bound is a bound that drifts from the first with nothing to say so.
//
// It asks for one more row than it will return, which is what lets it tell a
// page that happens to be full from a page that was cut short. The truncation is
// returned rather than handled here because the two readers owe it different
// answers — see pageSize — and a function that decided for both would be making
// the wrong one for one of them.
func currentSubscriptions(
	ctx context.Context,
	store billing.SubscriptionStore,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	account string,
) (subscriptions []*billing.Subscription, truncated bool, err error) {
	// One past the bound: a page holding exactly pageSize rows is
	// indistinguishable from one the bound cut, and the distinction is the whole
	// point.
	limit := uint16(pageSize + 1)

	page, err := store.ListCurrentSubscriptions(ctx, q, scope, account,
		&filtering.QueryFilter{MaxResponseSize: &limit})
	if err != nil {
		return nil, false, err
	}

	if len(page.Data) > pageSize {
		return page.Data[:pageSize], true, nil
	}

	return page.Data, false, nil
}

// PlanFor implements entitlements.PlanSource.
//
// An account with no current subscription, or one whose current subscriptions
// Choose declines, returns an error wrapping entitlements.ErrNoPlan — which that
// package reads as "this account has no plan" rather than as a failure, and
// answers from CheckerConfig.FallbackPlan.
func (s *Source) PlanFor(ctx context.Context, account string) (string, error) {
	// The truncation is deliberately not consulted. Choose picks from what it is
	// shown and asks nothing of the rest, so a page that stopped early still
	// answers correctly for every reading this package ships — and an account
	// whose data has gone wrong is one this should answer slowly rather than
	// refuse to entitle at all.
	subscriptions, _, err := currentSubscriptions(ctx, s.store, s.reader, s.scope, account)
	if err != nil {
		return "", platformerrors.Wrapf(err, "reading current subscriptions for account %q", account)
	}

	plan, ok := s.choose(subscriptions)
	if !ok {
		return "", platformerrors.Wrapf(entitlements.ErrNoPlan, "account %q", account)
	}

	return plan, nil
}

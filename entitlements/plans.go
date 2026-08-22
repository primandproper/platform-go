package entitlements

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// PlanSource answers which plan an account is on.
//
// This is the one seam this package cannot fill, and the reason is worth
// stating: the join between an internal account ID and a purchased plan is
// application data. It is written by the handler that processes the billing
// provider's subscription webhook, it is corrected by hand when somebody is
// grandfathered, and it lives in a column next to the account. A library that
// guessed it would gate one customer's features on another's subscription.
//
// It is deliberately not read from capitalism. capitalism talks to the payment
// provider, and the provider's API is not something to put on a request path: a
// Stripe round trip per feature check is a latency budget spent on a fact that
// changes a few times a year, and an outage there would take the product down
// rather than the billing. The provider tells you when a subscription changes;
// what it changed to belongs in your database by the time anybody asks.
//
// Returning an error wrapping ErrNoPlan means the account has no plan, which is
// an answer rather than a failure — see ErrNoPlan. Any other error is a failure,
// and CheckerConfig.FallbackPlan decides what happens next.
//
// Implementations must be safe for concurrent use.
type PlanSource interface {
	PlanFor(ctx context.Context, account string) (string, error)
}

// PlanSourceFunc adapts a function to PlanSource.
type PlanSourceFunc func(ctx context.Context, account string) (string, error)

// PlanFor implements PlanSource.
func (f PlanSourceFunc) PlanFor(ctx context.Context, account string) (string, error) {
	return f(ctx, account)
}

var _ PlanSource = (*StaticPlanSource)(nil)

// StaticPlanSource puts every account on one plan. It is exported, and returned
// by NewStaticPlanSource, so a caller can depend on the source it built rather
// than on the PlanSource seam.
type StaticPlanSource struct {
	plan string
}

// NewStaticPlanSource returns a PlanSource that answers plan for every account.
//
// It is right for a deployment that sells one thing — an internal service gating
// on a catalog rather than on a subscription, or a product before it has tiers —
// and wrong the moment two accounts can buy different amounts. It is also what
// makes an entitlements wiring testable without a database.
//
// A free tier is not this. "Accounts with no subscription get the free plan" is
// a business rule, and it belongs in the application's own PlanSource where it
// is visible next to the query that failed to find a subscription; expressing it
// here would make every account free the day the subscription lookup broke.
func NewStaticPlanSource(plan string) *StaticPlanSource {
	return &StaticPlanSource{plan: plan}
}

// PlanFor implements PlanSource.
func (s *StaticPlanSource) PlanFor(context.Context, string) (string, error) {
	if s.plan == "" {
		return "", platformerrors.Wrap(ErrNoPlan, "static plan source has no plan")
	}

	return s.plan, nil
}

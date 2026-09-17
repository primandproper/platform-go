package plans

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/metering"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The sentinels the period resolver returns.
var (
	// ErrNilCycle indicates a nil Cycle. It is required for the reason Choose
	// is — see [Sole] for the reading most deployments take.
	ErrNilCycle = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil billing period cycle")

	// ErrNoBillingPeriod indicates no subscription supplies a window for the
	// instant being resolved: the account holds none that covers it, or holds
	// several and Cycle declined to pick between them.
	//
	// It is a refusal rather than a fallback to the calendar month. Usage filed
	// under a window the provider will not invoice is usage nobody is billed
	// for, and it arrives a month later looking like a pricing bug.
	ErrNoBillingPeriod = platformerrors.New("no current subscription supplies a billing period")

	// ErrTooManySubscriptions indicates an account holding more current
	// subscriptions than one page of this table can show, where the window is
	// therefore not answerable.
	//
	// It is separate from ErrNoBillingPeriod because it is not a statement about
	// the account: the account may well have exactly one window, and this is the
	// resolver saying it could not see enough of the set to know. Cycle is a
	// question about the whole of it — Sole answers only when nothing disagrees
	// with what it picked — so a disagreement past the page bound is one it would
	// answer confidently and wrongly, and the wrong answer becomes the primary
	// key of a durable total that no invoice would say was wrong.
	//
	// Source.PlanFor does not refuse the same page, and the asymmetry is the
	// point: Choose asks nothing of the rows it was not shown. See pageSize.
	ErrTooManySubscriptions = platformerrors.New("account holds more current subscriptions than the billing period resolver will read")

	// ErrNotBillingPeriod indicates a period other than
	// metering.PeriodBillingPeriod. This resolver answers that one and no
	// others; the calendar periods are metering.NewCalendarPeriodResolver's,
	// which is what this is handed to.
	ErrNotBillingPeriod = platformerrors.New("billing period resolver asked for a calendar period")
)

// Cycle picks the subscription whose paid period is the billing period.
//
// The slice holds every one of the account's current subscriptions whose paid
// period covers the instant being resolved, in the order the store paged them:
// oldest first, which is the order they were opened in. It is never nil and may
// be empty, and reporting false is how this says the account has no billing
// period for that instant — which reaches the caller as [ErrNoBillingPeriod].
//
// It is a constructor argument rather than an option with a default for the
// reason [Choose] is: an account holding two current subscriptions at once —
// which is what an upgrade looks like while it settles, and what a deployment
// selling two products looks like always — has two cycles, and which of them a
// shared meter's usage belongs to is the deployment's answer. See [Sole] for
// the reading most of them want.
type Cycle func(subscriptions []*billing.Subscription) (*billing.Subscription, bool)

// Sole is the cycle most deployments want and none of them have to take: the
// one covering subscription, or nothing at all when two of them disagree about
// where the window is.
//
// Two subscriptions on the same cycle are one answer, because they are one
// window — a deployment selling an add-on alongside a plan renews both together
// and has nothing to choose between. Two on different cycles are refused rather
// than resolved by position, because the bounds become the primary key of a
// durable total: picking the first would file one meter's usage under one
// subscription's window this month and the other's the month the paging order
// changes, and neither invoice would say so.
//
// A deployment that genuinely has overlapping cycles writes the rule it bills
// by — the oldest agreement, the one naming a particular product — as its own
// Cycle.
func Sole(subscriptions []*billing.Subscription) (*billing.Subscription, bool) {
	var chosen *billing.Subscription

	for _, subscription := range subscriptions {
		if chosen == nil {
			chosen = subscription

			continue
		}

		if !subscription.CurrentPeriodStart.Equal(chosen.CurrentPeriodStart) ||
			!subscription.CurrentPeriodEnd.Equal(chosen.CurrentPeriodEnd) {
			return nil, false
		}
	}

	return chosen, chosen != nil
}

var _ metering.PeriodResolver = (*PeriodResolver)(nil)

// PeriodResolver is a metering.PeriodResolver backed by a billing store: the
// window metering.PeriodBillingPeriod buckets by is the paid period the
// subscription row already carries.
//
// It is exported, and returned by NewPeriodResolver, so a caller can depend on
// the resolver it built rather than on the seam every resolver shares.
type PeriodResolver struct {
	store  billing.SubscriptionStore
	reader database.SQLQueryExecutor
	cycle  Cycle
	scope  tenancy.Scope
}

// NewPeriodResolver builds the billing-period half of a metering resolver over
// one scope's subscriptions.
//
// It is handed to metering.NewCalendarPeriodResolver rather than to a recorder
// directly:
//
//	periods, err := plans.NewPeriodResolver(store, client.Reader(), scope, plans.Sole)
//	if err != nil {
//	    return err
//	}
//
//	recorder, err := metering.NewRecorder(meteringStore, registry,
//	    metering.WithRecorderPeriodResolver(metering.NewCalendarPeriodResolver(periods)))
//
// because the calendar periods are arithmetic on the instant and want no
// database read at all, and delegating only the one period that does is what
// leaves a day-bucketed meter costing nothing.
//
// The scope and the executor are fixed at construction for the reason [New]'s
// are: metering.PeriodResolver.Resolve takes a subject and an instant, and
// there is nowhere in that signature to put either.
func NewPeriodResolver(
	store billing.SubscriptionStore,
	reader database.SQLQueryExecutor,
	scope tenancy.Scope,
	cycle Cycle,
) (*PeriodResolver, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if reader == nil {
		return nil, ErrNilExecutor
	}

	if cycle == nil {
		return nil, ErrNilCycle
	}

	if err := scope.Validate(); err != nil {
		return nil, err
	}

	return &PeriodResolver{store: store, reader: reader, scope: scope, cycle: cycle}, nil
}

// Resolve implements metering.PeriodResolver for metering.PeriodBillingPeriod.
//
// Cycle is shown the subscriptions whose paid period covers at rather than every
// current one, and the distinction is the whole reason the instant is an
// argument: usage is recorded at Usage.OccurredAt, which a queue redelivery or a
// batch replayed by hand can put well before now. A subscription is current as
// of the store's clock and still says nothing about an instant outside the
// window it names, so an instant no row covers is refused here rather than
// filed under whichever window happens to be open.
//
// The chosen subscription is vetted against at for the same reason
// metering.CalendarResolver vets the bounds it is handed: what comes back keys
// a durable total, and a window that does not contain the usage it is keyed by
// is one nothing downstream can notice.
//
// An account holding more current subscriptions than one page can show is
// ErrTooManySubscriptions rather than a window, for the same reason again: Cycle
// is a question about the whole set, and a page that was cut short cannot be
// asked it. That is a deployment whose billing data has gone wrong, and a meter
// that refuses is one somebody fixes.
func (r *PeriodResolver) Resolve(
	ctx context.Context,
	subject string,
	p metering.Period,
	at time.Time,
) (metering.Bounds, error) {
	if p != metering.PeriodBillingPeriod {
		return metering.Bounds{}, platformerrors.Wrapf(ErrNotBillingPeriod, "period %q", p)
	}

	subscriptions, truncated, err := currentSubscriptions(ctx, r.store, r.reader, r.scope, subject)
	if err != nil {
		return metering.Bounds{}, platformerrors.Wrapf(err, "reading current subscriptions for subject %q", subject)
	}

	// Refused before the cycle is consulted rather than after. What was cut off
	// the page is exactly what Sole needed to see in order to refuse, so asking
	// it here would be asking a question whose answer is already known to be
	// unreliable.
	if truncated {
		return metering.Bounds{}, platformerrors.Wrapf(ErrTooManySubscriptions,
			"subject %q holds more than %d", subject, pageSize)
	}

	covering := make([]*billing.Subscription, 0, len(subscriptions))

	for _, subscription := range subscriptions {
		if subscription.CurrentAt(at) {
			covering = append(covering, subscription)
		}
	}

	chosen, ok := r.cycle(covering)
	if !ok {
		return metering.Bounds{}, platformerrors.Wrapf(ErrNoBillingPeriod,
			"subject %q, %d of %d current subscriptions cover %s",
			subject, len(covering), len(subscriptions), at.UTC())
	}

	// Vetted rather than trusted, and against the instant rather than against
	// the slice: a Cycle that hands back a subscription from somewhere else is
	// the same mistake as one that hands back nil, and both read as a period
	// this account never bought.
	if !chosen.CurrentAt(at) {
		return metering.Bounds{}, platformerrors.Newf(
			"billing period cycle chose a subscription whose paid period does not cover %s for subject %q",
			at.UTC(), subject)
	}

	return metering.Bounds{
		Start: chosen.CurrentPeriodStart.UTC(),
		End:   chosen.CurrentPeriodEnd.UTC(),
	}, nil
}

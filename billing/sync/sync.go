package sync

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/standing"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// componentName scopes the spans, logger and instruments a Syncer emits.
//
// It is separate from billing's own so that a dashboard can tell "a delivery
// arrived" from "a row was written", which are the same event only when the
// deliveries are being applied.
const componentName = "billing_sync"

// The values a sync records on its span and its logger.
const (
	scopeKey          = componentName + ".scope"
	eventIDKey        = componentName + ".event_id"
	eventTypeKey      = componentName + ".event_type"
	externalIDKey     = componentName + ".external_subscription_id"
	subscriptionKey   = componentName + ".subscription"
	accountKey        = componentName + ".account"
	statusKey         = componentName + ".status"
	providerStatusKey = componentName + ".provider_status"
	outcomeKey        = componentName + ".outcome"
	standingKey       = componentName + ".standing"
	standingPlacedKey = componentName + ".standing_placed"
)

// Outcome is what one delivery did to the store.
//
// It is returned rather than inferred from the row, because two of the five
// answers have no row to infer anything from and the other three leave one that
// looks the same afterwards. A deployment watching OutcomeUnplaced climb is
// watching an adapter fall behind its provider's vocabulary, and nothing in the
// subscriptions table would say so.
type Outcome string

const (
	// OutcomeIgnored is an event carrying no subscription state. Nothing was
	// read and nothing was written.
	OutcomeIgnored Outcome = "ignored"

	// OutcomeUnplaced is a delivery whose status no adapter could place.
	// Nothing was written; see the package documentation for why the permissive
	// reading is the one thing this must not do.
	OutcomeUnplaced Outcome = "unplaced"

	// OutcomeCreated is the first delivery for an agreement nobody held.
	OutcomeCreated Outcome = "created"

	// OutcomeUpdated is a delivery that moved a status, a paid period, or both.
	OutcomeUpdated Outcome = "updated"

	// OutcomeUnchanged is a delivery reporting what the row already said, which
	// is what a redelivery looks like.
	OutcomeUnchanged Outcome = "unchanged"
)

type (
	// Placement is what a delivery cannot say: whose agreement this is, and
	// which product it buys.
	//
	// Both are required, because billing requires both of a subscription it
	// stores. They are this deployment's own identifiers rather than the
	// provider's — the product id is billing's, not the price id capitalism
	// reports — and turning one into the other is what a [Place] is for.
	Placement struct {
		// AccountID is the account the agreement belongs to, in this
		// deployment's words.
		AccountID string

		// ProductID is the billing product the agreement buys. It is the id of a
		// row in billing's own products table: a create is gated on the product
		// existing, in the same transaction.
		ProductID string
	}

	// Place answers where a delivery lands, for a subscription this deployment
	// does not hold yet.
	//
	// It is called on a create and never on an update, because an agreement's
	// account is immutable and its product moves only where the deployment says
	// so. A delivery for a subscription already stored does not consult it at
	// all, so a Place that reads the provider's API costs nothing on the
	// deliveries that arrive every month.
	//
	// The executor is the caller's transaction. A Place that reads this
	// deployment's own tables — the account holding the customer id capitalism
	// reported is the usual one — sees everything that transaction has written,
	// including a customer id attached seconds earlier by the checkout flow that
	// caused this delivery.
	//
	// Reporting an error refuses the delivery. A customer this deployment does
	// not know is a fact worth surfacing: the alternative is a subscription
	// filed against an account picked by a fallback.
	Place func(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		state *capitalism.SubscriptionState,
	) (*Placement, error)

	// Result is what one delivery did.
	Result struct {
		// Subscription is the agreement as the store now holds it, or nil for
		// the two outcomes that read nothing — OutcomeIgnored and
		// OutcomeUnplaced.
		//
		// It is read back on the caller's transaction, so what comes back is
		// what that transaction will commit rather than what the sync assembled
		// on its way in.
		Subscription *billing.Subscription

		// Standing is the account standing this delivery wrote, or nil where
		// none was: a Syncer built without [WithStanding], a delivery that
		// changed nothing, or a Classify that could not place the status.
		Standing *identity.BillingStatus

		// Outcome is what happened, and it is never empty.
		Outcome Outcome
	}

	// Syncer applies a payment processor's deliveries to the billing store.
	//
	// It owns no table. What it adds to billing.SubscriptionStore is the order
	// the store's methods are called in for one delivery, the two readings the
	// hand-written version gets wrong, and the standing write that belongs in
	// the same transaction as the row that caused it.
	Syncer struct {
		store    billing.SubscriptionStore
		place    Place
		accounts identity.BillingWriter
		classify standing.Classify
		o11y     observability.Observer

		instruments *metrics.OperationSet

		// deliveries counts deliveries by what they did, which the operation
		// set cannot: its request counter is one attempt per call, and three of
		// the five outcomes are the sync declining to write. A deployment
		// watching OutcomeUnplaced climb is watching an adapter that has fallen
		// behind its provider's vocabulary, and nothing in the subscriptions
		// table would say so.
		deliveries metrics.Int64Counter

		// What the options wrote, kept only until the observer is built from it.
		// Read s.o11y.Logger() for the logger a Syncer actually uses; this one
		// may be nil, because supplying none is how a caller asks for no
		// logging.
		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider
	}
)

// New builds a Syncer over a billing subscription store.
//
// Both positional dependencies are required. The store, because this package
// ships no implementation of its own and a deployment keeping its agreements
// elsewhere passes another. The [Place], because an agreement nobody holds yet
// has an account and a product no delivery carries, and a sync that guessed
// either would file somebody's subscription against the wrong account.
//
// Everything else is an option, and absent means nothing happens: no standing is
// recorded, nothing is logged, nothing is traced and nothing is measured.
func New(store billing.SubscriptionStore, place Place, opts ...Option) (*Syncer, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if place == nil {
		return nil, ErrNilPlace
	}

	s := &Syncer{store: store, place: place}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	// Half a standing seam is refused here rather than one delivery later, where
	// it would be a nil classifier panicking inside somebody's webhook endpoint.
	if s.accounts != nil && s.classify == nil {
		return nil, ErrNilClassify
	}

	if s.classify != nil && s.accounts == nil {
		return nil, ErrNilBillingWriter
	}

	s.o11y = observability.NewObserver(componentName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, componentName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating billing sync instruments")
	}

	s.instruments = instruments

	if s.deliveries, err = metrics.EnsureMetricsProvider(s.metricsProvider).
		NewInt64Counter(componentName + "_deliveries"); err != nil {
		return nil, platformerrors.Wrap(err, "creating billing sync delivery counter")
	}

	return s, nil
}

// Apply reconciles one delivery into the store, on the caller's transaction.
//
//	err := client.WithTransaction(ctx, func(tx database.Tx) error {
//		result, syncErr := syncer.Apply(ctx, tx, scope, event)
//		if syncErr != nil {
//			return syncErr
//		}
//
//		return recordAudit(ctx, tx, scope, result)
//	})
//
// What it does with a delivery depends on what the store already holds, and the
// three paths are the whole of it:
//
// An agreement nobody holds is opened, with the paid period the delivery
// reported and the account and product its [Place] answered with. A delivery
// reporting no bounded period cannot open one and is refused — see
// [ErrNoPaidPeriod], which is the refusal this package exists for.
//
// An agreement whose paid period moved is updated: the renewal that shifted the
// window, and the status with it. That write is why the period is carried at all
// — a row left on last month's window lapses out of
// billing.SubscriptionStore.ListCurrentSubscriptions while the customer is
// paying, and nothing about the delivery that should have moved it would say so.
//
// An agreement whose period is unchanged has its status moved and nothing else,
// through billing.SubscriptionStore.SetSubscriptionStatus, whose guard lives in
// the statement. A redelivery therefore writes nothing and answers
// billing.ErrStatusUnchanged, which this acknowledges: a provider resending an
// event it already sent is the protocol working, and a webhook endpoint that
// returned 500 to it would be asking for it again forever. It comes back as
// [OutcomeUnchanged].
//
// A half-bounded period — one end reported and not the other — leaves the stored
// period alone and moves the status. Writing one end against the other end of an
// older window is the same invention refused above, wearing a stored value as a
// disguise.
//
// The scope is the tenant the delivery is for. It is an argument rather than
// something read off the event, because a processor's customer is not a tenant
// and nothing in a capitalism.Event knows which of a deployment's tenants it
// arrived for. An application with one tenant passes tenancy.Global.
//
// The event is not written to.
func (s *Syncer) Apply(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	event *capitalism.Event,
) (result *Result, err error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))

	s.instruments.Attempt(ctx)

	stop := op.Time(ctx, nil, s.instruments.Latency)

	defer func() {
		if err != nil {
			s.instruments.Failed(ctx)
		}

		stop()
		op.End()
	}()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "syncing a subscription event")
	}

	if event == nil {
		return nil, op.Error(ErrNilEvent, "syncing a subscription event")
	}

	if err = scope.Validate(); err != nil {
		return nil, op.Error(err, "syncing a subscription event")
	}

	if event.ID != "" {
		op.Set(eventIDKey, event.ID)
	}

	if event.Type != "" {
		op.Set(eventTypeKey, event.Type)
	}

	state := event.Subscription
	if state == nil {
		// Not every delivery is about a subscription, and capitalism keeps that
		// a third case rather than an unknown status precisely so this branch
		// can exist. A succeeded payment intent reaching a subscription sync is
		// the sync being wired to one endpoint rather than a problem.
		return s.acknowledged(ctx, op, OutcomeIgnored), nil
	}

	if state.ID == "" {
		return nil, op.Error(ErrUnidentifiedSubscription, "syncing a subscription event")
	}

	op.Set(externalIDKey, state.ID)

	if !state.Status.Known() {
		// The status-less delivery, which is where the hand-written version
		// reads Active. What arrived is recorded so the fix is one entry in an
		// adapter's table; the stored standing stays stale rather than becoming
		// wrong.
		op.Set(providerStatusKey, state.ProviderStatus)

		return s.acknowledged(ctx, op, OutcomeUnplaced), nil
	}

	op.Set(statusKey, string(state.Status))

	existing, readErr := s.store.GetSubscriptionByExternalID(ctx, tx, scope, state.ID)

	switch {
	case readErr == nil:
		result, err = s.move(ctx, tx, op, scope, existing, state)
	case stderrors.Is(readErr, billing.ErrSubscriptionNotFound):
		result, err = s.open(ctx, tx, scope, state)
	default:
		return nil, op.Error(readErr, "reading the subscription a delivery reports")
	}

	if err != nil {
		return nil, op.Error(err, "applying a subscription event")
	}

	op.Set(subscriptionKey, result.Subscription.ID)
	op.Set(accountKey, result.Subscription.BelongsToAccount)
	op.Set(outcomeKey, string(result.Outcome))

	// Counted here, which is before the caller's transaction commits and before
	// the standing write below has even run. It counts what a delivery decided
	// rather than what was committed, for the reason webhooks.Emitter's two
	// counters do: the alternative is a counter that cannot be incremented from
	// inside the transaction it is describing.
	s.deliveries.Add(ctx, 1, outcomeAttr(result.Outcome))

	if err = s.record(ctx, tx, op, scope, result); err != nil {
		return nil, op.Error(err, "recording the account standing a delivery reports")
	}

	return result, nil
}

// open writes the agreement this deployment did not hold.
//
// The period is checked before the Place is consulted, so a delivery nothing
// could store does not first send somebody's resolver to the database — and, on
// a resolver that reads the provider's API, does not spend a network call on a
// row that is about to be refused.
func (s *Syncer) open(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	state *capitalism.SubscriptionState,
) (*Result, error) {
	start, end, bounded := paidPeriod(state)
	if !bounded {
		return nil, platformerrors.Wrapf(ErrNoPaidPeriod, "subscription %q", state.ID)
	}

	placement, err := s.place(ctx, tx, scope, state)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "placing subscription %q", state.ID)
	}

	if placement == nil {
		return nil, platformerrors.Wrapf(ErrNoPlacement, "subscription %q", state.ID)
	}

	created, err := s.store.CreateSubscription(ctx, tx, scope, &billing.Subscription{
		BelongsToAccount:       placement.AccountID,
		ProductID:              placement.ProductID,
		ExternalSubscriptionID: state.ID,
		Status:                 state.Status,
		CurrentPeriodStart:     start,
		CurrentPeriodEnd:       end,
	})
	if err != nil {
		return nil, platformerrors.Wrapf(err, "opening subscription %q", state.ID)
	}

	return &Result{Subscription: created, Outcome: OutcomeCreated}, nil
}

// move applies a delivery to an agreement the store already holds.
func (s *Syncer) move(
	ctx context.Context,
	tx database.Tx,
	op observability.Operation,
	scope tenancy.Scope,
	existing *billing.Subscription,
	state *capitalism.SubscriptionState,
) (*Result, error) {
	start, end, bounded := paidPeriod(state)

	// The period write and the status write are one statement rather than two,
	// because a renewal moves both and two statements would leave a window in
	// which the row says the new period at the old status. UpdateSubscription
	// answers with the row it wrote, so no read follows it.
	if bounded && movedPeriod(existing, start, end) {
		desired := *existing
		desired.Status = state.Status
		desired.CurrentPeriodStart = start
		desired.CurrentPeriodEnd = end

		updated, err := s.store.UpdateSubscription(ctx, tx, scope, &desired)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "renewing subscription %q", existing.ID)
		}

		return &Result{Subscription: updated, Outcome: OutcomeUpdated}, nil
	}

	if existing.Status == state.Status {
		return &Result{Subscription: existing, Outcome: OutcomeUnchanged}, nil
	}

	if err := s.store.SetSubscriptionStatus(ctx, tx, scope, existing.ID, state.Status); err != nil {
		// Not an error path. The read above says the status is moving, so
		// reaching this means somebody else moved it to the same value between
		// that read and this write — a concurrent delivery of the same event,
		// which is the thing the store's in-statement guard exists to make
		// harmless.
		if stderrors.Is(err, billing.ErrStatusUnchanged) {
			op.Acknowledge(err, "subscription %q already holds the reported status", existing.ID)

			return &Result{Subscription: existing, Outcome: OutcomeUnchanged}, nil
		}

		return nil, platformerrors.Wrapf(err, "moving subscription %q status", existing.ID)
	}

	// Read back rather than assembled, for the reason the store reads its own
	// writes back: the status write stamps LastUpdatedAt, and a Result carrying
	// the pre-image with one field edited would differ from the row the caller's
	// transaction is about to commit.
	stored, err := s.store.GetSubscription(ctx, tx, scope, existing.ID)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "reading back subscription %q", existing.ID)
	}

	return &Result{Subscription: stored, Outcome: OutcomeUpdated}, nil
}

// record writes the account's coarse standing, where a Syncer was given
// somewhere to write it.
//
// A delivery that changed nothing writes nothing here either. The standing was
// written by whichever delivery moved the row in the first place, and a
// redelivery that rewrote it would stamp identity's reconciliation time for an
// event that reported no news.
func (s *Syncer) record(
	ctx context.Context,
	tx database.Tx,
	op observability.Operation,
	scope tenancy.Scope,
	result *Result,
) error {
	if s.accounts == nil || result.Outcome == OutcomeUnchanged {
		return nil
	}

	subscription := result.Subscription

	status, ok := s.classify(subscription.Status)
	if !ok {
		// standing's ruling, applied: a status the deployment has not ruled on
		// leaves the account where it is. The subscription row still moved, so
		// the fact is stored and only the reading is missing.
		op.Set(standingPlacedKey, false)

		return nil
	}

	var err error

	if standing.Ended(subscription.Status) {
		err = s.accounts.RecordAccountSubscriptionEnded(ctx, tx, scope, subscription.BelongsToAccount, status)
	} else {
		err = s.accounts.RecordAccountSubscription(
			ctx, tx, scope, subscription.BelongsToAccount, status, subscription.ProductID)
	}

	if err != nil {
		return platformerrors.Wrapf(err, "recording standing for account %q", subscription.BelongsToAccount)
	}

	op.Set(standingKey, string(status))
	result.Standing = &status

	return nil
}

// acknowledged is the answer to a delivery there was nothing to do about.
func (s *Syncer) acknowledged(ctx context.Context, op observability.Operation, outcome Outcome) *Result {
	op.Set(outcomeKey, string(outcome))
	s.deliveries.Add(ctx, 1, outcomeAttr(outcome))

	return &Result{Outcome: outcome}
}

// periodPrecision is the granularity a stored paid period is compared at, and
// it is the coarsest any dialect this module ships a schema for keeps.
//
// MySQL and Postgres hold these columns to the microsecond; SQLite holds them
// to the second. So a period read back is not necessarily the period written,
// and comparing a delivery's instants against it exactly asks a question the
// storage cannot answer — on SQLite every redelivery of an unchanged agreement
// would differ in the discarded fraction, take the renewal branch, and report
// OutcomeUpdated for news nobody sent. The account standing would be rewritten
// with it, which is the write record exists to skip for a redelivery.
//
// A second is the right coarseness rather than merely the safe one. A paid
// period is a billing boundary a provider names to the second at best, so two
// periods that differ by less than one are not two periods.
const periodPrecision = time.Second

// movedPeriod reports whether the delivery's paid period is a different period
// from the one the row holds, at the precision the row is stored to.
//
// Both sides are truncated, not just the stored one: the value that came off
// the wire carries whatever precision the provider sent, and comparing a
// truncated column against an untruncated instant is the same mistake in one
// direction only. See periodPrecision.
func movedPeriod(existing *billing.Subscription, start, end time.Time) bool {
	return !existing.CurrentPeriodStart.Truncate(periodPrecision).Equal(start.Truncate(periodPrecision)) ||
		!existing.CurrentPeriodEnd.Truncate(periodPrecision).Equal(end.Truncate(periodPrecision))
}

// paidPeriod is the window a delivery reported, and whether it reported one at
// all.
//
// Both ends or neither. capitalism keeps them independent because a half-bounded
// period is a real thing for a provider to report, and billing stores neither
// end as nullable — so the only way to store a half-bounded one is to invent the
// other half, which is exactly what this package refuses to do.
func paidPeriod(state *capitalism.SubscriptionState) (start, end time.Time, bounded bool) {
	if state.CurrentPeriodStart == nil || state.CurrentPeriodEnd == nil {
		return time.Time{}, time.Time{}, false
	}

	return state.CurrentPeriodStart.UTC(), state.CurrentPeriodEnd.UTC(), true
}

// outcomeAttr labels the delivery counter with what the delivery did.
func outcomeAttr(outcome Outcome) metric.AddOption {
	return metric.WithAttributes(attribute.String(outcomeKey, string(outcome)))
}

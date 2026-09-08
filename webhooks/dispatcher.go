package webhooks

import (
	"context"

	"github.com/primandproper/primitives-go/clock"
	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
	"github.com/primandproper/primitives-go/tenancy"
)

// Dispatcher is the write side: it turns an application event into per-endpoint
// work, and re-drives that work when an operator asks.
//
// Every method here writes, and every one of them but Replay takes the caller's
// database.Tx — a registration, a subscription and the event that fans out are
// all things an application does beside something else of its own, and the
// transaction argument is what lets the two commit together. Replay is the
// exception and says why on its own doc comment.
type Dispatcher interface {
	// Dispatch fans an event out to every endpoint in the delivery's scope that is
	// subscribed to it, writing through the caller's transaction so the deliveries
	// commit with the state change that caused them.
	Dispatch(ctx context.Context, tx database.Tx, delivery *Delivery) error
	// Replay re-drives a specific past delivery to a specific one of the scope's
	// endpoints, for operator recovery. It takes no transaction; see
	// StoreDispatcher.Replay.
	Replay(ctx context.Context, scope tenancy.Scope, deliveryID, endpointID string) error
	// Register validates and stores an endpoint in scope, through the caller's
	// transaction. Validation is not optional and not separable: an unvalidated
	// endpoint is an SSRF target.
	Register(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpoint *Endpoint) error
	// Subscribe adds one event type to one of the scope's endpoints, gating on the
	// catalog, and returns the subscription — through the caller's transaction.
	Subscribe(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpointID string, eventType EventType) (*Subscription, error)
	// Unsubscribe retires one of the scope's subscriptions by ID, through the
	// caller's transaction, leaving the endpoint and its other subscriptions
	// alone.
	Unsubscribe(ctx context.Context, tx database.Tx, scope tenancy.Scope, subscriptionID string) error
}

var _ Dispatcher = (*StoreDispatcher)(nil)

// StoreDispatcher is the Dispatcher backed by a Store. It is exported, and
// returned by NewDispatcher, so a caller can depend on the dispatcher it built
// rather than on the Dispatcher seam.
type StoreDispatcher struct {
	store    Store
	reader   database.SQLQueryExecutor
	clock    clock.Clock
	o11y     observability.Observer
	catalog  Catalog
	checkURL URLChecker

	dispatchedCounter metrics.Int64Counter
	fanoutHist        metrics.Float64Histogram
	replayedCounter   metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read d.o11y.Logger() for the logger this dispatcher actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// NewDispatcher builds a Dispatcher over the given Store.
//
// reader is the executor Replay's precondition read runs on, and it is the one
// thing here a caller cannot leave out. Every other read this package performs
// belongs to somebody: a consumer's, on the executor they name, or the worker's,
// on the handle the store was built with. Replay's is neither — it is an
// operator asking the queue to move, with no transaction of their own — and it
// has to see committed state, because the requeue that follows commits on its
// own whatever the caller does next. Pass Client.Reader(); a deployment that
// wants that check against the primary passes Client.Writer(), and that is now a
// visible wiring decision rather than a choice this constructor made quietly.
func NewDispatcher(store Store, reader database.SQLQueryExecutor, opts ...DispatcherOption) (*StoreDispatcher, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if reader == nil {
		return nil, ErrNilExecutor
	}

	d := &StoreDispatcher{
		store:    store,
		reader:   reader,
		clock:    clock.NewClock(),
		catalog:  Catalog{},
		checkURL: CheckEndpointURL,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}

	d.o11y = observability.NewObserver(serviceName, d.logger, d.tracerProvider)

	mp := metrics.EnsureMetricsProvider(d.metricsProvider)

	var err error
	if d.dispatchedCounter, err = mp.NewInt64Counter(serviceName + "_deliveries_dispatched"); err != nil {
		return nil, platformerrors.Wrap(err, "creating deliveries dispatched counter")
	}
	if d.replayedCounter, err = mp.NewInt64Counter(serviceName + "_deliveries_replayed"); err != nil {
		return nil, platformerrors.Wrap(err, "creating deliveries replayed counter")
	}
	if d.fanoutHist, err = mp.NewFloat64Histogram(serviceName + "_dispatch_fanout"); err != nil {
		return nil, platformerrors.Wrap(err, "creating dispatch fanout histogram")
	}

	return d, nil
}

// Register validates an endpoint against the catalog and the SSRF rules, then
// stores it in scope, through the caller's transaction.
//
// Validation happens here rather than being left to the caller because the
// consequence of skipping it is not a bad row — it is a server that will make
// authenticated requests to whatever URL was submitted. There is no variant of
// this that stores without checking.
//
// The transaction is the caller's because registering a subscriber is rarely the
// only thing that happens: the audit entry naming who registered it, and the
// account row that now has one, belong in the same commit. An endpoint that
// names no scope adopts the argument; one naming a different tenant is
// ErrScopeMismatch, and is refused before anything is validated or written.
func (d *StoreDispatcher) Register(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpoint *Endpoint) error {
	ctx, op := d.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "registering webhook endpoint")
	}

	if endpoint == nil {
		return op.Error(ErrNilEndpoint, "registering webhook endpoint")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "registering webhook endpoint")
	}

	// The scope is settled before Validate rather than after, so that an endpoint
	// carrying somebody else's is refused as the mix-up it is instead of being
	// validated as though it were this caller's.
	if err := adoptEndpointScope(scope, endpoint); err != nil {
		return op.Error(err, "registering webhook endpoint")
	}

	endpoint.EnsureDefaults()

	if endpoint.ID == "" {
		endpoint.ID = identifiers.New()
	}

	op.Set(endpointIDKey, endpoint.ID).
		SpanOnly(endpointURLKey, endpoint.URL)

	if err := endpoint.Validate(ctx, d.catalog, d.checkURL); err != nil {
		return op.Error(err, "validating webhook endpoint")
	}

	if err := d.store.SaveEndpoint(ctx, tx, scope, endpoint); err != nil {
		return op.Error(err, "saving webhook endpoint")
	}

	return nil
}

// Subscribe adds one event type to an existing endpoint, without rewriting the
// rest of its subscriptions.
//
// It goes through the dispatcher rather than straight to the Store for the
// reason Register does: the catalog gate lives here. A subscription to an event
// type nothing publishes is accepted silently by any storage layer and produces
// an endpoint that never fires, with no signal explaining why — which is the
// failure the catalog exists to turn into an error at the moment somebody can
// still fix the typo.
//
// It is idempotent on the (endpoint, event type) pair. Subscribing to something
// the endpoint already receives returns the existing subscription, and
// re-subscribing to one that was archived revives it, keeping its ID.
func (d *StoreDispatcher) Subscribe(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpointID string, eventType EventType) (*Subscription, error) {
	ctx, op := d.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(endpointIDKey, endpointID),
		observability.WithValue(eventTypeKey, eventType.String()),
	)
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "subscribing webhook endpoint %q", endpointID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "subscribing webhook endpoint %q", endpointID)
	}

	if endpointID == "" {
		return nil, op.Error(platformerrors.ErrInvalidIDProvided, "subscribing webhook endpoint")
	}

	if eventType == "" {
		return nil, op.Error(ErrEmptyEventType, "subscribing webhook endpoint %q", endpointID)
	}

	if !d.catalog.Known(eventType) {
		return nil, op.Error(
			platformerrors.Wrapf(ErrUnknownEventType, "event type %q", eventType),
			"subscribing webhook endpoint %q", endpointID,
		)
	}

	subscription, err := d.store.AddSubscription(ctx, tx, scope, endpointID, eventType)
	if err != nil {
		return nil, op.Error(err, "subscribing webhook endpoint %q to %q", endpointID, eventType)
	}

	op.Set(subscriptionIDKey, subscription.ID)

	return subscription, nil
}

// Unsubscribe retires one subscription, named by its own ID.
//
// This is the operation a flat event list has no form for. Against one, "stop
// sending me order.created" is a rewrite of the whole set — which loses when the
// subscription ended, has nothing for the request that asked to name, and
// silently reverts any concurrent edit of the same endpoint.
//
// There is no catalog gate here, and there should not be: an event type can be
// removed from an application's catalog, and the subscriptions to it are exactly
// what somebody then needs to be able to retire.
func (d *StoreDispatcher) Unsubscribe(ctx context.Context, tx database.Tx, scope tenancy.Scope, subscriptionID string) error {
	ctx, op := d.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subscriptionIDKey, subscriptionID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "unsubscribing webhook subscription %q", subscriptionID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "unsubscribing webhook subscription %q", subscriptionID)
	}

	if subscriptionID == "" {
		return op.Error(platformerrors.ErrInvalidIDProvided, "unsubscribing webhook subscription")
	}

	if err := d.store.ArchiveSubscription(ctx, tx, scope, subscriptionID); err != nil {
		return op.Error(err, "unsubscribing webhook subscription %q", subscriptionID)
	}

	return nil
}

// Dispatch fans a delivery out to its subscribers, inside the caller's
// transaction.
//
// Taking the executor rather than opening its own is the entire transactional
// guarantee, and it is the same seam outbox.Enqueue uses:
//
//	err := client.WithTransaction(ctx, func(tx database.Tx) error {
//		if err := updateOrder(ctx, tx, order); err != nil {
//			return err
//		}
//
//		return dispatcher.Dispatch(ctx, tx, &webhooks.Delivery{
//			EventType:   OrderUpdated,
//			OrderingKey: order.ID,
//			Payload:     body,
//		})
//	})
//
// The deliveries live or die with the state change that caused them. There is
// no way to dispatch outside a transaction by accident: holding a
// SQLQueryExecutor from WithTransaction means you are already in one.
//
// An event nobody subscribes to is not an error and writes nothing. That is the
// common case for most event types most of the time, and making it an error
// would have every publisher branch on it.
//
// The fan-out is bounded by the delivery's Scope: subscribers are resolved within
// it, so an endpoint registered by one account never receives another account's
// copy of the same event type. A delivery with no scope is refused rather than
// fanned out to everybody — see Delivery.Scope. An application whose events are
// global says tenancy.Global() and gets what it had before the dimension existed.
func (d *StoreDispatcher) Dispatch(ctx context.Context, tx database.Tx, delivery *Delivery) error {
	ctx, op := d.o11y.Begin(ctx)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "dispatching webhook delivery")
	}

	if delivery == nil {
		return op.Error(ErrNilDelivery, "dispatching webhook delivery")
	}

	if err := delivery.Scope.Validate(); err != nil {
		return op.Error(err, "dispatching webhook delivery")
	}

	if !d.catalog.Known(delivery.EventType) {
		return op.Error(
			platformerrors.Wrapf(ErrUnknownEventType, "event type %q", delivery.EventType),
			"dispatching webhook delivery",
		)
	}

	if len(delivery.Payload) == 0 {
		return op.Error(
			platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty webhook delivery payload"),
			"dispatching webhook delivery",
		)
	}

	if delivery.ID == "" {
		delivery.ID = identifiers.New()
	}

	op.Set(deliveryIDKey, delivery.ID).
		Set(scopeKey, delivery.Scope.String()).
		Set(eventTypeKey, delivery.EventType.String())

	if delivery.OrderingKey != "" {
		op.Set(orderingKeyKey, delivery.OrderingKey)
	}

	endpoints, err := d.store.EndpointsForEvent(ctx, tx, delivery.Scope, delivery.EventType)
	if err != nil {
		return op.Error(err, "resolving webhook endpoints for event %q", delivery.EventType)
	}

	op.Set(fanoutKey, len(endpoints))
	d.fanoutHist.Record(ctx, float64(len(endpoints)), eventTypeAttr(delivery.EventType))

	if len(endpoints) == 0 {
		return nil
	}

	endpointIDs := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		endpointIDs = append(endpointIDs, endpoint.ID)
	}

	now := d.clock.Now().UTC()

	if err = d.store.Enqueue(ctx, tx, delivery, endpointIDs, now); err != nil {
		return op.Error(err, "enqueuing webhook delivery")
	}

	// Counted after the statements succeed, but the transaction can still roll
	// back afterwards — so this counts intent to deliver, not committed rows.
	// The gap is exactly the rollback rate, and comparing this against
	// webhooks_deliveries_sent is how you see it.
	d.dispatchedCounter.Add(ctx, int64(len(endpointIDs)), eventTypeAttr(delivery.EventType))

	return nil
}

// Replay makes one past delivery to one endpoint claimable again.
//
// It is the operator's recovery tool, and it is scoped to a pair rather than to
// a delivery because that is what recovery actually looks like: one subscriber
// was down, the others were fine, and re-driving the whole delivery would send
// duplicates to everyone who already accepted it.
//
// The attempt count is reset, so a dead dispatch gets a full budget rather than
// dying again on its next attempt.
//
// The scope is what makes this a replay of one's own delivery rather than of
// anybody's. It is established on the endpoint, which is read within it first: an
// endpoint in another scope reads as absent, and the requeue that follows names a
// (delivery, endpoint) pair, which exists only where a fan-out in that scope put
// it.
//
// It is the one method on this type that takes no transaction, and the reason is
// Store.Requeue's: making a dispatch claimable again is a write to the queue's
// own state, committed on the store's handle the moment it is asked for. A
// caller who could hand this a transaction would be told the replay had happened
// and could then roll back around it — the requeue would stand and their record
// of it would not. The endpoint check ahead of it runs on the executor
// NewDispatcher was given, for the same reason: it has to be a fact about
// committed state, since the requeue does not wait for the caller.
func (d *StoreDispatcher) Replay(ctx context.Context, scope tenancy.Scope, deliveryID, endpointID string) error {
	ctx, op := d.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(deliveryIDKey, deliveryID),
		observability.WithValue(endpointIDKey, endpointID),
	)
	defer op.End()

	if err := scope.Validate(); err != nil {
		return op.Error(err, "replaying webhook delivery")
	}

	if deliveryID == "" || endpointID == "" {
		return op.Error(platformerrors.ErrInvalidIDProvided, "replaying webhook delivery")
	}

	// The endpoint is checked before the requeue rather than left to the worker,
	// so an operator replaying to a disabled endpoint is told why nothing
	// happened instead of watching a row sit claimable and never delivered.
	endpoint, err := d.store.GetEndpoint(ctx, d.reader, scope, endpointID)
	if err != nil {
		return op.Error(err, "reading webhook endpoint %q", endpointID)
	}

	if endpoint.Disabled {
		return op.Error(platformerrors.Wrapf(ErrEndpointDisabled, "endpoint %q", endpointID), "replaying webhook delivery")
	}

	if err = d.store.Requeue(ctx, deliveryID, endpointID, d.clock.Now().UTC()); err != nil {
		return op.Error(err, "requeuing webhook delivery")
	}

	d.replayedCounter.Add(ctx, 1)
	op.Set(replayedKey, true).Logger().Info("webhook delivery replayed")

	return nil
}

// maxStoredErrorLength bounds a stored error rendering, so a pathological
// transport error cannot bloat the row.
const maxStoredErrorLength = 1024

// truncateError bounds what goes into last_error and into a recorded delivery
// attempt.
func truncateError(err error) string {
	return platformerrors.TruncateError(err, maxStoredErrorLength)
}

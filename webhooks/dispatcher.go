package webhooks

import (
	"context"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/tenancy"
)

// Dispatcher is the write side: it turns an application event into per-endpoint
// work, and re-drives that work when an operator asks.
type Dispatcher interface {
	// Dispatch fans an event out to every endpoint in the delivery's scope that is
	// subscribed to it, writing through the caller's executor so the deliveries
	// commit with the state change that caused them.
	Dispatch(ctx context.Context, q database.SQLQueryExecutor, delivery *Delivery) error
	// Replay re-drives a specific past delivery to a specific one of the scope's
	// endpoints, for operator recovery.
	Replay(ctx context.Context, scope tenancy.Scope, deliveryID, endpointID string) error
	// Register validates and stores an endpoint, under the scope the endpoint
	// carries. Validation is not optional and not separable: an unvalidated
	// endpoint is an SSRF target.
	Register(ctx context.Context, endpoint *Endpoint) error
}

var _ Dispatcher = (*StoreDispatcher)(nil)

// StoreDispatcher is the Dispatcher backed by a Store. It is exported, and
// returned by NewDispatcher, so a caller can depend on the dispatcher it built
// rather than on the Dispatcher seam.
type StoreDispatcher struct {
	store    Store
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
func NewDispatcher(store Store, opts ...DispatcherOption) (*StoreDispatcher, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	d := &StoreDispatcher{
		store:    store,
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
// stores it.
//
// Validation happens here rather than being left to the caller because the
// consequence of skipping it is not a bad row — it is a server that will make
// authenticated requests to whatever URL was submitted. There is no variant of
// this that stores without checking.
func (d *StoreDispatcher) Register(ctx context.Context, endpoint *Endpoint) error {
	ctx, op := d.o11y.Begin(ctx)
	defer op.End()

	if endpoint == nil {
		return op.Error(ErrNilEndpoint, "registering webhook endpoint")
	}

	endpoint.EnsureDefaults()

	if endpoint.ID == "" {
		endpoint.ID = identifiers.New()
	}

	op.Set(endpointIDKey, endpoint.ID).
		Set(scopeKey, endpoint.Scope.String()).
		SpanOnly(endpointURLKey, endpoint.URL)

	if err := endpoint.Validate(ctx, d.catalog, d.checkURL); err != nil {
		return op.Error(err, "validating webhook endpoint")
	}

	if err := d.store.SaveEndpoint(ctx, endpoint); err != nil {
		return op.Error(err, "saving webhook endpoint")
	}

	return nil
}

// Dispatch fans a delivery out to its subscribers, inside the caller's
// transaction.
//
// Taking the executor rather than opening its own is the entire transactional
// guarantee, and it is the same seam outbox.Enqueue uses:
//
//	err := client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
//		if err := updateOrder(ctx, q, order); err != nil {
//			return err
//		}
//
//		return dispatcher.Dispatch(ctx, q, &webhooks.Delivery{
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
func (d *StoreDispatcher) Dispatch(ctx context.Context, q database.SQLQueryExecutor, delivery *Delivery) error {
	ctx, op := d.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
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

	endpoints, err := d.store.EndpointsForEvent(ctx, q, delivery.Scope, delivery.EventType)
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

	if err = d.store.Enqueue(ctx, q, delivery, endpointIDs, now); err != nil {
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
	endpoint, err := d.store.GetEndpoint(ctx, scope, endpointID)
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

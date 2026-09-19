package webhooks

import (
	"context"
	"encoding/json"

	"github.com/primandproper/platform-go/v14/outbox"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/encoding"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Enqueuer is the outbox seam an Emitter publishes through: one method, the one
// outbox.Writer exports for a caller who is already inside a transaction.
//
// It is declared here rather than in outbox because it is this package that
// needs a name for the half of the writer it uses, and a seam belongs to the
// side that depends on it. The direction is the whole reason the composition
// lives here: outbox is a transport for domain events and must not learn what a
// webhook is, while this package already knows it is dispatching one.
type Enqueuer interface {
	Enqueue(ctx context.Context, tx database.Tx, msgs ...outbox.Message) error
}

// The writer is the implementation this exists to describe, and the assertion
// is what keeps the two signatures from drifting apart at a distance.
var _ Enqueuer = (*outbox.Writer)(nil)

// Event is one domain event on its way out: the fact an application just
// recorded, published to the broker and fanned out to whoever subscribed to it.
//
// It is not a Delivery. A Delivery is what one subscriber receives, and the
// Emitter builds those; this is the application's side of the same event, which
// also has a broker to reach.
type Event struct {
	// Payload is the event body. It is marshaled to JSON once, and those exact
	// bytes are what the outbox stores and what the dispatcher signs and sends —
	// see Emitter.Emit for why that matters.
	//
	// It is any rather than json.RawMessage because the value an application has
	// at the call site is its own event struct, and marshaling it at a hundred
	// and fifty call sites is a hundred and fifty places to forget to check the
	// error.
	Payload any
	// ID identifies the delivery this event produces, and is what Replay names
	// and what a subscriber deduplicates on. Generated when empty.
	//
	// A caller that will need to name the delivery later mints one and passes it,
	// because an Emit that gated out its dispatch produced no delivery to answer
	// with and this type is not written to.
	ID string
	// EventType is the event's name. The catalog decides whether it is
	// subscribable; one that is not is still published to the broker.
	EventType EventType
	// OrderingKey groups events that must not overtake one another — typically
	// the subject resource's ID. It is the outbox partition key and the
	// delivery's ordering key, so the broker and the subscribers see one order
	// rather than two that were configured separately.
	//
	// Empty means the scope's own identifier, which is per-tenant ordering: two
	// of an account's events publish in the order they were written and two
	// accounts never wait on each other. That is the right default and the wrong
	// guarantee for a resource whose updates must not overtake its creation, so
	// name the subject wherever the call site knows it.
	OrderingKey string
}

// Emitter writes a domain event to the outbox and fans it out to its webhook
// subscribers, in one call, on the caller's transaction.
//
// It is the composition every consumer of these two packages writes, and it is
// here because both halves are only worth anything together: an event published
// with no webhook dispatched, or a webhook dispatched for an event that never
// published, is the split a hand-rolled version keeps producing. Both writes go
// through the transaction the caller is already in, so they commit with the row
// change that caused them or not at all.
type Emitter struct {
	enqueuer   Enqueuer
	dispatcher Dispatcher
	o11y       observability.Observer

	// marshaler is pinned to JSON, for the reason outbox.Writer's is: the relay
	// republishes the stored bytes inside a json.RawMessage and the dispatcher
	// signs them as a JSON body, so any other encoding would be spliced into a
	// JSON message rather than encoded into one.
	marshaler encoding.Marshaler

	emittedCounter        metrics.Int64Counter
	unsubscribableCounter metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read e.o11y.Logger() for the logger this emitter actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	topic string
}

// NewEmitter builds an Emitter over an outbox writer and a dispatcher.
//
// topic is the destination every event this emitter publishes is enqueued
// under. It is fixed at construction rather than per call because a topic is a
// deployment's wiring and an event type is an application's vocabulary, and a
// call site that could name the topic is a call site that can name the wrong
// one.
//
// All three are required. A nil writer, a nil dispatcher or an empty topic each
// refuse here rather than degrading to an emitter that silently does half the
// job — which is precisely the split this type exists to close. A process that
// genuinely has no webhook tables wires no Emitter and calls outbox.Enqueue.
func NewEmitter(enqueuer Enqueuer, dispatcher Dispatcher, topic string, opts ...EmitterOption) (*Emitter, error) {
	if enqueuer == nil {
		return nil, ErrNilEnqueuer
	}

	if dispatcher == nil {
		return nil, ErrNilDispatcher
	}

	if topic == "" {
		return nil, platformerrors.Wrap(outbox.ErrEmptyTopic, "building a webhooks emitter")
	}

	e := &Emitter{
		enqueuer:   enqueuer,
		dispatcher: dispatcher,
		topic:      topic,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}

	e.o11y = observability.NewObserver(serviceName, e.logger, e.tracerProvider)
	e.marshaler = encoding.NewClientEncoder(encoding.ContentTypeJSON,
		encoding.WithLogger(e.o11y.Logger()), encoding.WithTracerProvider(e.tracerProvider))

	mp := metrics.EnsureMetricsProvider(e.metricsProvider)

	var err error
	if e.emittedCounter, err = mp.NewInt64Counter(serviceName + "_events_emitted"); err != nil {
		return nil, platformerrors.Wrap(err, "creating events emitted counter")
	}

	if e.unsubscribableCounter, err = mp.NewInt64Counter(serviceName + "_events_unsubscribable"); err != nil {
		return nil, platformerrors.Wrap(err, "creating unsubscribable events counter")
	}

	return e, nil
}

// Emit publishes one event and dispatches its webhooks, both inside the
// caller's transaction.
//
//	err := client.WithTransaction(ctx, func(tx database.Tx) error {
//		if err := updateOrder(ctx, tx, order); err != nil {
//			return err
//		}
//
//		return emitter.Emit(ctx, tx, tenancy.Of(order.AccountID), &webhooks.Event{
//			EventType:   OrderUpdated,
//			OrderingKey: order.ID,
//			Payload:     order,
//		})
//	})
//
// The row, the outbox message and the dispatch rows are one commit. Unwinding
// that transaction leaves none of the three, which is the property the two
// halves cannot give separately: a publish after a commit is two operations
// against two systems that share no commit, and the row lands while the event
// does not with nothing able to detect it.
//
// The payload is marshaled once and both halves are handed the same bytes, so a
// queue consumer and a webhook subscriber read byte-identical bodies — and the
// bytes that are signed are the bytes that were stored, since re-marshaling
// between the two is exactly how a signature comes to cover something other
// than what was sent.
//
// # The catalog gate
//
// An event type outside the dispatcher's catalog is published to the outbox and
// not dispatched, rather than refused.
//
// Handing it to Dispatch would refuse it, and that refusal would arrive inside
// the transaction that wrote the row the event describes — so an event type
// nothing subscribes to would not fail a webhook, it would fail the order. Two
// things land here and neither may do that: an application's deliberate
// exclusions, the sign-ins and credential changes it publishes internally and no
// subscriber may receive, and a constant that has drifted out of the catalog,
// which is a build-time problem that must not become a runtime outage. A typo'd
// event type is still caught where a human types one, at Register and at
// Subscribe.
//
// The gate reads the dispatcher's own catalog rather than a second copy: a gate
// that could disagree with Dispatch would either drop events the dispatcher
// would have accepted or hand it ones it refuses, and the second of those is the
// failed write this arrangement exists to prevent.
//
// # The scope
//
// The scope is the argument, as it is everywhere else here, and it bounds the
// fan-out exactly as Dispatch's does. It is also the default ordering key, so
// an event that names no subject still gets per-tenant order on both sides.
//
// The event is not written to. An event that named no ID has one minted onto the
// delivery this builds, which is the only place the fan-out was ever going to
// put it.
func (e *Emitter) Emit(ctx context.Context, tx database.Tx, scope tenancy.Scope, event *Event) error {
	ctx, op := e.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(topicKey, e.topic),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "emitting domain event")
	}

	if event == nil {
		return op.Error(ErrNilEvent, "emitting domain event")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "emitting domain event")
	}

	if event.EventType == "" {
		return op.Error(ErrEmptyEventType, "emitting domain event")
	}

	if event.Payload == nil {
		return op.Error(
			platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil domain event payload"),
			"emitting domain event %q", event.EventType,
		)
	}

	op.Set(eventTypeKey, event.EventType.String())

	// Empty means the scope's identifier, which is per-tenant ordering on both
	// sides. One key rather than two: an event whose broker order and whose
	// subscriber order were configured separately is an event whose two
	// audiences can be told two different stories about what happened first.
	//
	// Owner rather than String, because this is a key rather than a log field:
	// String renders the global scope as "<global>", and a global application's
	// events would all share one partition key named after a rendering. The
	// scope was validated above, so the empty identifier here is Global's and
	// not an unset scope's — which is outbox's word for unordered, and the right
	// answer for a deployment with no tenants to keep apart.
	key := event.OrderingKey
	if key == "" {
		key = scope.Owner()
	}

	if key != "" {
		op.Set(orderingKeyKey, key)
	}

	// Marshaled once, before either write, so a payload that cannot be rendered
	// fails before anything has been enqueued rather than between the two halves.
	payload, err := e.marshaler.Marshal(ctx, event.Payload)
	if err != nil {
		return op.Error(err, "marshaling domain event %q", event.EventType)
	}

	// The same bytes reach both. json.RawMessage marshals to itself, so what the
	// writer stores is what was rendered here and what the subscriber is signed
	// over.
	body := json.RawMessage(payload)

	if err = e.enqueuer.Enqueue(ctx, tx, outbox.Message{
		Topic:   e.topic,
		Payload: body,
		Key:     key,
	}); err != nil {
		return op.Error(err, "enqueuing domain event %q", event.EventType)
	}

	// Counted after the enqueue succeeds, but the transaction can still roll back
	// afterwards — so this counts intent to publish, not committed rows, exactly
	// as the two instruments it sits between do.
	e.emittedCounter.Add(ctx, 1, eventTypeAttr(event.EventType))

	if !e.dispatcher.Catalog().Known(event.EventType) {
		// Worth an instrument of its own: an event type an application publishes
		// and no subscriber may receive is either a deliberate exclusion or a
		// constant that fell out of the catalog, and the counter climbing for a
		// type nobody meant to exclude is the only signal that says which.
		e.unsubscribableCounter.Add(ctx, 1, eventTypeAttr(event.EventType))
		op.Set(subscribableKey, false)

		return nil
	}

	op.Set(subscribableKey, true)

	if err = e.dispatcher.Dispatch(ctx, tx, scope, &Delivery{
		ID:        event.ID,
		EventType: event.EventType,
		// Not scoped. It is compared only against other dispatches for the same
		// endpoint, and an endpoint belongs to one scope, so the scope would add
		// nothing but width to an index that is already on the claim path.
		OrderingKey: key,
		Payload:     body,
	}); err != nil {
		return op.Error(err, "dispatching domain event %q", event.EventType)
	}

	return nil
}

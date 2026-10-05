package webhooks

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// orderAudited is an event the application publishes and no subscriber may
// receive, standing in for a sign-in or a credential change.
const orderAudited EventType = "order.audited"

// internalCatalog is testCatalog with one internal event beside it.
var internalCatalog = Catalog{
	orderCreated: {Description: "an order was created"},
	orderUpdated: {Description: "an order was updated"},
	orderAudited: {Description: "an order was read by staff", Internal: true},
}

func TestMerge(T *testing.T) {
	T.Parallel()

	T.Run("unions disjoint catalogs", func(t *testing.T) {
		t.Parallel()

		merged, err := Merge(
			Catalog{orderCreated: {Description: "created"}},
			Catalog{orderUpdated: {Description: "updated"}, orderAudited: {Description: "audited", Internal: true}},
		)
		must.NoError(t, err)

		test.Eq(t, Catalog{
			orderCreated: {Description: "created"},
			orderUpdated: {Description: "updated"},
			orderAudited: {Description: "audited", Internal: true},
		}, merged)
	})

	// The whole reason it exists: maps.Copy lets the later definition win, and
	// the subscriber is told the wrong thing about what it receives.
	T.Run("refuses an event type two catalogs define", func(t *testing.T) {
		t.Parallel()

		merged, err := Merge(
			Catalog{orderCreated: {Description: "the application's"}},
			Catalog{orderUpdated: {Description: "updated"}},
			Catalog{orderCreated: {Description: "a package's"}},
		)
		test.ErrorIs(t, err, ErrDuplicateEventType)
		test.StrContains(t, err.Error(), string(orderCreated))
		test.Nil(t, merged)
	})

	// Two packages naming one event type is the fault whether or not they
	// happen to describe it alike.
	T.Run("refuses identical definitions too", func(t *testing.T) {
		t.Parallel()

		_, err := Merge(
			Catalog{orderCreated: {Description: "created"}},
			Catalog{orderCreated: {Description: "created"}},
		)
		test.ErrorIs(t, err, ErrDuplicateEventType)
	})

	T.Run("a nil catalog contributes nothing", func(t *testing.T) {
		t.Parallel()

		merged, err := Merge(nil, testCatalog, nil)
		must.NoError(t, err)
		test.Eq(t, testCatalog, merged)
	})

	T.Run("no catalogs is an empty one", func(t *testing.T) {
		t.Parallel()

		merged, err := Merge()
		must.NoError(t, err)
		must.NotNil(t, merged)
		test.MapEmpty(t, merged)
	})

	T.Run("does not modify its arguments", func(t *testing.T) {
		t.Parallel()

		first := Catalog{orderCreated: {Description: "created"}}
		second := Catalog{orderUpdated: {Description: "updated"}}

		merged, err := Merge(first, second)
		must.NoError(t, err)

		merged[orderDeleted] = EventDefinition{Description: "deleted"}

		test.MapLen(t, 1, first)
		test.MapLen(t, 1, second)
	})
}

func TestCatalog_Internal(T *testing.T) {
	T.Parallel()

	T.Run("Known reads definition, Subscribable reads delivery", func(t *testing.T) {
		t.Parallel()

		test.True(t, internalCatalog.Known(orderAudited))
		test.False(t, internalCatalog.Subscribable(orderAudited))

		test.True(t, internalCatalog.Known(orderCreated))
		test.True(t, internalCatalog.Subscribable(orderCreated))

		test.False(t, internalCatalog.Known(orderDeleted))
		test.False(t, internalCatalog.Subscribable(orderDeleted))
		test.False(t, Catalog(nil).Subscribable(orderCreated))
	})

	T.Run("EventTypes includes internal events, SubscribableEventTypes does not", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []EventType{orderAudited, orderCreated, orderUpdated}, internalCatalog.EventTypes())
		test.Eq(t, []EventType{orderCreated, orderUpdated}, internalCatalog.SubscribableEventTypes())
		test.SliceEmpty(t, Catalog{}.SubscribableEventTypes())
	})

	// The flag is omitted when unset, so a catalog with no internal events
	// renders exactly as it did before the field existed.
	T.Run("marshals the flag only when set", func(t *testing.T) {
		t.Parallel()

		rendered, err := json.Marshal(internalCatalog)
		must.NoError(t, err)
		test.StrContains(t, string(rendered), `"order.created":{"description":"an order was created"}`)
		test.StrContains(t, string(rendered), `"order.audited":{"description":"an order was read by staff","internal":true}`)

		var decoded Catalog
		must.NoError(t, json.Unmarshal(rendered, &decoded))
		test.Eq(t, internalCatalog, decoded)
	})
}

// The gate at registration and the gate at dispatch read one predicate, so an
// internal event is refused at every one of them, with the same sentinel an
// unknown one gets.
func TestCatalog_InternalGates(T *testing.T) {
	T.Parallel()

	T.Run("Validate refuses an endpoint subscribing to an internal event", func(t *testing.T) {
		t.Parallel()

		endpoint := &Endpoint{
			Scope:         testScope,
			URL:           "https://93.184.216.34/hooks",
			Secret:        Secret{Current: []byte("secret")},
			Subscriptions: SubscribeTo(orderCreated, orderAudited),
		}

		test.ErrorIs(t, endpoint.Validate(t.Context(), internalCatalog, nil), ErrUnknownEventType)

		endpoint.Subscriptions = SubscribeTo(orderCreated)
		test.NoError(t, endpoint.Validate(t.Context(), internalCatalog, nil))
	})

	T.Run("Register refuses it without storing", func(t *testing.T) {
		t.Parallel()

		called := false

		d := newTestDispatcher(t, &fakeStore{
			saveEndpoint: func(context.Context, database.Tx, tenancy.Scope, *Endpoint) (*Endpoint, error) {
				called = true

				return &Endpoint{}, nil
			},
		}, WithCatalog(internalCatalog))

		err := registerErr(t, d, testTx(), testScope, &Endpoint{
			URL:           "https://93.184.216.34/hooks",
			Secret:        Secret{Current: []byte("secret")},
			Subscriptions: SubscribeTo(orderAudited),
		})
		test.ErrorIs(t, err, ErrUnknownEventType)
		test.False(t, called)
	})

	T.Run("Subscribe refuses it without storing", func(t *testing.T) {
		t.Parallel()

		called := false

		d := newTestDispatcher(t, &fakeStore{
			addSubscription: func(context.Context, database.Tx, tenancy.Scope, string, EventType) (*Subscription, error) {
				called = true

				return &Subscription{}, nil
			},
		}, WithCatalog(internalCatalog))

		subscription, err := d.Subscribe(t.Context(), testTx(), testScope, "endpoint-1", orderAudited)
		test.ErrorIs(t, err, ErrUnknownEventType)
		test.Nil(t, subscription)
		test.False(t, called)
	})

	// A property of the type, not of the subscription: an endpoint subscribed
	// before the event was marked internal still has the row, and Dispatch
	// refuses before looking it up.
	T.Run("Dispatch refuses it before resolving subscribers", func(t *testing.T) {
		t.Parallel()

		looked := false

		d := newTestDispatcher(t, &fakeStore{
			endpointsForEvent: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, EventType) ([]*Endpoint, error) {
				looked = true

				return []*Endpoint{{ID: "subscribed-before-it-was-internal", Scope: testScope}}, nil
			},
		}, WithCatalog(internalCatalog))

		err := d.Dispatch(t.Context(), testTx(), testScope, &Delivery{EventType: orderAudited, Payload: testBody})
		test.ErrorIs(t, err, ErrUnknownEventType)
		test.False(t, looked)
	})
}

// countingCounter records how many times it was added to.
type countingCounter struct {
	added int64
}

func (c *countingCounter) Add(_ context.Context, incr int64, _ ...metric.AddOption) {
	c.added += incr
}

func TestEmitter_Internal(T *testing.T) {
	T.Parallel()

	newEmitter := func(t *testing.T, enqueuer Enqueuer, recorder *dispatchRecorder) (*Emitter, *countingCounter) {
		t.Helper()

		e, err := NewEmitter(enqueuer, newTestDispatcher(t, recorder.store(), WithCatalog(internalCatalog)), testTopic)
		must.NoError(t, err)

		counter := &countingCounter{}
		e.unsubscribableCounter = counter

		return e, counter
	}

	T.Run("publishes an internal event without dispatching or counting it", func(t *testing.T) {
		t.Parallel()

		enqueuer := &fakeEnqueuer{}
		recorder := &dispatchRecorder{}
		e, unsubscribable := newEmitter(t, enqueuer, recorder)

		must.NoError(t, e.Emit(t.Context(), testTx(), testScope, &Event{
			EventType: orderAudited,
			Payload:   map[string]string{"id": "order-1"},
		}))

		must.SliceLen(t, 1, enqueuer.got)
		test.EqOp(t, 0, recorder.resolved)
		test.SliceEmpty(t, recorder.deliveries)

		// A deliberate exclusion, said out loud, is not the suspicious kind.
		test.EqOp(t, int64(0), unsubscribable.added)
	})

	T.Run("counts an event the catalog does not list", func(t *testing.T) {
		t.Parallel()

		enqueuer := &fakeEnqueuer{}
		recorder := &dispatchRecorder{}
		e, unsubscribable := newEmitter(t, enqueuer, recorder)

		must.NoError(t, e.Emit(t.Context(), testTx(), testScope, &Event{
			EventType: orderDeleted,
			Payload:   map[string]string{"id": "order-1"},
		}))

		must.SliceLen(t, 1, enqueuer.got)
		test.EqOp(t, 0, recorder.resolved)
		test.EqOp(t, int64(1), unsubscribable.added)
	})

	T.Run("dispatches a subscribable event without counting it", func(t *testing.T) {
		t.Parallel()

		enqueuer := &fakeEnqueuer{}
		recorder := &dispatchRecorder{}
		e, unsubscribable := newEmitter(t, enqueuer, recorder)

		must.NoError(t, e.Emit(t.Context(), testTx(), testScope, &Event{
			EventType: orderCreated,
			Payload:   map[string]string{"id": "order-1"},
		}))

		must.SliceLen(t, 1, recorder.deliveries)
		test.EqOp(t, int64(0), unsubscribable.added)
	})
}

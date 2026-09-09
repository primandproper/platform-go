package webhooks

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// fakeStore is a hand-written Store double. The moq mock in webhooks/mock is
// for external consumers; in-package tests use this because almost every case
// here cares about only two or three methods and a moq struct would need every
// field stubbed to avoid a nil-func panic.
type fakeStore struct {
	saveEndpoint        func(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpoint *Endpoint) error
	getEndpoint         func(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, endpointID string) (*Endpoint, error)
	endpointsForEvent   func(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, eventType EventType) ([]*Endpoint, error)
	enqueue             func(ctx context.Context, q database.SQLQueryExecutor, delivery *Delivery, endpointIDs []string, now time.Time) error
	claim               func(ctx context.Context, now time.Time, limit int, leaseUntil time.Time) ([]ClaimedDispatch, error)
	markDelivered       func(ctx context.Context, dispatchID string, at time.Time) error
	recordFailure       func(ctx context.Context, dispatchID string, attempts int, nextAttempt time.Time, lastErr string, dead bool) error
	recordAttempt       func(ctx context.Context, attempt *Attempt) error
	addSubscription     func(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpointID string, eventType EventType) (*Subscription, error)
	archiveSubscription func(ctx context.Context, tx database.Tx, scope tenancy.Scope, subscriptionID string) error
	requeue             func(ctx context.Context, deliveryID, endpointID string, at time.Time) error
	backlog             func(ctx context.Context) (int64, time.Time, error)
	reap                func(ctx context.Context, before time.Time, limit int) (int64, error)
}

var _ Store = (*fakeStore)(nil)

func (f *fakeStore) SaveEndpoint(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpoint *Endpoint) error {
	if f.saveEndpoint == nil {
		return nil
	}

	return f.saveEndpoint(ctx, tx, scope, endpoint)
}

func (f *fakeStore) GetEndpoint(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, endpointID string) (*Endpoint, error) {
	if f.getEndpoint == nil {
		return &Endpoint{ID: endpointID, Scope: scope}, nil
	}

	return f.getEndpoint(ctx, q, scope, endpointID)
}

func (f *fakeStore) ListEndpoints(context.Context, database.SQLQueryExecutor, tenancy.Scope, *filtering.QueryFilter) (*filtering.QueryFilteredResult[Endpoint], error) {
	return &filtering.QueryFilteredResult[Endpoint]{}, nil
}

func (f *fakeStore) ArchiveEndpoint(context.Context, database.Tx, tenancy.Scope, string) error {
	return nil
}

func (f *fakeStore) AddSubscription(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpointID string, eventType EventType) (*Subscription, error) {
	if f.addSubscription == nil {
		return &Subscription{ID: "sub-" + endpointID, EndpointID: endpointID, EventType: eventType}, nil
	}

	return f.addSubscription(ctx, tx, scope, endpointID, eventType)
}

func (f *fakeStore) GetSubscription(_ context.Context, _ database.SQLQueryExecutor, _ tenancy.Scope, subscriptionID string) (*Subscription, error) {
	return &Subscription{ID: subscriptionID}, nil
}

func (f *fakeStore) ListSubscriptions(context.Context, database.SQLQueryExecutor, tenancy.Scope, string, *filtering.QueryFilter) (*filtering.QueryFilteredResult[Subscription], error) {
	return &filtering.QueryFilteredResult[Subscription]{}, nil
}

func (f *fakeStore) ArchiveSubscription(ctx context.Context, tx database.Tx, scope tenancy.Scope, subscriptionID string) error {
	if f.archiveSubscription == nil {
		return nil
	}

	return f.archiveSubscription(ctx, tx, scope, subscriptionID)
}

func (f *fakeStore) EndpointsForEvent(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, eventType EventType) ([]*Endpoint, error) {
	if f.endpointsForEvent == nil {
		return nil, nil
	}

	return f.endpointsForEvent(ctx, q, scope, eventType)
}

func (f *fakeStore) Enqueue(ctx context.Context, q database.Tx, delivery *Delivery, endpointIDs []string, now time.Time) error {
	if f.enqueue == nil {
		return nil
	}

	return f.enqueue(ctx, q, delivery, endpointIDs, now)
}

func (f *fakeStore) Claim(ctx context.Context, now time.Time, limit int, leaseUntil time.Time) ([]ClaimedDispatch, error) {
	if f.claim == nil {
		return nil, nil
	}

	return f.claim(ctx, now, limit, leaseUntil)
}

func (f *fakeStore) MarkDelivered(ctx context.Context, dispatchID string, at time.Time) error {
	if f.markDelivered == nil {
		return nil
	}

	return f.markDelivered(ctx, dispatchID, at)
}

func (f *fakeStore) RecordFailure(ctx context.Context, dispatchID string, attempts int, nextAttempt time.Time, lastErr string, dead bool) error {
	if f.recordFailure == nil {
		return nil
	}

	return f.recordFailure(ctx, dispatchID, attempts, nextAttempt, lastErr, dead)
}

func (f *fakeStore) RecordAttempt(ctx context.Context, attempt *Attempt) error {
	if f.recordAttempt == nil {
		return nil
	}

	return f.recordAttempt(ctx, attempt)
}

func (f *fakeStore) ListAttempts(context.Context, database.SQLQueryExecutor, tenancy.Scope, string, *filtering.QueryFilter) (*filtering.QueryFilteredResult[Attempt], error) {
	return &filtering.QueryFilteredResult[Attempt]{}, nil
}

func (f *fakeStore) Requeue(ctx context.Context, deliveryID, endpointID string, at time.Time) error {
	if f.requeue == nil {
		return nil
	}

	return f.requeue(ctx, deliveryID, endpointID, at)
}

func (f *fakeStore) Backlog(ctx context.Context) (int64, time.Time, error) {
	if f.backlog == nil {
		return 0, time.Time{}, nil
	}

	return f.backlog(ctx)
}

func (f *fakeStore) Reap(ctx context.Context, before time.Time, limit int) (int64, error) {
	if f.reap == nil {
		return 0, nil
	}

	return f.reap(ctx, before, limit)
}

// stubExecutor is a non-nil database.SQLQueryExecutor for tests that only need
// Dispatch to see one. Nothing here is called: the Store double intercepts
// every query.
type stubExecutor struct{}

var _ database.SQLQueryExecutor = (*stubExecutor)(nil)

func (*stubExecutor) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, nil
}

func (*stubExecutor) PrepareContext(context.Context, string) (*sql.Stmt, error) { return nil, nil }

func (*stubExecutor) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, nil
}

func (*stubExecutor) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }

func newTestDispatcher(t *testing.T, store Store, opts ...DispatcherOption) Dispatcher {
	t.Helper()

	d, err := NewDispatcher(store, &stubExecutor{}, append([]DispatcherOption{WithCatalog(testCatalog)}, opts...)...)
	must.NoError(t, err)

	return d
}

// testTx is the transaction the dispatcher cases hand to a write.
//
// Nothing executes through it — the Store double intercepts every statement —
// and what it is here to do is satisfy the type, which is the whole point of the
// port: a consumer write that cannot be called without one.
func testTx() database.Tx { return database.NewTxForTesting(&stubExecutor{}) }

func TestNewDispatcher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		d, err := NewDispatcher(&fakeStore{}, &stubExecutor{}, WithCatalog(testCatalog))
		must.NoError(t, err)
		test.NotNil(t, d)
	})

	T.Run("nil store", func(t *testing.T) {
		t.Parallel()

		_, err := NewDispatcher(nil, &stubExecutor{})
		test.ErrorIs(t, err, ErrNilStore)
	})

	// Replay's precondition read has to run on something, and there is no
	// fallback for it to reach for: the store's handle is the machinery's, and
	// this method answers an operator.
	T.Run("nil reader", func(t *testing.T) {
		t.Parallel()

		_, err := NewDispatcher(&fakeStore{}, nil)
		test.ErrorIs(t, err, ErrNilExecutor)
	})

	T.Run("nil options are skipped", func(t *testing.T) {
		t.Parallel()

		var absent DispatcherOption

		_, err := NewDispatcher(&fakeStore{}, &stubExecutor{}, absent, WithCatalog(testCatalog))
		test.NoError(t, err)
	})
}

func TestDispatcher_Register(T *testing.T) {
	T.Parallel()

	// No Scope: the scope is the call's argument now, and an endpoint that names
	// none adopts it. The cases that are about the scope set the field.
	valid := func() *Endpoint {
		return &Endpoint{
			URL:           "https://93.184.216.34/hooks",
			Secret:        Secret{Current: []byte("secret")},
			Subscriptions: SubscribeTo(orderCreated),
		}
	}

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		var saved *Endpoint

		d := newTestDispatcher(t, &fakeStore{
			saveEndpoint: func(_ context.Context, _ database.Tx, _ tenancy.Scope, endpoint *Endpoint) error {
				saved = endpoint

				return nil
			},
		})

		endpoint := valid()
		must.NoError(t, d.Register(t.Context(), testTx(), testScope, endpoint))

		must.NotNil(t, saved)
		test.NotEqOp(t, "", saved.ID)
		test.EqOp(t, DefaultContentType, saved.ContentType)
	})

	T.Run("preserves a caller-supplied ID", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{})

		endpoint := valid()
		endpoint.ID = "chosen"

		must.NoError(t, d.Register(t.Context(), testTx(), testScope, endpoint))
		test.EqOp(t, "chosen", endpoint.ID)
	})

	// The SSRF case, at the layer that matters: an unvalidated endpoint must
	// never reach the store.
	T.Run("refuses a non-routable URL without storing it", func(t *testing.T) {
		t.Parallel()

		saved := false

		d := newTestDispatcher(t, &fakeStore{
			saveEndpoint: func(context.Context, database.Tx, tenancy.Scope, *Endpoint) error {
				saved = true

				return nil
			},
		})

		endpoint := valid()
		endpoint.URL = "https://169.254.169.254/latest/meta-data/"

		test.ErrorIs(t, d.Register(t.Context(), testTx(), testScope, endpoint), ErrDisallowedEndpointHost)
		test.False(t, saved)
	})

	T.Run("refuses an unknown event type", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{})

		endpoint := valid()
		endpoint.Subscriptions = SubscribeTo(orderExploded)

		test.ErrorIs(t, d.Register(t.Context(), testTx(), testScope, endpoint), ErrUnknownEventType)
	})

	// The same shape as the SSRF case, for the same reason: a registration that
	// says nothing about whose endpoint it is must not reach the store, because
	// nothing downstream can recover the account it was meant for.
	T.Run("refuses a call with no scope without storing it", func(t *testing.T) {
		t.Parallel()

		saved := false

		d := newTestDispatcher(t, &fakeStore{
			saveEndpoint: func(context.Context, database.Tx, tenancy.Scope, *Endpoint) error {
				saved = true

				return nil
			},
		})

		test.ErrorIs(t, d.Register(t.Context(), testTx(), tenancy.Scope{}, valid()), ErrNoScope)
		test.False(t, saved)
	})

	// An endpoint that names no scope is the ordinary case: the caller says whose
	// it is, and the value written back is what the statement bound.
	T.Run("an endpoint naming no scope adopts the argument", func(t *testing.T) {
		t.Parallel()

		var saved *Endpoint

		d := newTestDispatcher(t, &fakeStore{
			saveEndpoint: func(_ context.Context, _ database.Tx, _ tenancy.Scope, endpoint *Endpoint) error {
				saved = endpoint

				return nil
			},
		})

		endpoint := valid()
		must.NoError(t, d.Register(t.Context(), testTx(), otherScope, endpoint))

		must.NotNil(t, saved)
		test.EqOp(t, otherScope, saved.Scope)
	})

	// The mix-up the argument exists to catch: a caller holding one tenant's
	// endpoint and registering it into another. Refused rather than corrected,
	// and refused before the URL is even checked.
	T.Run("refuses an endpoint naming another scope without storing it", func(t *testing.T) {
		t.Parallel()

		saved := false

		d := newTestDispatcher(t, &fakeStore{
			saveEndpoint: func(context.Context, database.Tx, tenancy.Scope, *Endpoint) error {
				saved = true

				return nil
			},
		})

		endpoint := valid()
		endpoint.Scope = otherScope

		test.ErrorIs(t, d.Register(t.Context(), testTx(), testScope, endpoint), ErrScopeMismatch)
		test.False(t, saved)
	})

	// The transaction is not optional: a registration with nothing to commit
	// with is the shape the port exists to make impossible.
	T.Run("nil transaction", func(t *testing.T) {
		t.Parallel()

		saved := false

		d := newTestDispatcher(t, &fakeStore{
			saveEndpoint: func(context.Context, database.Tx, tenancy.Scope, *Endpoint) error {
				saved = true

				return nil
			},
		})

		test.ErrorIs(t, d.Register(t.Context(), nil, testScope, valid()), ErrNilExecutor)
		test.False(t, saved)
	})

	T.Run("nil endpoint", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, newTestDispatcher(t, &fakeStore{}).Register(t.Context(), testTx(), testScope, nil), ErrNilEndpoint)
	})
}

func TestDispatcher_Dispatch(T *testing.T) {
	T.Parallel()

	subscribed := []*Endpoint{{ID: "endpoint-1", Scope: testScope}, {ID: "endpoint-2", Scope: testScope}}

	T.Run("fans out to every subscriber", func(t *testing.T) {
		t.Parallel()

		var (
			enqueuedIDs []string
			enqueued    *Delivery
		)

		d := newTestDispatcher(t, &fakeStore{
			endpointsForEvent: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, EventType) ([]*Endpoint, error) {
				return subscribed, nil
			},
			enqueue: func(_ context.Context, _ database.SQLQueryExecutor, delivery *Delivery, endpointIDs []string, _ time.Time) error {
				enqueued = delivery
				enqueuedIDs = endpointIDs

				return nil
			},
		})

		delivery := &Delivery{Scope: testScope, EventType: "order.created", Payload: testBody, OrderingKey: "order-7"}

		must.NoError(t, d.Dispatch(t.Context(), database.NewTxForTesting(&stubExecutor{}), delivery))

		test.Eq(t, []string{"endpoint-1", "endpoint-2"}, enqueuedIDs)
		must.NotNil(t, enqueued)
		test.NotEqOp(t, "", enqueued.ID)
		test.EqOp(t, "order-7", enqueued.OrderingKey)
	})

	// The scope the store is asked for is the delivery's, not a default. Get this
	// wrong and one account's event is fanned out to another's subscribers, which
	// is the entire failure this dimension exists to prevent.
	T.Run("resolves subscribers within the delivery's scope", func(t *testing.T) {
		t.Parallel()

		var asked tenancy.Scope

		d := newTestDispatcher(t, &fakeStore{
			endpointsForEvent: func(_ context.Context, _ database.SQLQueryExecutor, scope tenancy.Scope, _ EventType) ([]*Endpoint, error) {
				asked = scope

				return subscribed, nil
			},
		})

		must.NoError(t, d.Dispatch(t.Context(), database.NewTxForTesting(&stubExecutor{}),
			&Delivery{Scope: otherScope, EventType: "order.created", Payload: testBody}))

		test.EqOp(t, otherScope, asked)
	})

	// Refused rather than read as "every subscriber": the convenient reading is
	// the one that leaks a payload to every other tenant.
	T.Run("rejects a delivery with no scope before touching the store", func(t *testing.T) {
		t.Parallel()

		looked := false

		d := newTestDispatcher(t, &fakeStore{
			endpointsForEvent: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, EventType) ([]*Endpoint, error) {
				looked = true

				return subscribed, nil
			},
		})

		err := d.Dispatch(t.Context(), database.NewTxForTesting(&stubExecutor{}), &Delivery{EventType: "order.created", Payload: testBody})

		test.ErrorIs(t, err, ErrNoScope)
		test.False(t, looked)
	})

	// An application whose events are global has to stay expressible without
	// inventing a tenant it does not have.
	T.Run("the global scope dispatches", func(t *testing.T) {
		t.Parallel()

		var asked tenancy.Scope

		d := newTestDispatcher(t, &fakeStore{
			endpointsForEvent: func(_ context.Context, _ database.SQLQueryExecutor, scope tenancy.Scope, _ EventType) ([]*Endpoint, error) {
				asked = scope

				return nil, nil
			},
		})

		must.NoError(t, d.Dispatch(t.Context(), database.NewTxForTesting(&stubExecutor{}),
			&Delivery{Scope: tenancy.Global(), EventType: "order.created", Payload: testBody}))

		test.EqOp(t, tenancy.Global(), asked)
	})

	// The common case for most event types most of the time. Making it an error
	// would have every publisher branch on it.
	T.Run("an event nobody subscribes to writes nothing", func(t *testing.T) {
		t.Parallel()

		enqueued := false

		d := newTestDispatcher(t, &fakeStore{
			endpointsForEvent: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, EventType) ([]*Endpoint, error) {
				return nil, nil
			},
			enqueue: func(context.Context, database.SQLQueryExecutor, *Delivery, []string, time.Time) error {
				enqueued = true

				return nil
			},
		})

		test.NoError(t, d.Dispatch(t.Context(), database.NewTxForTesting(&stubExecutor{}),
			&Delivery{Scope: testScope, EventType: "order.created", Payload: testBody}))
		test.False(t, enqueued)
	})

	T.Run("preserves a caller-supplied delivery ID", func(t *testing.T) {
		t.Parallel()

		var enqueued *Delivery

		d := newTestDispatcher(t, &fakeStore{
			endpointsForEvent: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, EventType) ([]*Endpoint, error) {
				return subscribed, nil
			},
			enqueue: func(_ context.Context, _ database.SQLQueryExecutor, delivery *Delivery, _ []string, _ time.Time) error {
				enqueued = delivery

				return nil
			},
		})

		must.NoError(t, d.Dispatch(t.Context(), database.NewTxForTesting(&stubExecutor{}),
			&Delivery{Scope: testScope, ID: "chosen", EventType: "order.created", Payload: testBody}))

		must.NotNil(t, enqueued)
		test.EqOp(t, "chosen", enqueued.ID)
	})

	T.Run("rejects an unknown event type before touching the store", func(t *testing.T) {
		t.Parallel()

		looked := false

		d := newTestDispatcher(t, &fakeStore{
			endpointsForEvent: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, EventType) ([]*Endpoint, error) {
				looked = true

				return nil, nil
			},
		})

		err := d.Dispatch(t.Context(), database.NewTxForTesting(&stubExecutor{}), &Delivery{Scope: testScope, EventType: "order.exploded", Payload: testBody})

		test.ErrorIs(t, err, ErrUnknownEventType)
		test.False(t, looked)
	})

	T.Run("rejects an empty payload", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{})

		test.Error(t, d.Dispatch(t.Context(), database.NewTxForTesting(&stubExecutor{}), &Delivery{Scope: testScope, EventType: "order.created"}))
	})

	// The transactional guarantee is the executor. Without one there is nothing
	// for the deliveries to commit with.
	T.Run("nil executor", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{})

		err := d.Dispatch(t.Context(), nil, &Delivery{Scope: testScope, EventType: "order.created", Payload: testBody})
		test.ErrorIs(t, err, ErrNilExecutor)
	})

	T.Run("nil delivery", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t,
			newTestDispatcher(t, &fakeStore{}).Dispatch(t.Context(), database.NewTxForTesting(&stubExecutor{}), nil),
			ErrNilDelivery,
		)
	})

	T.Run("surfaces a store failure", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("blammo")

		d := newTestDispatcher(t, &fakeStore{
			endpointsForEvent: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, EventType) ([]*Endpoint, error) {
				return subscribed, nil
			},
			enqueue: func(context.Context, database.SQLQueryExecutor, *Delivery, []string, time.Time) error {
				return expected
			},
		})

		err := d.Dispatch(t.Context(), database.NewTxForTesting(&stubExecutor{}), &Delivery{Scope: testScope, EventType: "order.created", Payload: testBody})
		test.ErrorIs(t, err, expected)
	})

	// A dispatcher built without WithCatalog rejects everything, which is the
	// deliberate failure mode described on the option.
	T.Run("an empty catalog rejects every event", func(t *testing.T) {
		t.Parallel()

		d, err := NewDispatcher(&fakeStore{}, &stubExecutor{})
		must.NoError(t, err)

		test.ErrorIs(t,
			d.Dispatch(t.Context(), database.NewTxForTesting(&stubExecutor{}), &Delivery{Scope: testScope, EventType: "order.created", Payload: testBody}),
			ErrUnknownEventType,
		)
	})
}

// errStoreFailed stands in for whatever a Store's backing went wrong with. The
// dispatcher's contract is that it surfaces it rather than translating it.
var errStoreFailed = platformerrors.New("store failed")

func TestDispatcher_Subscribe(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{})

		subscription, err := d.Subscribe(t.Context(), testTx(), testScope, "endpoint-1", orderCreated)
		must.NoError(t, err)
		test.EqOp(t, orderCreated, subscription.EventType)
	})

	// The reason this goes through the dispatcher rather than straight to the
	// store: a subscription to an event type nothing publishes is accepted
	// silently by any storage layer and then never fires.
	T.Run("refuses an unknown event type without storing it", func(t *testing.T) {
		t.Parallel()

		called := false

		d := newTestDispatcher(t, &fakeStore{
			addSubscription: func(context.Context, database.Tx, tenancy.Scope, string, EventType) (*Subscription, error) {
				called = true

				return &Subscription{}, nil
			},
		})

		_, err := d.Subscribe(t.Context(), testTx(), testScope, "endpoint-1", orderExploded)
		test.ErrorIs(t, err, ErrUnknownEventType)
		test.False(t, called)
	})

	T.Run("refuses an empty event type", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{})

		_, err := d.Subscribe(t.Context(), testTx(), testScope, "endpoint-1", "")
		test.ErrorIs(t, err, ErrEmptyEventType)
	})

	T.Run("refuses a scope that names nobody", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{})

		_, err := d.Subscribe(t.Context(), testTx(), tenancy.Scope{}, "endpoint-1", orderCreated)
		test.ErrorIs(t, err, ErrNoScope)
	})

	T.Run("refuses an empty endpoint ID", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{})

		_, err := d.Subscribe(t.Context(), testTx(), testScope, "", orderCreated)
		test.ErrorIs(t, err, platformerrors.ErrInvalidIDProvided)
	})

	T.Run("surfaces a store failure", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{
			addSubscription: func(context.Context, database.Tx, tenancy.Scope, string, EventType) (*Subscription, error) {
				return nil, errStoreFailed
			},
		})

		_, err := d.Subscribe(t.Context(), testTx(), testScope, "endpoint-1", orderCreated)
		test.ErrorIs(t, err, errStoreFailed)
	})
}

func TestDispatcher_Unsubscribe(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		var archived string

		d := newTestDispatcher(t, &fakeStore{
			archiveSubscription: func(_ context.Context, _ database.Tx, _ tenancy.Scope, subscriptionID string) error {
				archived = subscriptionID

				return nil
			},
		})

		must.NoError(t, d.Unsubscribe(t.Context(), testTx(), testScope, "subscription-1"))
		test.EqOp(t, "subscription-1", archived)
	})

	// Deliberately not gated on the catalog: an event type can be withdrawn from
	// an application's catalog, and the subscriptions to it are exactly what has
	// to remain retirable afterwards.
	T.Run("retires a subscription to an event type the catalog no longer holds", func(t *testing.T) {
		t.Parallel()

		d, err := NewDispatcher(&fakeStore{}, &stubExecutor{}, WithCatalog(Catalog{}))
		must.NoError(t, err)

		test.NoError(t, d.Unsubscribe(t.Context(), testTx(), testScope, "subscription-1"))
	})

	T.Run("refuses a scope that names nobody", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{})

		test.ErrorIs(t, d.Unsubscribe(t.Context(), testTx(), tenancy.Scope{}, "subscription-1"), ErrNoScope)
	})

	T.Run("refuses an empty subscription ID", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{})

		test.ErrorIs(t, d.Unsubscribe(t.Context(), testTx(), testScope, ""), platformerrors.ErrInvalidIDProvided)
	})

	T.Run("surfaces a store failure", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{
			archiveSubscription: func(context.Context, database.Tx, tenancy.Scope, string) error {
				return errStoreFailed
			},
		})

		test.ErrorIs(t, d.Unsubscribe(t.Context(), testTx(), testScope, "subscription-1"), errStoreFailed)
	})
}

func TestDispatcher_Replay(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		var gotDelivery, gotEndpoint string

		d := newTestDispatcher(t, &fakeStore{
			requeue: func(_ context.Context, deliveryID, endpointID string, _ time.Time) error {
				gotDelivery, gotEndpoint = deliveryID, endpointID

				return nil
			},
		})

		must.NoError(t, d.Replay(t.Context(), testScope, "delivery-1", "endpoint-1"))

		test.EqOp(t, "delivery-1", gotDelivery)
		test.EqOp(t, "endpoint-1", gotEndpoint)
	})

	// An operator replaying to a disabled endpoint is told why nothing happened,
	// rather than watching a row sit claimable and never delivered.
	T.Run("refuses a disabled endpoint", func(t *testing.T) {
		t.Parallel()

		requeued := false

		d := newTestDispatcher(t, &fakeStore{
			getEndpoint: func(_ context.Context, _ database.SQLQueryExecutor, _ tenancy.Scope, endpointID string) (*Endpoint, error) {
				return &Endpoint{ID: endpointID, Scope: testScope, Disabled: true}, nil
			},
			requeue: func(context.Context, string, string, time.Time) error {
				requeued = true

				return nil
			},
		})

		test.ErrorIs(t, d.Replay(t.Context(), testScope, "delivery-1", "endpoint-1"), ErrEndpointDisabled)
		test.False(t, requeued)
	})

	T.Run("surfaces a missing delivery", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{
			requeue: func(context.Context, string, string, time.Time) error {
				return ErrDeliveryNotFound
			},
		})

		test.ErrorIs(t, d.Replay(t.Context(), testScope, "nope", "endpoint-1"), ErrDeliveryNotFound)
	})

	// The scope is established on the endpoint, which is read within it: an
	// operator replaying somebody else's delivery is refused by the read, not by
	// the requeue.
	T.Run("reads the endpoint in the scope it was given", func(t *testing.T) {
		t.Parallel()

		var asked tenancy.Scope

		d := newTestDispatcher(t, &fakeStore{
			getEndpoint: func(_ context.Context, _ database.SQLQueryExecutor, scope tenancy.Scope, endpointID string) (*Endpoint, error) {
				asked = scope

				return &Endpoint{ID: endpointID, Scope: scope}, nil
			},
		})

		must.NoError(t, d.Replay(t.Context(), otherScope, "delivery-1", "endpoint-1"))
		test.EqOp(t, otherScope, asked)
	})

	T.Run("refuses a scope that names nobody", func(t *testing.T) {
		t.Parallel()

		requeued := false

		d := newTestDispatcher(t, &fakeStore{
			requeue: func(context.Context, string, string, time.Time) error {
				requeued = true

				return nil
			},
		})

		test.ErrorIs(t, d.Replay(t.Context(), tenancy.Scope{}, "delivery-1", "endpoint-1"), ErrNoScope)
		test.False(t, requeued)
	})

	// The read runs on the executor the dispatcher was built with rather than on
	// one the caller names, because the requeue behind it commits on its own: a
	// check against a caller's uncommitted state would be a precondition that
	// could be rolled back out from under a replay that had already happened.
	T.Run("reads the endpoint on the executor it was built with", func(t *testing.T) {
		t.Parallel()

		reader := &stubExecutor{}

		var asked database.SQLQueryExecutor

		store := &fakeStore{
			getEndpoint: func(_ context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, endpointID string) (*Endpoint, error) {
				asked = q

				return &Endpoint{ID: endpointID, Scope: scope}, nil
			},
		}

		d, err := NewDispatcher(store, reader, WithCatalog(testCatalog))
		must.NoError(t, err)

		must.NoError(t, d.Replay(t.Context(), testScope, "delivery-1", "endpoint-1"))
		test.EqOp(t, database.SQLQueryExecutor(reader), asked)
	})

	T.Run("empty identifiers", func(t *testing.T) {
		t.Parallel()

		d := newTestDispatcher(t, &fakeStore{})

		test.ErrorIs(t, d.Replay(t.Context(), testScope, "", "endpoint-1"), platformerrors.ErrInvalidIDProvided)
		test.ErrorIs(t, d.Replay(t.Context(), testScope, "delivery-1", ""), platformerrors.ErrInvalidIDProvided)
	})
}

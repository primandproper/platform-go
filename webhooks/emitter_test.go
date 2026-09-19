package webhooks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/outbox"
	outboxmigrations "github.com/primandproper/platform-go/v14/outbox/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testTopic is the outbox topic the emitter cases publish under.
const testTopic = "domain.events"

// fakeEnqueuer is a hand-written Enqueuer double, recording what an Emit handed
// the outbox so a case can assert on the message rather than on a count.
type fakeEnqueuer struct {
	enqueue func(ctx context.Context, tx database.Tx, msgs ...outbox.Message) error
	got     []outbox.Message
	calls   int
}

var _ Enqueuer = (*fakeEnqueuer)(nil)

func (f *fakeEnqueuer) Enqueue(ctx context.Context, tx database.Tx, msgs ...outbox.Message) error {
	f.calls++
	f.got = append(f.got, msgs...)

	if f.enqueue == nil {
		return nil
	}

	return f.enqueue(ctx, tx, msgs...)
}

// dispatchRecorder is a Store double that records the deliveries a Dispatch
// reached, with one subscriber standing in for the fan-out.
//
// The dispatcher under it is the real StoreDispatcher, because the catalog gate
// this type exists to test reads that dispatcher's own catalog: a double for
// the Dispatcher seam would be a second copy of exactly the fact the gate is
// meant to share.
type dispatchRecorder struct {
	fail       error
	deliveries []*Delivery
	resolved   int
}

func (r *dispatchRecorder) store() *fakeStore {
	return &fakeStore{
		endpointsForEvent: func(_ context.Context, _ database.SQLQueryExecutor, scope tenancy.Scope, _ EventType) ([]*Endpoint, error) {
			r.resolved++

			if r.fail != nil {
				return nil, r.fail
			}

			return []*Endpoint{{ID: "endpoint-1", Scope: scope}}, nil
		},
		enqueue: func(_ context.Context, _ database.SQLQueryExecutor, delivery *Delivery, _ []string, _ time.Time) error {
			r.deliveries = append(r.deliveries, delivery)

			return nil
		},
	}
}

// newTestEmitter builds an Emitter over a recorder and a real dispatcher
// carrying testCatalog.
func newTestEmitter(t *testing.T, enqueuer Enqueuer, recorder *dispatchRecorder, opts ...EmitterOption) *Emitter {
	t.Helper()

	e, err := NewEmitter(enqueuer, newTestDispatcher(t, recorder.store()), testTopic, opts...)
	must.NoError(t, err)

	return e
}

func TestNewEmitter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e, err := NewEmitter(&fakeEnqueuer{}, newTestDispatcher(t, &fakeStore{}), testTopic)
		must.NoError(t, err)
		must.NotNil(t, e)
		test.EqOp(t, testTopic, e.topic)
	})

	T.Run("refuses a nil enqueuer", func(t *testing.T) {
		t.Parallel()

		e, err := NewEmitter(nil, newTestDispatcher(t, &fakeStore{}), testTopic)
		test.Nil(t, e)
		test.ErrorIs(t, err, ErrNilEnqueuer)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("refuses a nil dispatcher", func(t *testing.T) {
		t.Parallel()

		e, err := NewEmitter(&fakeEnqueuer{}, nil, testTopic)
		test.Nil(t, e)
		test.ErrorIs(t, err, ErrNilDispatcher)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	// An emitter with no topic would enqueue nothing the relay could route, and
	// outbox refuses the message later — well after the transaction it was
	// supposed to ride is open.
	T.Run("refuses an empty topic", func(t *testing.T) {
		t.Parallel()

		e, err := NewEmitter(&fakeEnqueuer{}, newTestDispatcher(t, &fakeStore{}), "")
		test.Nil(t, e)
		test.ErrorIs(t, err, outbox.ErrEmptyTopic)
	})

	T.Run("tolerates a nil option", func(t *testing.T) {
		t.Parallel()

		var absent EmitterOption

		e, err := NewEmitter(&fakeEnqueuer{}, newTestDispatcher(t, &fakeStore{}), testTopic, absent)
		must.NoError(t, err)
		must.NotNil(t, e)
	})
}

func TestEmitterOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithEmitterLogger", func(t *testing.T) {
		t.Parallel()

		e := &Emitter{}

		WithEmitterLogger(logging.EnsureLogger(nil))(e)
		test.NotNil(t, e.logger)
	})

	T.Run("WithEmitterTracerProvider", func(t *testing.T) {
		t.Parallel()

		e := &Emitter{}

		provider := tracingnoop.NewTracerProvider()
		WithEmitterTracerProvider(provider)(e)
		test.True(t, e.tracerProvider == provider)
	})

	T.Run("WithEmitterMetricsProvider", func(t *testing.T) {
		t.Parallel()

		e := &Emitter{}

		WithEmitterMetricsProvider(metrics.EnsureMetricsProvider(nil))(e)
		test.NotNil(t, e.metricsProvider)
	})
}

func TestEmitter_Emit(T *testing.T) {
	T.Parallel()

	T.Run("enqueues and dispatches in one call", func(t *testing.T) {
		t.Parallel()

		enqueuer := &fakeEnqueuer{}
		recorder := &dispatchRecorder{}

		must.NoError(t, newTestEmitter(t, enqueuer, recorder).Emit(t.Context(), testTx(), testScope, &Event{
			EventType:   orderCreated,
			OrderingKey: "order-1",
			Payload:     map[string]string{"id": "order-1"},
		}))

		must.SliceLen(t, 1, enqueuer.got)
		test.EqOp(t, testTopic, enqueuer.got[0].Topic)
		test.EqOp(t, "order-1", enqueuer.got[0].Key)

		must.SliceLen(t, 1, recorder.deliveries)
		test.EqOp(t, orderCreated, recorder.deliveries[0].EventType)
		test.EqOp(t, "order-1", recorder.deliveries[0].OrderingKey)
		test.EqOp(t, testScope, recorder.deliveries[0].Scope)
	})

	// The whole point of marshaling once. A queue consumer and a webhook
	// subscriber read the same bytes, and the bytes a subscriber verifies the
	// signature over are the bytes that were stored.
	T.Run("hands both halves the same bytes", func(t *testing.T) {
		t.Parallel()

		enqueuer := &fakeEnqueuer{}
		recorder := &dispatchRecorder{}

		must.NoError(t, newTestEmitter(t, enqueuer, recorder).Emit(t.Context(), testTx(), testScope, &Event{
			EventType: orderCreated,
			Payload:   map[string]string{"id": "order-1"},
		}))

		must.SliceLen(t, 1, enqueuer.got)
		must.SliceLen(t, 1, recorder.deliveries)

		stored, ok := enqueuer.got[0].Payload.(json.RawMessage)
		must.True(t, ok, must.Sprintf("outbox payload is %T, want json.RawMessage", enqueuer.got[0].Payload))

		test.Eq(t, []byte(stored), []byte(recorder.deliveries[0].Payload))
		test.EqOp(t, `{"id":"order-1"}`, string(stored))
	})

	// The ruling's gate, stated as a test: an event type nothing may subscribe
	// to is still published, and the dispatcher is never asked.
	T.Run("publishes an event outside the catalog without dispatching it", func(t *testing.T) {
		t.Parallel()

		enqueuer := &fakeEnqueuer{}
		recorder := &dispatchRecorder{}

		must.NoError(t, newTestEmitter(t, enqueuer, recorder).Emit(t.Context(), testTx(), testScope, &Event{
			EventType: orderDeleted,
			Payload:   map[string]string{"id": "order-1"},
		}))

		must.SliceLen(t, 1, enqueuer.got)
		test.SliceEmpty(t, recorder.deliveries)

		// Not merely "no delivery": the dispatcher was not reached at all, which
		// is what keeps its refusal out of the caller's transaction.
		test.EqOp(t, 0, recorder.resolved)
	})

	// An event with no subject still gets per-tenant order on both sides, and
	// one key rather than two is what keeps the broker's order and the
	// subscribers' the same order.
	T.Run("defaults the ordering key to the scope", func(t *testing.T) {
		t.Parallel()

		enqueuer := &fakeEnqueuer{}
		recorder := &dispatchRecorder{}

		must.NoError(t, newTestEmitter(t, enqueuer, recorder).Emit(t.Context(), testTx(), testScope, &Event{
			EventType: orderCreated,
			Payload:   map[string]string{"id": "order-1"},
		}))

		must.SliceLen(t, 1, enqueuer.got)
		must.SliceLen(t, 1, recorder.deliveries)
		test.EqOp(t, testScope.Owner(), enqueuer.got[0].Key)
		test.EqOp(t, testScope.Owner(), recorder.deliveries[0].OrderingKey)
	})

	// A global application has no tenant identifier to order by, and an empty
	// key is outbox's word for unordered rather than a key everything shares.
	T.Run("leaves the ordering key empty in the global scope", func(t *testing.T) {
		t.Parallel()

		enqueuer := &fakeEnqueuer{}
		recorder := &dispatchRecorder{}

		must.NoError(t, newTestEmitter(t, enqueuer, recorder).Emit(t.Context(), testTx(), tenancy.Global(), &Event{
			EventType: orderCreated,
			Payload:   map[string]string{"id": "order-1"},
		}))

		must.SliceLen(t, 1, enqueuer.got)
		test.EqOp(t, "", enqueuer.got[0].Key)
	})

	T.Run("carries the caller's delivery ID", func(t *testing.T) {
		t.Parallel()

		enqueuer := &fakeEnqueuer{}
		recorder := &dispatchRecorder{}

		must.NoError(t, newTestEmitter(t, enqueuer, recorder).Emit(t.Context(), testTx(), testScope, &Event{
			ID:        "delivery-1",
			EventType: orderCreated,
			Payload:   map[string]string{"id": "order-1"},
		}))

		must.SliceLen(t, 1, recorder.deliveries)
		test.EqOp(t, "delivery-1", recorder.deliveries[0].ID)
	})

	// The event is the caller's value and is left as they wrote it, the way
	// Register leaves an Endpoint.
	T.Run("does not write to the event", func(t *testing.T) {
		t.Parallel()

		event := &Event{EventType: orderCreated, Payload: map[string]string{"id": "order-1"}}

		must.NoError(t, newTestEmitter(t, &fakeEnqueuer{}, &dispatchRecorder{}).
			Emit(t.Context(), testTx(), testScope, event))

		test.EqOp(t, "", event.ID)
		test.EqOp(t, "", event.OrderingKey)
	})

	// A failed enqueue must not be followed by a dispatch. The transaction is
	// aborted either way, but a dispatch attempted against an aborted
	// transaction reports the driver's error rather than the one that caused it.
	T.Run("does not dispatch when the enqueue fails", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("outbox unavailable")
		enqueuer := &fakeEnqueuer{
			enqueue: func(context.Context, database.Tx, ...outbox.Message) error { return expected },
		}
		recorder := &dispatchRecorder{}

		err := newTestEmitter(t, enqueuer, recorder).Emit(t.Context(), testTx(), testScope, &Event{
			EventType: orderCreated,
			Payload:   map[string]string{"id": "order-1"},
		})

		test.ErrorIs(t, err, expected)
		test.EqOp(t, 0, recorder.resolved)
	})

	T.Run("reports a failed dispatch", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("endpoints unreadable")
		enqueuer := &fakeEnqueuer{}
		recorder := &dispatchRecorder{fail: expected}

		err := newTestEmitter(t, enqueuer, recorder).Emit(t.Context(), testTx(), testScope, &Event{
			EventType: orderCreated,
			Payload:   map[string]string{"id": "order-1"},
		})

		test.ErrorIs(t, err, expected)
		test.SliceLen(t, 1, enqueuer.got)
	})

	// Rendered before either write, so a payload nothing can marshal fails with
	// neither half done rather than between them.
	T.Run("refuses a payload it cannot marshal", func(t *testing.T) {
		t.Parallel()

		enqueuer := &fakeEnqueuer{}
		recorder := &dispatchRecorder{}

		err := newTestEmitter(t, enqueuer, recorder).Emit(t.Context(), testTx(), testScope, &Event{
			EventType: orderCreated,
			Payload:   make(chan int),
		})

		test.Error(t, err)
		test.EqOp(t, 0, enqueuer.calls)
		test.EqOp(t, 0, recorder.resolved)
	})

	T.Run("refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		err := newTestEmitter(t, &fakeEnqueuer{}, &dispatchRecorder{}).
			Emit(t.Context(), nil, testScope, &Event{EventType: orderCreated, Payload: "body"})

		test.ErrorIs(t, err, ErrNilExecutor)
	})

	T.Run("refuses a nil event", func(t *testing.T) {
		t.Parallel()

		err := newTestEmitter(t, &fakeEnqueuer{}, &dispatchRecorder{}).
			Emit(t.Context(), testTx(), testScope, nil)

		test.ErrorIs(t, err, ErrNilEvent)
	})

	T.Run("refuses an unset scope", func(t *testing.T) {
		t.Parallel()

		enqueuer := &fakeEnqueuer{}

		err := newTestEmitter(t, enqueuer, &dispatchRecorder{}).
			Emit(t.Context(), testTx(), tenancy.Scope{}, &Event{EventType: orderCreated, Payload: "body"})

		test.ErrorIs(t, err, ErrNoScope)
		test.EqOp(t, 0, enqueuer.calls)
	})

	T.Run("refuses an empty event type", func(t *testing.T) {
		t.Parallel()

		enqueuer := &fakeEnqueuer{}

		err := newTestEmitter(t, enqueuer, &dispatchRecorder{}).
			Emit(t.Context(), testTx(), testScope, &Event{Payload: "body"})

		test.ErrorIs(t, err, ErrEmptyEventType)
		test.EqOp(t, 0, enqueuer.calls)
	})

	T.Run("refuses a nil payload", func(t *testing.T) {
		t.Parallel()

		enqueuer := &fakeEnqueuer{}

		err := newTestEmitter(t, enqueuer, &dispatchRecorder{}).
			Emit(t.Context(), testTx(), testScope, &Event{EventType: orderCreated})

		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.EqOp(t, 0, enqueuer.calls)
	})
}

// TestEmitter_OneTransaction is the property the whole type exists for, against
// a real database: the outbox message and the dispatch rows are the caller's
// transaction's, so unwinding it leaves neither.
//
// It runs on SQLite rather than behind the container gate because what is under
// test is the transaction boundary, which every dialect this module supports
// draws in the same place — and a property this load-bearing should be checked
// on every run rather than on the runs that have a Docker daemon.
func TestEmitter_OneTransaction(T *testing.T) {
	T.Parallel()

	T.Run("a rolled-back transaction leaves neither the message nor the dispatch", func(t *testing.T) {
		t.Parallel()

		client, prefix, emitter := newLiveEmitter(t)

		unwound := platformerrors.New("the caller changed its mind")

		err := client.WithTransaction(t.Context(), func(tx database.Tx) error {
			if emitErr := emitter.Emit(t.Context(), tx, testScope, &Event{
				EventType: orderCreated,
				Payload:   map[string]string{"id": "order-1"},
			}); emitErr != nil {
				return emitErr
			}

			return unwound
		})

		test.ErrorIs(t, err, unwound)

		test.EqOp(t, int64(0), countRows(t, client, prefix+"_outbox_messages"))
		test.EqOp(t, int64(0), countRows(t, client, prefix+"_webhooks_deliveries"))
		test.EqOp(t, int64(0), countRows(t, client, prefix+"_webhooks_dispatches"))
	})

	// The control. Without it the case above would pass against an emitter that
	// never wrote anything at all.
	T.Run("a committed transaction leaves both", func(t *testing.T) {
		t.Parallel()

		client, prefix, emitter := newLiveEmitter(t)

		must.NoError(t, client.WithTransaction(t.Context(), func(tx database.Tx) error {
			return emitter.Emit(t.Context(), tx, testScope, &Event{
				EventType: orderCreated,
				Payload:   map[string]string{"id": "order-1"},
			})
		}))

		test.EqOp(t, int64(1), countRows(t, client, prefix+"_outbox_messages"))
		test.EqOp(t, int64(1), countRows(t, client, prefix+"_webhooks_deliveries"))
		test.EqOp(t, int64(1), countRows(t, client, prefix+"_webhooks_dispatches"))
	})

	// The gate against a live database, where "not dispatched" is the absence of
	// rows rather than the absence of a call.
	T.Run("an event outside the catalog commits to the outbox alone", func(t *testing.T) {
		t.Parallel()

		client, prefix, emitter := newLiveEmitter(t)

		must.NoError(t, client.WithTransaction(t.Context(), func(tx database.Tx) error {
			return emitter.Emit(t.Context(), tx, testScope, &Event{
				EventType: orderDeleted,
				Payload:   map[string]string{"id": "order-1"},
			})
		}))

		test.EqOp(t, int64(1), countRows(t, client, prefix+"_outbox_messages"))
		test.EqOp(t, int64(0), countRows(t, client, prefix+"_webhooks_dispatches"))
	})
}

// newLiveEmitter stands up one SQLite database carrying both schemas under one
// prefix, registers a subscriber to orderCreated, and returns an Emitter over
// the real writer and the real dispatcher.
//
// Both schemas share the prefix deliberately: the two tables are the two halves
// of one commit, and a test that put them in separate databases would be
// checking something SQLite cannot give and no deployment would run.
func newLiveEmitter(t *testing.T) (client database.Client, prefix string, emitter *Emitter) {
	t.Helper()

	client, prefix = newSQLiteEnv(t).database(t)

	stmts, err := outboxmigrations.Statements(dialect.SQLite, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := NewSQLStore(client, WithTablePrefix(prefix))
	must.NoError(t, err)

	dispatcher, err := NewDispatcher(store, client.Reader(), WithCatalog(testCatalog))
	must.NoError(t, err)

	writer, err := outbox.NewWriter(dialect.SQLite, outbox.WithWriterTablePrefix(prefix))
	must.NoError(t, err)

	emitter, err = NewEmitter(writer, dispatcher, testTopic)
	must.NoError(t, err)

	// A subscriber, committed ahead of the cases, so the fan-out has somewhere to
	// go and an empty dispatches table means the rollback rather than an event
	// nobody wanted.
	must.NoError(t, client.WithTransaction(t.Context(), func(tx database.Tx) error {
		_, registerErr := dispatcher.Register(t.Context(), tx, testScope, &Endpoint{
			URL:           "https://93.184.216.34/hooks",
			Secret:        Secret{Current: []byte("current")},
			Subscriptions: SubscribeTo(orderCreated),
		})

		return registerErr
	}))

	return client, prefix, emitter
}

// countRows reads one table's size. The prefix is the test's own and the table
// names are this module's constants, so nothing a caller supplies reaches the
// statement.
func countRows(t *testing.T, client database.Client, table string) int64 {
	t.Helper()

	var count int64

	must.NoError(t, client.Reader().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count))

	return count
}

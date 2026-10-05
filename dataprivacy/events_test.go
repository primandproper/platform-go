package dataprivacy

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v15/outbox"
	"github.com/primandproper/platform-go/v15/webhooks"
	webhooksmock "github.com/primandproper/platform-go/v15/webhooks/mock"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// dispatched is one delivery the emitter handed the dispatcher, and the scope
// it was handed under.
type dispatched struct {
	delivery *webhooks.Delivery
	scope    tenancy.Scope
}

// eventLog captures what a Fulfiller emits.
type eventLog struct {
	refuse     error
	deliveries []dispatched
	mu         sync.Mutex
}

func (l *eventLog) all() []dispatched {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]dispatched(nil), l.deliveries...)
}

// newEventEmitter builds an Emitter over mocks that write into l, with the
// dispatcher's catalog knowing every event this package emits.
func newEventEmitter(t *testing.T, l *eventLog) *webhooks.Emitter {
	t.Helper()

	enqueuer := &webhooksmock.EnqueuerMock{
		EnqueueFunc: func(context.Context, database.Tx, ...outbox.Message) error { return nil },
	}

	dispatcher := &webhooksmock.DispatcherMock{
		CatalogFunc: EventCatalog,
		DispatchFunc: func(_ context.Context, _ database.Tx, scope tenancy.Scope, delivery *webhooks.Delivery) error {
			l.mu.Lock()
			defer l.mu.Unlock()

			if l.refuse != nil {
				return l.refuse
			}

			l.deliveries = append(l.deliveries, dispatched{delivery: delivery, scope: scope})

			return nil
		},
	}

	emitter, err := webhooks.NewEmitter(enqueuer, dispatcher, "events")
	must.NoError(t, err)

	return emitter
}

func decodeErasureEvent(t *testing.T, delivery *webhooks.Delivery) *ErasureEvent {
	t.Helper()

	var event ErasureEvent
	must.NoError(t, json.Unmarshal(delivery.Payload, &event))

	return &event
}

func TestEventCatalog(T *testing.T) {
	T.Parallel()

	T.Run("knows every event this package emits", func(t *testing.T) {
		t.Parallel()

		catalog := EventCatalog()
		test.True(t, catalog.Known(EventErasureFulfilled))
		test.NotEqOp(t, "", catalog[EventErasureFulfilled].Description)
		test.MapLen(t, 1, catalog)
	})

	T.Run("hands out a fresh copy each time", func(t *testing.T) {
		t.Parallel()

		delete(EventCatalog(), EventErasureFulfilled)
		test.True(t, EventCatalog().Known(EventErasureFulfilled))
	})
}

func TestFulfiller_ErasureEvent(T *testing.T) {
	T.Parallel()

	T.Run("an erasure emits one event carrying every section's counts", func(t *testing.T) {
		t.Parallel()

		l := &eventLog{}

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(9, 2, nil, nil)))
			must.NoError(t, r.RegisterEraser("passwordreset", countingEraser(0, 0, nil, nil)))
			must.NoError(t, r.RegisterEraser("waitlists", countingEraser(3, 0, nil, nil)))
		}, WithFulfillerEventEmitter(newEventEmitter(t, l)))

		req := env.submitAndRun(t, RequestErasure)
		must.EqOp(t, StatusCompleted, req.Status)

		deliveries := l.all()
		must.SliceLen(t, 1, deliveries)
		test.EqOp(t, EventErasureFulfilled, deliveries[0].delivery.EventType)
		test.EqOp(t, testScope, deliveries[0].scope)

		event := decodeErasureEvent(t, deliveries[0].delivery)
		test.EqOp(t, req.ID, event.RequestID)
		test.Eq(t, testSubject, event.Subject)
		test.EqOp(t, int64(12), event.Deleted)
		test.EqOp(t, int64(2), event.Anonymized)
		test.False(t, event.KeyShredded)
		test.Eq(t, map[string]ErasureSection{
			"identity":      {Deleted: 9, Anonymized: 2},
			"passwordreset": {},
			"waitlists":     {Deleted: 3},
		}, event.Sections)
	})

	T.Run("an unconfined erasure is announced under the global scope", func(t *testing.T) {
		t.Parallel()

		l := &eventLog{}

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(1, 0, nil, nil)))
		}, WithFulfillerEventEmitter(newEventEmitter(t, l)))

		req := saveRequest(t, env.client, env.store,
			newRequestInScope(identifiers.New(), RequestErasure, tenancy.Scope{}, testSubject, env.clock.read()))

		_, err := env.run(t, req.ID, RequestErasure, newFinalReporter())
		must.NoError(t, err)
		must.EqOp(t, StatusCompleted, env.reread(t, req.ID).Status)

		deliveries := l.all()
		must.SliceLen(t, 1, deliveries)
		test.EqOp(t, tenancy.Global(), deliveries[0].scope)
	})

	T.Run("a refused event rolls the erasure back", func(t *testing.T) {
		t.Parallel()

		l := &eventLog{refuse: platformerrors.New("the outbox said no")}

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(9, 0, nil, nil)))
		}, WithFulfillerEventEmitter(newEventEmitter(t, l)))

		req := env.submitAndRun(t, RequestErasure)

		// The event and the erasure share one transaction, so an erasure that
		// cannot be announced does not commit either.
		test.EqOp(t, StatusFailed, req.Status)
		test.EqOp(t, int64(0), req.Deleted)
		test.StrContains(t, req.LastError, "the outbox said no")
	})

	T.Run("a failed erasure emits nothing", func(t *testing.T) {
		t.Parallel()

		l := &eventLog{}

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", EraserFunc(
				func(context.Context, database.Tx, tenancy.Scope, Subject) (ErasureOutcome, error) {
					return ErasureOutcome{}, platformerrors.New("eraser down")
				},
			)))
		}, WithFulfillerEventEmitter(newEventEmitter(t, l)))

		req := env.submitAndRun(t, RequestErasure)
		test.EqOp(t, StatusFailed, req.Status)
		test.SliceEmpty(t, l.all())
	})

	T.Run("an export emits nothing", func(t *testing.T) {
		t.Parallel()

		l := &eventLog{}

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{}`)))
		}, WithFulfillerEventEmitter(newEventEmitter(t, l)))

		req := env.submitAndRun(t, RequestExport)
		test.EqOp(t, StatusCompleted, req.Status)
		test.SliceEmpty(t, l.all())
	})

	T.Run("without an emitter the erasure completes and announces nothing", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(1, 0, nil, nil)))
		})

		req := env.submitAndRun(t, RequestErasure)
		test.EqOp(t, StatusCompleted, req.Status)
		test.EqOp(t, int64(1), req.Deleted)
	})
}

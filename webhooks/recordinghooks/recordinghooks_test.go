package recordinghooks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/audit"
	auditmock "github.com/primandproper/platform-go/v15/audit/mock"
	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/outbox"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"
	webhooksmock "github.com/primandproper/platform-go/v15/webhooks/mock"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var testScope = tenancy.Of("acct_1")

// testPrincipal is the caller every recorded write here is attributed to.
type testPrincipal struct{}

func (testPrincipal) UserID() string          { return "operator-1" }
func (testPrincipal) Scope() tenancy.Scope    { return testScope }
func (testPrincipal) ActiveAccountID() string { return "" }

func operatorPrincipal(context.Context) (callers.Principal, bool) { return testPrincipal{}, true }

// ledger is everything the two halves were handed, in order.
type ledger struct {
	entries    []*audit.Entry
	deliveries []*webhooks.Delivery
}

// newHooks builds RecordingHooks over mocks that write into the ledger, with
// the dispatcher's catalog knowing every event the webhooks writes emit.
func newHooks(t *testing.T, l *ledger) *RecordingHooks {
	t.Helper()

	entries := &auditmock.RecorderMock{
		RecordFunc: func(_ context.Context, _ database.Tx, _ tenancy.Scope, entries ...*audit.Entry) error {
			l.entries = append(l.entries, entries...)

			return nil
		},
	}

	enqueuer := &webhooksmock.EnqueuerMock{
		EnqueueFunc: func(context.Context, database.Tx, ...outbox.Message) error { return nil },
	}

	dispatcher := &webhooksmock.DispatcherMock{
		CatalogFunc: webhooks.EventCatalog,
		DispatchFunc: func(_ context.Context, _ database.Tx, _ tenancy.Scope, delivery *webhooks.Delivery) error {
			l.deliveries = append(l.deliveries, delivery)

			return nil
		},
	}

	emitter, err := webhooks.NewEmitter(enqueuer, dispatcher, "events")
	must.NoError(t, err)

	recorder, err := recording.New(entries, emitter, operatorPrincipal)
	must.NoError(t, err)

	hooks, err := NewRecordingHooks(recorder)
	must.NoError(t, err)

	return hooks
}

// last is the most recent entry and delivery, which every write here produces
// exactly one of.
func (l *ledger) last(t *testing.T) (*audit.Entry, *webhooks.Delivery) {
	t.Helper()

	must.SliceLen(t, 1, l.entries)
	must.SliceLen(t, 1, l.deliveries)

	return l.entries[0], l.deliveries[0]
}

func decode[T any](t *testing.T, delivery *webhooks.Delivery) *T {
	t.Helper()

	var event T
	must.NoError(t, json.Unmarshal(delivery.Payload, &event))

	return &event
}

// rawKeys is the set of top-level keys a payload carries, for asserting what it
// does not.
func rawKeys(t *testing.T, delivery *webhooks.Delivery) map[string]json.RawMessage {
	t.Helper()

	var keys map[string]json.RawMessage
	must.NoError(t, json.Unmarshal(delivery.Payload, &keys))

	return keys
}

// testTx is a database.Tx for the mocked cases, which execute nothing on it.
func testTx(*testing.T) database.Tx { return database.NewTxForTesting(nil) }

func endpointFixture() *webhooks.Endpoint {
	created := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

	return &webhooks.Endpoint{
		ID:          "endpoint-1",
		Scope:       testScope,
		URL:         "https://93.184.216.34/hooks",
		ContentType: webhooks.DefaultContentType,
		Name:        "orders",
		Secret:      webhooks.Secret{Current: []byte("signing-key-current"), Previous: []byte("signing-key-previous")},
		Headers:     map[string]string{"Authorization": "Bearer routing-token"},
		CreatedAt:   created,
		Subscriptions: []webhooks.Subscription{
			{ID: "sub-1", EndpointID: "endpoint-1", EventType: webhooks.EventEndpointCreated, CreatedAt: created},
		},
	}
}

func TestNewRecordingHooks(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil recorder by name", func(t *testing.T) {
		t.Parallel()

		hooks, err := NewRecordingHooks(nil)
		test.Nil(t, hooks)
		test.ErrorIs(t, err, webhooks.ErrNilRecorder)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})
}

func TestEventCatalog(T *testing.T) {
	T.Parallel()

	T.Run("knows every event the hooks emit", func(t *testing.T) {
		t.Parallel()

		catalog := webhooks.EventCatalog()
		for _, eventType := range []webhooks.EventType{
			webhooks.EventEndpointCreated, webhooks.EventEndpointUpdated, webhooks.EventEndpointArchived,
			webhooks.EventEndpointSecretRotated, webhooks.EventSubscriptionCreated, webhooks.EventSubscriptionArchived,
		} {
			test.True(t, catalog.Known(eventType), test.Sprintf("%s is not in the catalog", eventType))
			test.NotEqOp(t, "", catalog[eventType].Description)
		}

		test.MapLen(t, 6, catalog)
	})

	T.Run("hands each caller its own copy", func(t *testing.T) {
		t.Parallel()

		mutated := webhooks.EventCatalog()
		delete(mutated, webhooks.EventEndpointCreated)

		test.True(t, webhooks.EventCatalog().Known(webhooks.EventEndpointCreated))
	})
}

func TestRecordingHooks_AfterSaveEndpoint(T *testing.T) {
	T.Parallel()

	T.Run("a save with no before row is a creation", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		endpoint := endpointFixture()

		must.NoError(t, newHooks(t, l).AfterSaveEndpoint(t.Context(), testTx(t), testScope, nil, endpoint))

		entry, delivery := l.last(t)
		test.EqOp(t, webhooks.ResourceTypeEndpoint, entry.ResourceType)
		test.EqOp(t, "endpoint-1", entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.Nil(t, entry.Changes)
		test.EqOp(t, "operator-1", entry.Actor.ID)
		test.EqOp(t, testScope, entry.Scope)

		test.EqOp(t, webhooks.EventEndpointCreated, delivery.EventType)
		test.EqOp(t, "endpoint-1", delivery.OrderingKey)

		payload := decode[webhooks.EndpointEvent](t, delivery)
		test.EqOp(t, "endpoint-1", payload.EndpointID)
		test.EqOp(t, endpoint.URL, payload.URL)
		test.SliceEmpty(t, payload.Changed)
	})

	T.Run("a save over a row is an update carrying the diff", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		before := endpointFixture()
		after := endpointFixture()
		after.URL = "https://93.184.216.35/hooks"
		after.Headers = map[string]string{"Authorization": "Bearer rotated-token"}
		stamped := after.CreatedAt.Add(time.Minute)
		after.LastUpdatedAt = &stamped

		must.NoError(t, newHooks(t, l).AfterSaveEndpoint(t.Context(), testTx(t), testScope, before, after))

		entry, delivery := l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		must.MapContainsKey(t, entry.Changes, "url")
		test.EqOp(t, any(before.URL), entry.Changes["url"].Old)
		test.EqOp(t, any(after.URL), entry.Changes["url"].New)
		test.MapContainsKey(t, entry.Changes, "lastUpdatedAt")

		test.EqOp(t, webhooks.EventEndpointUpdated, delivery.EventType)
		test.Eq(t, []string{"url"}, decode[webhooks.EndpointEvent](t, delivery).Changed)
	})

	T.Run("names no header and no secret, in the entry or the event", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		before := endpointFixture()
		after := endpointFixture()
		after.Headers = map[string]string{"Authorization": "Bearer rotated-token"}
		after.Secret = webhooks.Secret{Current: []byte("signing-key-next"), Previous: before.Secret.Current}

		must.NoError(t, newHooks(t, l).AfterSaveEndpoint(t.Context(), testTx(t), testScope, before, after))

		entry, delivery := l.last(t)
		test.MapNotContainsKey(t, entry.Changes, "headers")
		test.MapNotContainsKey(t, entry.Changes, "Headers")
		test.MapNotContainsKey(t, entry.Changes, "secret")
		test.MapNotContainsKey(t, entry.Changes, "Secret")

		rendered, err := json.Marshal(entry)
		must.NoError(t, err)

		for _, leaked := range []string{"routing-token", "rotated-token", "signing-key"} {
			test.StrNotContains(t, string(rendered), leaked)
			test.StrNotContains(t, string(delivery.Payload), leaked)
		}

		keys := rawKeys(t, delivery)
		test.MapNotContainsKey(t, keys, "headers")
		test.MapNotContainsKey(t, keys, "secret")
		test.SliceEmpty(t, decode[webhooks.EndpointEvent](t, delivery).Changed)
	})

	T.Run("names subscriptions only when the event types moved", func(t *testing.T) {
		t.Parallel()

		restamped := endpointFixture()
		stamp := restamped.CreatedAt.Add(time.Hour)
		restamped.Subscriptions[0].LastUpdatedAt = &stamp

		l := &ledger{}
		must.NoError(t, newHooks(t, l).AfterSaveEndpoint(t.Context(), testTx(t), testScope, endpointFixture(), restamped))

		entry, delivery := l.last(t)
		test.MapContainsKey(t, entry.Changes, "subscriptions")
		test.SliceEmpty(t, decode[webhooks.EndpointEvent](t, delivery).Changed)

		resubscribed := endpointFixture()
		resubscribed.Subscriptions = append(resubscribed.Subscriptions, webhooks.Subscription{
			ID: "sub-2", EndpointID: "endpoint-1", EventType: webhooks.EventEndpointArchived,
		})

		l = &ledger{}
		must.NoError(t, newHooks(t, l).AfterSaveEndpoint(t.Context(), testTx(t), testScope, endpointFixture(), resubscribed))

		_, delivery = l.last(t)
		test.Eq(t, []string{"subscriptions"}, decode[webhooks.EndpointEvent](t, delivery).Changed)
	})

	T.Run("refuses a nil after row", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		err := newHooks(t, l).AfterSaveEndpoint(t.Context(), testTx(t), testScope, nil, nil)
		test.ErrorIs(t, err, webhooks.ErrNilEndpoint)
		test.SliceEmpty(t, l.entries)
		test.SliceEmpty(t, l.deliveries)
	})
}

func TestRecordingHooks_AfterArchiveEndpoint(T *testing.T) {
	T.Parallel()

	T.Run("records an archive", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		endpoint := endpointFixture()
		archived := endpoint.CreatedAt.Add(time.Hour)
		endpoint.ArchivedAt = &archived
		endpoint.Subscriptions = nil

		must.NoError(t, newHooks(t, l).AfterArchiveEndpoint(t.Context(), testTx(t), testScope, endpoint))

		entry, delivery := l.last(t)
		test.EqOp(t, webhooks.ResourceTypeEndpoint, entry.ResourceType)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, webhooks.EventEndpointArchived, delivery.EventType)

		payload := decode[webhooks.EndpointEvent](t, delivery)
		test.EqOp(t, "endpoint-1", payload.EndpointID)
		test.EqOp(t, endpoint.URL, payload.URL)
		test.StrNotContains(t, string(delivery.Payload), "signing-key")
	})

	T.Run("refuses a nil endpoint", func(t *testing.T) {
		t.Parallel()

		err := newHooks(t, &ledger{}).AfterArchiveEndpoint(t.Context(), testTx(t), testScope, nil)
		test.ErrorIs(t, err, webhooks.ErrNilEndpoint)
	})
}

func TestRecordingHooks_AfterRotateSecret(T *testing.T) {
	T.Parallel()

	T.Run("records that the key changed, and no key", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		must.NoError(t, newHooks(t, l).AfterRotateSecret(t.Context(), testTx(t), testScope, "endpoint-1"))

		entry, delivery := l.last(t)
		test.EqOp(t, webhooks.ResourceTypeEndpoint, entry.ResourceType)
		test.EqOp(t, "endpoint-1", entry.ResourceID)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.EqOp(t, "operator-1", entry.Actor.ID)
		test.Nil(t, entry.Changes)
		test.Eq(t, map[string]string{"endpointID": "endpoint-1", "rotated": "secret"}, entry.Metadata)

		test.EqOp(t, webhooks.EventEndpointSecretRotated, delivery.EventType)
		test.EqOp(t, "endpoint-1", delivery.OrderingKey)

		keys := rawKeys(t, delivery)
		test.MapLen(t, 1, keys)
		test.MapContainsKey(t, keys, "endpointID")
	})
}

func TestRecordingHooks_AfterAddSubscription(T *testing.T) {
	T.Parallel()

	created := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	archived := created.Add(time.Hour)
	live := &webhooks.Subscription{ID: "sub-1", EndpointID: "endpoint-1", EventType: webhooks.EventEndpointArchived, CreatedAt: created}
	retired := &webhooks.Subscription{ID: "sub-1", EndpointID: "endpoint-1", EventType: webhooks.EventEndpointArchived, CreatedAt: created, ArchivedAt: &archived}

	for name, before := range map[string]*webhooks.Subscription{
		"a new row":            nil,
		"a revived row":        retired,
		"a row already living": live,
	} {
		T.Run("records "+name+" as a creation", func(t *testing.T) {
			t.Parallel()

			l := &ledger{}
			must.NoError(t, newHooks(t, l).AfterAddSubscription(t.Context(), testTx(t), testScope, before, live))

			entry, delivery := l.last(t)
			test.EqOp(t, webhooks.ResourceTypeSubscription, entry.ResourceType)
			test.EqOp(t, "sub-1", entry.ResourceID)
			test.EqOp(t, audit.EventCreated, entry.EventType)
			test.Eq(t, map[string]string{
				"endpointID": "endpoint-1",
				"eventType":  webhooks.EventEndpointArchived.String(),
			}, entry.Metadata)

			test.EqOp(t, webhooks.EventSubscriptionCreated, delivery.EventType)
			test.EqOp(t, "endpoint-1", delivery.OrderingKey)
			test.Eq(t, &webhooks.SubscriptionEvent{
				SubscriptionID: "sub-1",
				EndpointID:     "endpoint-1",
				EventType:      webhooks.EventEndpointArchived,
			}, decode[webhooks.SubscriptionEvent](t, delivery))
		})
	}

	T.Run("refuses a nil after row", func(t *testing.T) {
		t.Parallel()

		err := newHooks(t, &ledger{}).AfterAddSubscription(t.Context(), testTx(t), testScope, nil, nil)
		test.ErrorIs(t, err, webhooks.ErrNilSubscription)
	})
}

func TestRecordingHooks_AfterArchiveSubscription(T *testing.T) {
	T.Parallel()

	T.Run("records an unsubscription", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		archived := time.Date(2026, 10, 4, 13, 0, 0, 0, time.UTC)
		subscription := &webhooks.Subscription{
			ID: "sub-1", EndpointID: "endpoint-1", EventType: webhooks.EventEndpointCreated, ArchivedAt: &archived,
		}

		must.NoError(t, newHooks(t, l).AfterArchiveSubscription(t.Context(), testTx(t), testScope, subscription))

		entry, delivery := l.last(t)
		test.EqOp(t, webhooks.ResourceTypeSubscription, entry.ResourceType)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, webhooks.EventSubscriptionArchived, delivery.EventType)
		test.EqOp(t, "sub-1", decode[webhooks.SubscriptionEvent](t, delivery).SubscriptionID)
	})

	T.Run("refuses a nil subscription", func(t *testing.T) {
		t.Parallel()

		err := newHooks(t, &ledger{}).AfterArchiveSubscription(t.Context(), testTx(t), testScope, nil)
		test.ErrorIs(t, err, webhooks.ErrNilSubscription)
	})
}

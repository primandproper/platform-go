package oauth2clients

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	auditmock "github.com/primandproper/platform-go/v14/audit/mock"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/outbox"
	"github.com/primandproper/platform-go/v14/recording"
	"github.com/primandproper/platform-go/v14/webhooks"
	webhooksmock "github.com/primandproper/platform-go/v14/webhooks/mock"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testPrincipal is the caller every recorded write here is attributed to.
type testPrincipal struct{}

func (testPrincipal) UserID() string          { return "operator-1" }
func (testPrincipal) Scope() tenancy.Scope    { return testScope }
func (testPrincipal) ActiveAccountID() string { return "" }

func operatorPrincipal(context.Context) (callers.Principal, bool) { return testPrincipal{}, true }

// ledger is everything the two halves were handed, in order.
type ledger struct {
	refuse     error
	entries    []*audit.Entry
	deliveries []*webhooks.Delivery
}

// newRecordingHooks builds RecordingHooks over mocks that write into the
// ledger, with the dispatcher's catalog knowing every event this package emits.
func newRecordingHooks(t *testing.T, l *ledger) *RecordingHooks {
	t.Helper()

	entries := &auditmock.RecorderMock{
		RecordFunc: func(_ context.Context, _ database.Tx, _ tenancy.Scope, entries ...*audit.Entry) error {
			if l.refuse != nil {
				return l.refuse
			}

			l.entries = append(l.entries, entries...)

			return nil
		},
	}

	enqueuer := &webhooksmock.EnqueuerMock{
		EnqueueFunc: func(context.Context, database.Tx, ...outbox.Message) error { return nil },
	}

	dispatcher := &webhooksmock.DispatcherMock{
		CatalogFunc: EventCatalog,
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

	must.SliceNotEmpty(t, l.entries)
	must.SliceNotEmpty(t, l.deliveries)

	return l.entries[len(l.entries)-1], l.deliveries[len(l.deliveries)-1]
}

func decodeClient(t *testing.T, delivery *webhooks.Delivery) *ClientEvent {
	t.Helper()

	var event ClientEvent
	must.NoError(t, json.Unmarshal(delivery.Payload, &event))

	return &event
}

func TestNewRecordingHooks(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil recorder by name", func(t *testing.T) {
		t.Parallel()

		hooks, err := NewRecordingHooks(nil)
		test.Nil(t, hooks)
		test.ErrorIs(t, err, ErrNilRecorder)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})
}

func TestEventCatalog(T *testing.T) {
	T.Parallel()

	T.Run("knows every event this package emits", func(t *testing.T) {
		t.Parallel()

		catalog := EventCatalog()
		for _, eventType := range []webhooks.EventType{EventClientCreated, EventClientUpdated, EventClientArchived} {
			test.True(t, catalog.Known(eventType), test.Sprintf("%s is not in the catalog", eventType))
			test.NotEqOp(t, "", catalog[eventType].Description)
		}

		test.MapLen(t, 3, catalog)
	})

	T.Run("hands out a fresh copy each time", func(t *testing.T) {
		t.Parallel()

		delete(EventCatalog(), EventClientCreated)
		test.True(t, EventCatalog().Known(EventClientCreated))
	})
}

func TestRecordingHooks(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("every operation records one entry and one event naming the registration", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		svc, _ := newService(t, env, WithHooks(newRecordingHooks(t, l)))

		issued, err := svc.CreateClient(t.Context(), testScope, testOwner, &CreationInput{
			Name:         "a client",
			RedirectURIs: []string{testRedirect},
		})
		must.NoError(t, err)
		client := issued.Client

		entry, delivery := l.last(t)
		test.EqOp(t, ResourceTypeClient, entry.ResourceType)
		test.EqOp(t, client.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.EqOp(t, testScope, entry.Scope)
		test.EqOp(t, "operator-1", entry.Actor.ID)
		test.Eq(t, map[string]string{metadataClientID: client.ClientID}, entry.Metadata)
		test.MapEmpty(t, entry.Changes)
		test.EqOp(t, EventClientCreated, delivery.EventType)
		test.EqOp(t, client.ID, delivery.OrderingKey)
		test.Eq(t, &ClientEvent{
			ID:       client.ID,
			ClientID: client.ClientID,
			OwnerID:  testOwner,
			Name:     "a client",
		}, decodeClient(t, delivery))

		// The plaintext is on the value the caller was handed and on nothing
		// the hooks wrote.
		test.StrNotContains(t, string(delivery.Payload), issued.Secret)

		_, err = svc.UpdateClient(t.Context(), testScope, client.ID, &UpdateInput{
			Name:         "renamed",
			RedirectURIs: []string{testRedirect},
		})
		must.NoError(t, err)

		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.MapContainsKey(t, entry.Changes, "name")
		test.EqOp(t, "a client", entry.Changes["name"].Old)
		test.EqOp(t, "renamed", entry.Changes["name"].New)
		test.MapContainsKey(t, entry.Changes, lastUpdatedAtField)
		test.EqOp(t, EventClientUpdated, delivery.EventType)
		// The diff names the timestamp the revision stamped; the changed list
		// does not.
		test.Eq(t, []string{"name"}, decodeClient(t, delivery).Changed)

		must.NoError(t, svc.ArchiveClient(t.Context(), testScope, client.ID))

		entry, delivery = l.last(t)
		test.EqOp(t, client.ID, entry.ResourceID)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, EventClientArchived, delivery.EventType)
		// The name the archive left on the row, which no ordinary read now
		// reaches.
		test.EqOp(t, "renamed", decodeClient(t, delivery).Name)

		test.SliceLen(t, 3, l.entries)
		test.SliceLen(t, 3, l.deliveries)
	})

	T.Run("the secret's digest is in no diff, even when the row's differs", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		hooks := newRecordingHooks(t, l)

		before := newClient(testScope, testOwner)
		before.ID, before.ClientID, before.SecretHash = "row_1", "client_1", "digest-before"

		after := before.Clone()
		after.SecretHash = "digest-after"
		after.Description = "rewritten"

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			return hooks.AfterUpdateClient(t.Context(), tx, testScope, before, after)
		}))

		entry, delivery := l.last(t)
		test.Eq(t, []string{"description"}, decodeClient(t, delivery).Changed)
		test.MapNotContainsKey(t, entry.Changes, "SecretHash")
		test.MapNotContainsKey(t, entry.Changes, "secretHash")

		for field, change := range entry.Changes {
			test.NotEq(t, any("digest-before"), change.Old, test.Sprintf("%s carries the old digest", field))
			test.NotEq(t, any("digest-after"), change.New, test.Sprintf("%s carries the new digest", field))
		}

		test.StrNotContains(t, string(delivery.Payload), "digest")
	})

	T.Run("an administered registration names no subject", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		svc, _ := newService(t, env, WithHooks(newRecordingHooks(t, l)))

		_, err := svc.CreateClient(t.Context(), tenancy.Global(), "", &CreationInput{
			Name:         "an operator client",
			RedirectURIs: []string{testRedirect},
		})
		must.NoError(t, err)

		_, delivery := l.last(t)
		test.EqOp(t, "", decodeClient(t, delivery).OwnerID)
		test.StrNotContains(t, string(delivery.Payload), "ownerID")
	})

	T.Run("a refused recording fails the operation, and the row with it", func(t *testing.T) {
		t.Parallel()

		l := &ledger{refuse: platformerrors.New("the log said no")}
		svc, _ := newService(t, env, WithHooks(newRecordingHooks(t, l)))

		issued, err := svc.CreateClient(t.Context(), testScope, testOwner, &CreationInput{
			Name:         "a client",
			RedirectURIs: []string{testRedirect},
		})
		test.ErrorIs(t, err, l.refuse)
		test.Nil(t, issued)
		test.SliceEmpty(t, l.deliveries)
	})

	T.Run("a nil row is refused by name", func(t *testing.T) {
		t.Parallel()

		hooks := newRecordingHooks(t, &ledger{})
		client := newClient(testScope, testOwner)

		test.ErrorIs(t, hooks.AfterCreateClient(t.Context(), nil, testScope, nil), ErrNilClient)
		test.ErrorIs(t, hooks.AfterUpdateClient(t.Context(), nil, testScope, nil, client), ErrNilClient)
		test.ErrorIs(t, hooks.AfterUpdateClient(t.Context(), nil, testScope, client, nil), ErrNilClient)
		test.ErrorIs(t, hooks.AfterArchiveClient(t.Context(), nil, testScope, nil), ErrNilClient)
	})
}

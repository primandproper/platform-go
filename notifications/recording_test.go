package notifications

import (
	"context"
	"encoding/json"
	"testing"

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

// operator is the caller every recorded write here is attributed to.
type operator struct{}

func (operator) UserID() string          { return "operator-1" }
func (operator) Scope() tenancy.Scope    { return testScope }
func (operator) ActiveAccountID() string { return "" }

func operatorPrincipal(context.Context) (callers.Principal, bool) { return operator{}, true }

// ledger is everything the two halves were handed, in order.
type ledger struct {
	refuse     error
	entries    []*audit.Entry
	deliveries []*webhooks.Delivery
}

// newRecordingHooks builds RecordingHooks over mocks that write into the
// ledger, with the dispatcher's catalog knowing every event this package emits.
func newRecordingHooks(t *testing.T, l *ledger, opts ...recording.Option) *RecordingHooks {
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

	recorder, err := recording.New(entries, emitter, operatorPrincipal, opts...)
	must.NoError(t, err)

	hooks, err := NewRecordingHooks(recorder)
	must.NoError(t, err)

	return hooks
}

// last is the most recent entry and delivery.
func (l *ledger) last(t *testing.T) (*audit.Entry, *webhooks.Delivery) {
	t.Helper()

	must.SliceNotEmpty(t, l.entries)
	must.SliceNotEmpty(t, l.deliveries)

	return l.entries[len(l.entries)-1], l.deliveries[len(l.deliveries)-1]
}

func decodeNotificationEvent(t *testing.T, delivery *webhooks.Delivery) *NotificationEvent {
	t.Helper()

	var event NotificationEvent
	must.NoError(t, json.Unmarshal(delivery.Payload, &event))

	return &event
}

func decodeDeviceEvent(t *testing.T, delivery *webhooks.Delivery) *DeviceEvent {
	t.Helper()

	var event DeviceEvent
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
		for _, eventType := range []webhooks.EventType{
			EventNotificationCreated, EventNotificationArchived,
			EventDeviceRegistered, EventDeviceReregistered, EventDeviceRevoked,
		} {
			test.True(t, catalog.Known(eventType), test.Sprintf("%s is not in the catalog", eventType))
			test.NotEqOp(t, "", catalog[eventType].Description)
		}

		test.MapLen(t, 5, catalog)
	})

	T.Run("hands out a fresh copy each time", func(t *testing.T) {
		t.Parallel()

		delete(EventCatalog(), EventNotificationCreated)
		test.True(t, EventCatalog().Known(EventNotificationCreated))
	})
}

func TestRecordingHooks(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("every inbox write but a read records one entry and one event naming the row", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		n := env.mustCreate(t, store, testScope, newNotification(testPrincipal, "order.shipped", "Your order shipped"))
		entry, delivery := l.last(t)
		test.EqOp(t, ResourceTypeNotification, entry.ResourceType)
		test.EqOp(t, n.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.EqOp(t, testScope, entry.Scope)
		test.EqOp(t, "operator-1", entry.Actor.ID)
		test.Eq(t, map[string]string{metadataTopic: "order.shipped"}, entry.Metadata)
		test.EqOp(t, EventNotificationCreated, delivery.EventType)
		test.EqOp(t, n.ID, delivery.OrderingKey)
		test.Eq(t, &NotificationEvent{NotificationID: n.ID, Principal: testPrincipal, Topic: "order.shipped"}, decodeNotificationEvent(t, delivery))
		test.StrNotContains(t, string(delivery.Payload), "Your order shipped")

		// Reading records nothing: ReadAt on the row is the record.
		_, err := env.markRead(t, store, testScope, testPrincipal, n.ID)
		must.NoError(t, err)
		env.mustCreate(t, store, testScope, newNotification(testPrincipal, "order.delivered", "Your order arrived"))
		_, err = env.markAllRead(t, store, testScope, testPrincipal)
		must.NoError(t, err)
		test.SliceLen(t, 2, l.entries)
		test.SliceLen(t, 2, l.deliveries)

		_, err = env.archive(t, store, testScope, testPrincipal, n.ID)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, ResourceTypeNotification, entry.ResourceType)
		test.EqOp(t, n.ID, entry.ResourceID)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, EventNotificationArchived, delivery.EventType)

		test.SliceLen(t, 3, l.entries)
		test.SliceLen(t, 3, l.deliveries)
	})

	T.Run("a registration is a create, a re-registration an update whose diff says the handset changed hands", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		first := env.mustRegister(t, store, testScope, newDevice(testPrincipal, PlatformIOS, "token-a"))
		entry, delivery := l.last(t)
		test.EqOp(t, ResourceTypeDevice, entry.ResourceType)
		test.EqOp(t, first.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.MapEmpty(t, entry.Changes)
		test.Eq(t, map[string]string{metadataPlatform: "ios"}, entry.Metadata)
		test.EqOp(t, EventDeviceRegistered, delivery.EventType)
		test.EqOp(t, first.ID, delivery.OrderingKey)
		test.Eq(t, &DeviceEvent{DeviceID: first.ID, Principal: testPrincipal, Platform: PlatformIOS}, decodeDeviceEvent(t, delivery))

		moved := env.mustRegister(t, store, testScope, newDevice(otherPrincipal, PlatformIOS, "token-a"))
		entry, delivery = l.last(t)
		test.EqOp(t, first.ID, entry.ResourceID)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		must.MapContainsKey(t, entry.Changes, "principal")
		test.EqOp(t, testPrincipal, entry.Changes["principal"].Old)
		test.EqOp(t, otherPrincipal, entry.Changes["principal"].New)
		test.EqOp(t, EventDeviceReregistered, delivery.EventType)
		reregistered := decodeDeviceEvent(t, delivery)
		test.EqOp(t, moved.ID, reregistered.DeviceID)
		test.EqOp(t, otherPrincipal, reregistered.Principal)
		test.EqOp(t, testPrincipal, reregistered.PreviousPrincipal)
		// The diff may name the stamp every re-registration moves; the changed
		// list does not.
		test.SliceNotContains(t, reregistered.Changed, lastSeenAtField)
		test.SliceContains(t, reregistered.Changed, "principal")

		_, err := env.revoke(t, store, testScope, otherPrincipal, moved.ID)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, moved.ID, entry.ResourceID)
		test.EqOp(t, audit.EventDeleted, entry.EventType)
		test.EqOp(t, EventDeviceRevoked, delivery.EventType)
		// Handed the row from before the delete, so it can say whose it was.
		test.EqOp(t, otherPrincipal, decodeDeviceEvent(t, delivery).Principal)

		test.SliceLen(t, 3, l.entries)
		test.SliceLen(t, 3, l.deliveries)
	})

	T.Run("a device token reaches neither the entry nor the event", func(t *testing.T) {
		t.Parallel()

		const token = "secret-device-token"

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		d := env.mustRegister(t, store, testScope, newDevice(testPrincipal, PlatformAndroid, token))
		env.mustRegister(t, store, testScope, newDevice(otherPrincipal, PlatformAndroid, token))
		_, err := env.revoke(t, store, testScope, otherPrincipal, d.ID)
		must.NoError(t, err)

		must.SliceLen(t, 3, l.entries)

		for _, entry := range l.entries {
			test.MapNotContainsKey(t, entry.Changes, "token")
			test.MapNotContainsValue(t, entry.Metadata, token)

			for _, change := range entry.Changes {
				test.NotEq(t, any(token), change.Old)
				test.NotEq(t, any(token), change.New)
			}
		}

		for _, delivery := range l.deliveries {
			test.StrNotContains(t, string(delivery.Payload), token)
		}
	})

	T.Run("an entry names its principal as subject, never in metadata", func(t *testing.T) {
		t.Parallel()

		bySubject := func(_ context.Context, scope tenancy.Scope, entry *recording.Entry) tenancy.Scope {
			if entry.SubjectID != "" {
				return tenancy.Of(entry.SubjectID)
			}

			return scope
		}

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l, recording.WithScopeResolver(bySubject))))

		env.mustCreate(t, store, testScope, newNotification(testPrincipal, "order.shipped", "Your order shipped"))
		env.mustRegister(t, store, testScope, newDevice(testPrincipal, PlatformIOS, "token-a"))

		must.SliceLen(t, 2, l.entries)

		for _, entry := range l.entries {
			test.EqOp(t, tenancy.Of(testPrincipal), entry.Scope)
			test.MapNotContainsValue(t, entry.Metadata, testPrincipal)
		}
	})

	T.Run("an erasure records nothing, because dataprivacy records the request once", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		env.mustCreate(t, store, testScope, newNotification(testPrincipal, "order.shipped", "Your order shipped"))
		env.mustRegister(t, store, testScope, newDevice(testPrincipal, PlatformIOS, "token-a"))

		erasures := []func() (int64, error){
			func() (int64, error) { return env.eraseNotifications(t, store, testScope, testPrincipal) },
			func() (int64, error) { return env.eraseDevices(t, store, testScope, testPrincipal) },
		}

		for _, erase := range erasures {
			for _, want := range []int64{1, 0} {
				deleted, err := erase()
				must.NoError(t, err)
				test.EqOp(t, want, deleted)
			}
		}

		// The notification and the device, and nothing for any of the four
		// erasures.
		test.SliceLen(t, 2, l.entries)
		test.SliceLen(t, 2, l.deliveries)
		_, delivery := l.last(t)
		test.EqOp(t, EventDeviceRegistered, delivery.EventType)
	})

	T.Run("a refused recording fails the write, and the row with it", func(t *testing.T) {
		t.Parallel()

		l := &ledger{refuse: platformerrors.New("the log said no")}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		filed, err := env.create(t, store, testScope, newNotification(testPrincipal, "order.shipped", "Your order shipped"))
		test.ErrorIs(t, err, l.refuse)
		test.Nil(t, filed)
		test.SliceEmpty(t, l.deliveries)

		registered, err := env.register(t, store, testScope, newDevice(testPrincipal, PlatformIOS, "token-a"))
		test.ErrorIs(t, err, l.refuse)
		test.Nil(t, registered)

		devices, err := store.ListDevices(t.Context(), env.reader(), testScope, testPrincipal, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, devices.Data)
	})

	T.Run("a nil row is refused by name", func(t *testing.T) {
		t.Parallel()

		hooks := newRecordingHooks(t, &ledger{})
		ctx := t.Context()

		test.ErrorIs(t, hooks.AfterCreateNotification(ctx, nil, testScope, nil), ErrNilNotification)
		test.ErrorIs(t, hooks.AfterArchiveNotification(ctx, nil, testScope, nil), ErrNilNotification)
		test.ErrorIs(t, hooks.AfterRegisterDevice(ctx, nil, testScope, nil, nil), ErrNilDevice)
		test.ErrorIs(t, hooks.AfterRevokeDevice(ctx, nil, testScope, nil), ErrNilDevice)
	})
}

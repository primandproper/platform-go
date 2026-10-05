package mediaregistry

import (
	"context"
	"encoding/json"
	"strconv"
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

// last is the most recent entry and delivery, which every write here produces
// exactly one of.
func (l *ledger) last(t *testing.T) (*audit.Entry, *webhooks.Delivery) {
	t.Helper()

	must.SliceNotEmpty(t, l.entries)
	must.SliceNotEmpty(t, l.deliveries)

	return l.entries[len(l.entries)-1], l.deliveries[len(l.deliveries)-1]
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
		for _, eventType := range []webhooks.EventType{EventObjectRecorded, EventObjectArchived, EventObjectsErased} {
			test.True(t, catalog.Known(eventType), test.Sprintf("%s is not in the catalog", eventType))
			test.NotEqOp(t, "", catalog[eventType].Description)
		}

		test.MapLen(t, 3, catalog)
	})

	T.Run("hands out a fresh copy each time", func(t *testing.T) {
		t.Parallel()

		delete(EventCatalog(), EventObjectRecorded)
		test.True(t, EventCatalog().Known(EventObjectRecorded))
	})
}

func TestRecordingHooks(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("a registration and an archive each record one entry and one event naming the object", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		in := newInput("avatars/ada.png", "user_1")
		in.BelongsTo = Subject{Type: "recipe", ID: "recipe_1"}
		recorded := env.mustRecord(t, store, testScope, in)

		entry, delivery := l.last(t)
		test.EqOp(t, ResourceTypeObject, entry.ResourceType)
		test.EqOp(t, recorded.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.EqOp(t, testScope, entry.Scope)
		test.EqOp(t, "operator-1", entry.Actor.ID)
		test.Eq(t, map[string]string{
			metadataContentType:   "image/png",
			metadataSize:          "1024",
			metadataBelongsToType: "recipe",
		}, entry.Metadata)
		test.EqOp(t, EventObjectRecorded, delivery.EventType)
		test.EqOp(t, recorded.ID, delivery.OrderingKey)

		var event ObjectEvent
		must.NoError(t, json.Unmarshal(delivery.Payload, &event))
		test.Eq(t, ObjectEvent{
			ObjectID:    recorded.ID,
			OwnerID:     "user_1",
			BelongsTo:   Subject{Type: "recipe", ID: "recipe_1"},
			Key:         "avatars/ada.png",
			ContentType: "image/png",
			Size:        1024,
		}, event)

		env.mustArchive(t, store, testScope, recorded.ID)

		entry, delivery = l.last(t)
		test.EqOp(t, recorded.ID, entry.ResourceID)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, EventObjectArchived, delivery.EventType)

		// The key the surviving bytes are at, from the row the archive left.
		must.NoError(t, json.Unmarshal(delivery.Payload, &event))
		test.EqOp(t, "avatars/ada.png", event.Key)
		test.EqOp(t, "user_1", event.OwnerID)

		test.SliceLen(t, 2, l.entries)
		test.SliceLen(t, 2, l.deliveries)
	})

	T.Run("no entry carries the owner or the key in its metadata", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		in := newInput("users/user_1/avatar.png", "user_1")
		in.BelongsTo = Subject{Type: "user", ID: "user_1"}
		recorded := env.mustRecord(t, store, testScope, in)
		env.mustArchive(t, store, testScope, recorded.ID)

		for _, entry := range l.entries {
			for key, value := range entry.Metadata {
				test.StrNotContains(t, value, "user_1", test.Sprintf("metadata %q names the owner", key))
			}
		}
	})

	T.Run("an erasure records its count and never the owner, zero included", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		env.mustRecord(t, store, testScope, newInput("a.png", "user_1"))
		env.mustRecord(t, store, testScope, newInput("b.png", "user_1"))

		for _, want := range []int64{2, 0} {
			archived, err := env.archiveForOwner(t, store, testScope, "user_1")
			must.NoError(t, err)
			test.EqOp(t, want, archived)

			entry, delivery := l.last(t)
			test.EqOp(t, ResourceTypeObject, entry.ResourceType)
			test.EqOp(t, "", entry.ResourceID)
			test.EqOp(t, audit.EventArchived, entry.EventType)
			test.EqOp(t, testScope, entry.Scope)
			test.Eq(t, map[string]string{metadataArchived: strconv.FormatInt(want, 10)}, entry.Metadata)

			test.EqOp(t, EventObjectsErased, delivery.EventType)
			var erased ErasureEvent
			must.NoError(t, json.Unmarshal(delivery.Payload, &erased))
			test.EqOp(t, want, erased.Archived)
			test.StrNotContains(t, string(delivery.Payload), "user_1")
		}

		// Two registrations, two erasures.
		test.SliceLen(t, 4, l.entries)
		test.SliceLen(t, 4, l.deliveries)
	})

	T.Run("a recorder filing by subject puts an object's entries on its owner's chain", func(t *testing.T) {
		t.Parallel()

		bySubject := func(_ context.Context, scope tenancy.Scope, entry *recording.Entry) tenancy.Scope {
			if entry.SubjectID != "" {
				return tenancy.Of(entry.SubjectID)
			}

			return scope
		}

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l, recording.WithScopeResolver(bySubject))))

		recorded := env.mustRecord(t, store, testScope, newInput("a.png", "user_1"))
		env.mustArchive(t, store, testScope, recorded.ID)
		_, err := env.archiveForOwner(t, store, testScope, "user_1")
		must.NoError(t, err)

		must.SliceLen(t, 3, l.entries)
		test.EqOp(t, tenancy.Of("user_1"), l.entries[0].Scope)
		test.EqOp(t, tenancy.Of("user_1"), l.entries[1].Scope)
		// The erasure's entry names nobody, so it stays with the write.
		test.EqOp(t, testScope, l.entries[2].Scope)
	})

	T.Run("a refused recording fails the write, and the row with it", func(t *testing.T) {
		t.Parallel()

		l := &ledger{refuse: platformerrors.New("the log said no")}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		recorded, err := env.record(t, store, testScope, newInput("refused.png", "user_1"))
		test.ErrorIs(t, err, l.refuse)
		test.Nil(t, recorded)
		test.SliceEmpty(t, l.deliveries)

		_, err = store.GetObjectByKey(t.Context(), env.reader(), testScope, "refused.png")
		test.ErrorIs(t, err, ErrObjectNotFound)
	})

	T.Run("a nil row is refused by name", func(t *testing.T) {
		t.Parallel()

		hooks := newRecordingHooks(t, &ledger{})

		test.ErrorIs(t, hooks.AfterRecordObject(t.Context(), nil, testScope, nil), ErrNilObject)
		test.ErrorIs(t, hooks.AfterArchiveObject(t.Context(), nil, testScope, nil), ErrNilObject)
	})
}

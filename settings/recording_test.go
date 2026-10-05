package settings

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

// recordingPrincipal is the caller every recorded write here is attributed to.
type recordingPrincipal struct{}

func (recordingPrincipal) UserID() string          { return "operator-1" }
func (recordingPrincipal) Scope() tenancy.Scope    { return testScope }
func (recordingPrincipal) ActiveAccountID() string { return "" }

func operatorPrincipal(context.Context) (callers.Principal, bool) { return recordingPrincipal{}, true }

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

	must.SliceLen(t, 1, l.entries)
	must.SliceLen(t, 1, l.deliveries)

	return l.entries[0], l.deliveries[0]
}

func decodeEvent[T any](t *testing.T, delivery *webhooks.Delivery) *T {
	t.Helper()

	var event T
	must.NoError(t, json.Unmarshal(delivery.Payload, &event))

	return &event
}

func recordingTx() database.Tx { return database.NewTxForTesting(nil) }

func recordedDefinition() *Definition {
	return &Definition{
		ID:          "def-1",
		Name:        "theme",
		Description: "how the app looks",
		Kind:        KindString,
		Scope:       testScope,
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func recordedValue(raw string) *Value {
	return &Value{
		ID:           "val-1",
		DefinitionID: "def-1",
		Subject:      Subject{Type: "user", ID: "user-7"},
		Raw:          raw,
		Scope:        testScope,
		CreatedAt:    time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
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
			EventDefinitionCreated, EventDefinitionUpdated, EventDefinitionArchived,
			EventValueSet, EventValueCleared, EventValuesErased,
		} {
			test.True(t, catalog.Known(eventType), test.Sprintf("%s is not in the catalog", eventType))
			test.NotEqOp(t, "", catalog[eventType].Description)
		}

		test.MapLen(t, 6, catalog)
	})

	T.Run("hands out a fresh copy each time", func(t *testing.T) {
		t.Parallel()

		delete(EventCatalog(), EventDefinitionCreated)
		test.True(t, EventCatalog().Known(EventDefinitionCreated))
	})
}

func TestRecordingHooks_Definitions(T *testing.T) {
	T.Parallel()

	T.Run("a definition's entry names the setting and nobody", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		definition := recordedDefinition()
		must.NoError(t, newRecordingHooks(t, l).AfterCreateDefinition(t.Context(), recordingTx(), testScope, definition))

		entry, delivery := l.last(t)
		test.EqOp(t, ResourceTypeDefinition, entry.ResourceType)
		test.EqOp(t, "def-1", entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.EqOp(t, testScope, entry.Scope)
		test.EqOp(t, "operator-1", entry.Actor.ID)
		test.Eq(t, map[string]string{metadataName: "theme"}, entry.Metadata)
		test.EqOp(t, EventDefinitionCreated, delivery.EventType)
		test.EqOp(t, "def-1", delivery.OrderingKey)
		test.Eq(t, &DefinitionEvent{DefinitionID: "def-1", Name: "theme"}, decodeEvent[DefinitionEvent](t, delivery))
	})

	T.Run("an update's entry carries the diff and its event the fields that moved, without the stamp", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		before, after := recordedDefinition(), recordedDefinition()
		stamped := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
		after.Description, after.AdminOnly, after.LastUpdatedAt = "rewritten", true, &stamped

		must.NoError(t, newRecordingHooks(t, l).AfterUpdateDefinition(t.Context(), recordingTx(), testScope, before, after))

		entry, delivery := l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.EqOp(t, "how the app looks", entry.Changes["description"].Old)
		test.EqOp(t, "rewritten", entry.Changes["description"].New)
		test.MapContainsKey(t, entry.Changes, lastUpdatedAtField)
		test.EqOp(t, EventDefinitionUpdated, delivery.EventType)
		test.Eq(t, []string{"adminOnly", "description"}, decodeEvent[DefinitionEvent](t, delivery).Changed)
	})

	T.Run("an archive names the setting it retired", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		must.NoError(t, newRecordingHooks(t, l).AfterArchiveDefinition(t.Context(), recordingTx(), testScope, recordedDefinition()))

		entry, delivery := l.last(t)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, "theme", entry.Metadata[metadataName])
		test.EqOp(t, EventDefinitionArchived, delivery.EventType)
		test.EqOp(t, "theme", decodeEvent[DefinitionEvent](t, delivery).Name)
	})

	T.Run("a nil row is refused by name", func(t *testing.T) {
		t.Parallel()

		hooks := newRecordingHooks(t, &ledger{})
		test.ErrorIs(t, hooks.AfterCreateDefinition(t.Context(), recordingTx(), testScope, nil), ErrNilDefinition)
		test.ErrorIs(t, hooks.AfterUpdateDefinition(t.Context(), recordingTx(), testScope, nil, recordedDefinition()), ErrNilDefinition)
		test.ErrorIs(t, hooks.AfterArchiveDefinition(t.Context(), recordingTx(), testScope, nil), ErrNilDefinition)
	})
}

func TestRecordingHooks_Values(T *testing.T) {
	T.Parallel()

	T.Run("a first answer diffs against nothing, and names its subject only through SubjectID", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		must.NoError(t, newRecordingHooks(t, l).AfterSetValue(t.Context(), recordingTx(), testScope, recordedDefinition(), nil, recordedValue("dark")))

		entry, delivery := l.last(t)
		test.EqOp(t, ResourceTypeValue, entry.ResourceType)
		test.EqOp(t, "val-1", entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.Eq(t, map[string]string{
			metadataDefinitionID: "def-1",
			metadataName:         "theme",
			metadataSubjectType:  "user",
		}, entry.Metadata)
		test.EqOp(t, "dark", entry.Changes[rawValueField].New)
		test.Nil(t, entry.Changes[rawValueField].Old)
		// The subject never reaches the entry's diff or its metadata, so nothing
		// here outlives the subject's erasure.
		test.MapNotContainsKey(t, entry.Changes, "subject")

		test.EqOp(t, EventValueSet, delivery.EventType)
		test.Eq(t, &ValueEvent{
			Subject:      Subject{Type: "user", ID: "user-7"},
			ValueID:      "val-1",
			DefinitionID: "def-1",
			Name:         "theme",
		}, decodeEvent[ValueEvent](t, delivery))
	})

	T.Run("the entry's subject reaches a resolver filing by subject", func(t *testing.T) {
		t.Parallel()

		var resolved string
		hooks := newRecordingHooks(t, &ledger{},
			recording.WithScopeResolver(func(_ context.Context, scope tenancy.Scope, entry *recording.Entry) tenancy.Scope {
				resolved = entry.SubjectID

				return scope
			}))

		must.NoError(t, hooks.AfterSetValue(t.Context(), recordingTx(), testScope, recordedDefinition(), nil, recordedValue("dark")))
		test.EqOp(t, "user-7", resolved)
	})

	T.Run("a revised answer diffs against the previous one", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		before, after := recordedValue("dark"), recordedValue("light")
		stamped := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
		after.LastUpdatedAt = &stamped

		must.NoError(t, newRecordingHooks(t, l).AfterSetValue(t.Context(), recordingTx(), testScope, recordedDefinition(), before, after))

		entry, delivery := l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.EqOp(t, "dark", entry.Changes[rawValueField].Old)
		test.EqOp(t, "light", entry.Changes[rawValueField].New)
		test.MapLen(t, 2, entry.Changes)
		test.Eq(t, []string{rawValueField}, decodeEvent[ValueEvent](t, delivery).Changed)
	})

	T.Run("a clearing records the answer withdrawn and the archive stamp", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		cleared := recordedValue("dark")
		archived := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		cleared.ArchivedAt = &archived

		must.NoError(t, newRecordingHooks(t, l).AfterClearValue(t.Context(), recordingTx(), testScope, recordedDefinition(), cleared))

		entry, delivery := l.last(t)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, "dark", entry.Changes[rawValueField].Old)
		test.Nil(t, entry.Changes[rawValueField].New)
		test.MapContainsKey(t, entry.Changes, "archivedAt")
		test.MapLen(t, 2, entry.Changes)
		test.EqOp(t, EventValueCleared, delivery.EventType)
		test.SliceEmpty(t, decodeEvent[ValueEvent](t, delivery).Changed)
	})

	T.Run("a nil row is refused by name", func(t *testing.T) {
		t.Parallel()

		hooks := newRecordingHooks(t, &ledger{})
		test.ErrorIs(t, hooks.AfterSetValue(t.Context(), recordingTx(), testScope, nil, nil, recordedValue("x")), ErrNilDefinition)
		test.ErrorIs(t, hooks.AfterSetValue(t.Context(), recordingTx(), testScope, recordedDefinition(), nil, nil), ErrNilValue)
		test.ErrorIs(t, hooks.AfterClearValue(t.Context(), recordingTx(), testScope, nil, recordedValue("x")), ErrNilDefinition)
		test.ErrorIs(t, hooks.AfterClearValue(t.Context(), recordingTx(), testScope, recordedDefinition(), nil), ErrNilValue)
	})
}

func TestRecordingHooks_Erasure(T *testing.T) {
	T.Parallel()

	T.Run("records the count and the kind of subject, and nothing that identifies them", func(t *testing.T) {
		t.Parallel()

		for _, deleted := range []int64{0, 3} {
			l := &ledger{}
			subject := Subject{Type: "user", ID: "user-7"}
			must.NoError(t, newRecordingHooks(t, l).AfterDeleteValuesForSubject(t.Context(), recordingTx(), testScope, subject, deleted))

			entry, delivery := l.last(t)
			test.EqOp(t, ResourceTypeValue, entry.ResourceType)
			test.EqOp(t, "", entry.ResourceID)
			test.EqOp(t, audit.EventDeleted, entry.EventType)
			test.Eq(t, map[string]string{
				metadataSubjectType: "user",
				metadataDeleted:     map[int64]string{0: "0", 3: "3"}[deleted],
			}, entry.Metadata)
			test.EqOp(t, EventValuesErased, delivery.EventType)
			test.Eq(t, &ErasureEvent{SubjectType: "user", Deleted: deleted}, decodeEvent[ErasureEvent](t, delivery))
			test.StrNotContains(t, string(delivery.Payload), "user-7")
		}
	})
}

func TestRecordingHooks_Refusal(T *testing.T) {
	T.Parallel()

	T.Run("a refused entry fails the write", func(t *testing.T) {
		t.Parallel()

		l := &ledger{refuse: errHook}
		err := newRecordingHooks(t, l).AfterCreateDefinition(t.Context(), recordingTx(), testScope, recordedDefinition())
		test.ErrorIs(t, err, errHook)
		test.SliceEmpty(t, l.deliveries)
	})
}

func TestRecordingHooks_InstalledOnTheStore(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("a store's writes reach the recorder with the rows the store read", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		created := mustCreate(t, env, store, testScope, stringDefinition("digest"))
		mustSet(t, env, store, testScope, testSubject, "digest", "daily")
		mustSet(t, env, store, testScope, testSubject, "digest", "weekly")

		must.SliceLen(t, 3, l.entries)
		must.SliceLen(t, 3, l.deliveries)
		test.EqOp(t, created.ID, l.entries[0].ResourceID)
		test.EqOp(t, ResourceTypeValue, l.entries[1].ResourceType)
		test.EqOp(t, string(testSubject.Type), l.entries[1].Metadata[metadataSubjectType])
		test.EqOp(t, "daily", l.entries[2].Changes[rawValueField].Old)
		test.EqOp(t, "weekly", l.entries[2].Changes[rawValueField].New)
		test.EqOp(t, EventValueSet, l.deliveries[2].EventType)
	})
}

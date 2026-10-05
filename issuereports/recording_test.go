package issuereports

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

// triagerPrincipal is the caller every recorded write here is attributed to.
type triagerPrincipal struct{}

func (triagerPrincipal) UserID() string          { return "triager-1" }
func (triagerPrincipal) Scope() tenancy.Scope    { return testScope }
func (triagerPrincipal) ActiveAccountID() string { return "" }

func triager(context.Context) (callers.Principal, bool) { return triagerPrincipal{}, true }

// ledger is everything the two halves were handed, in order.
type ledger struct {
	entries    []*audit.Entry
	deliveries []*webhooks.Delivery
}

// newRecordingHooks builds RecordingHooks over mocks that write into the
// ledger, with the dispatcher's catalog knowing every event this package emits.
func newRecordingHooks(t *testing.T, l *ledger, opts ...recording.Option) *RecordingHooks {
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
		CatalogFunc: EventCatalog,
		DispatchFunc: func(_ context.Context, _ database.Tx, _ tenancy.Scope, delivery *webhooks.Delivery) error {
			l.deliveries = append(l.deliveries, delivery)

			return nil
		},
	}

	emitter, err := webhooks.NewEmitter(enqueuer, dispatcher, "events")
	must.NoError(t, err)

	recorder, err := recording.New(entries, emitter, triager, opts...)
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

func decodeEvent[T any](t *testing.T, delivery *webhooks.Delivery) *T {
	t.Helper()

	var event T
	must.NoError(t, json.Unmarshal(delivery.Payload, &event))

	return &event
}

func recordingTx() database.Tx { return database.NewTxForTesting(nil) }

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
			EventReportCreated, EventReportUpdated, EventReportTransitioned, EventReportArchived, EventReportsErased,
		} {
			test.True(t, catalog.Known(eventType), test.Sprintf("%s is not in the catalog", eventType))
			test.NotEqOp(t, "", catalog[eventType].Description)
		}

		test.MapLen(t, 5, catalog)
	})

	T.Run("hands out a fresh copy each time", func(t *testing.T) {
		t.Parallel()

		delete(EventCatalog(), EventReportCreated)
		test.True(t, EventCatalog().Known(EventReportCreated))
	})
}

func TestRecordingHooks(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("every write records one entry and one event, both naming the report", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		var subjects []string
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l,
			recording.WithScopeResolver(func(_ context.Context, scope tenancy.Scope, entry *recording.Entry) tenancy.Scope {
				subjects = append(subjects, entry.SubjectID)

				return scope
			}))))

		created, err := env.create(t, store, testScope, newReport(testReporter, "bug", "the button does nothing"))
		must.NoError(t, err)
		entry, delivery := l.last(t)
		test.EqOp(t, ResourceTypeReport, entry.ResourceType)
		test.EqOp(t, created.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.EqOp(t, testScope, entry.Scope)
		test.EqOp(t, "triager-1", entry.Actor.ID)
		test.Eq(t, map[string]string{metadataKind: "bug", metadataStatus: "open"}, entry.Metadata)
		test.EqOp(t, EventReportCreated, delivery.EventType)
		test.EqOp(t, created.ID, delivery.OrderingKey)
		test.Eq(t, &ReportEvent{ReportID: created.ID, Reporter: testReporter, Kind: "bug", Status: StatusOpen},
			decodeEvent[ReportEvent](t, delivery))
		test.StrNotContains(t, string(delivery.Payload), "the button does nothing")

		// A revision never records a transition.
		revision := *created
		revision.Details = "the button does nothing, twice"
		_, err = env.update(t, store, testScope, &revision)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		// The entry says the details changed, and never what they said.
		test.Eq(t, hashedChange(t, "details", "the button does nothing", "the button does nothing, twice"), entry.Changes["details"])
		test.MapNotContainsKey(t, entry.Changes, "status")
		test.MapNotContainsKey(t, entry.Metadata, metadataPreviousStatus)
		test.EqOp(t, EventReportUpdated, delivery.EventType)
		updated := decodeEvent[ReportEvent](t, delivery)
		test.EqOp(t, StatusOpen, updated.Status)
		test.EqOp(t, "", updated.PreviousStatus)
		test.Eq(t, []string{"details"}, updated.Changed)

		_, err = env.transition(t, store, testScope, created.ID, StatusOpen, StatusResolved, "fixed")
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.EqOp(t, "resolved", entry.Metadata[metadataStatus])
		test.EqOp(t, "open", entry.Metadata[metadataPreviousStatus])
		test.EqOp(t, EventReportTransitioned, delivery.EventType)
		resolved := decodeEvent[ReportEvent](t, delivery)
		test.EqOp(t, StatusResolved, resolved.Status)
		test.EqOp(t, StatusOpen, resolved.PreviousStatus)
		test.Eq(t, []string{"closedAt", "resolution", "status"}, resolved.Changed)

		// Who resolved it, and that its note changed, is answerable after a
		// reopen clears both from the row; what the note said is not.
		_, err = env.transition(t, store, testScope, created.ID, StatusResolved, StatusOpen, "")
		must.NoError(t, err)
		entry, _ = l.last(t)
		test.Eq(t, hashedChange(t, "resolution", "fixed", ""), entry.Changes["resolution"])
		test.EqOp[any](t, StatusResolved, entry.Changes["status"].Old)
		test.NotNil(t, entry.Changes["closedAt"].Old)
		test.EqOp(t, "triager-1", entry.Actor.ID)

		_, err = env.archive(t, store, testScope, created.ID)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, "open", entry.Metadata[metadataStatus])
		test.EqOp(t, EventReportArchived, delivery.EventType)

		test.Eq(t, []string{testReporter, testReporter, testReporter, testReporter, testReporter}, subjects)

		for _, recorded := range l.entries {
			test.MapNotContainsKey(t, recorded.Metadata, "reporter")
		}
	})

	T.Run("an erasure records the count and nothing that identifies the reporter, zero included", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		_, err := env.create(t, store, testScope, newReport(testReporter, "bug", "mine"))
		must.NoError(t, err)
		l.entries, l.deliveries = nil, nil

		for _, want := range []int64{1, 0} {
			deleted, eraseErr := env.erase(t, store, testScope, testReporter)
			must.NoError(t, eraseErr)
			test.EqOp(t, want, deleted)

			entry, delivery := l.last(t)
			test.EqOp(t, ResourceTypeReport, entry.ResourceType)
			test.EqOp(t, "", entry.ResourceID)
			test.EqOp(t, audit.EventDeleted, entry.EventType)
			test.Eq(t, map[string]string{metadataDeleted: map[int64]string{0: "0", 1: "1"}[want]}, entry.Metadata)
			test.EqOp(t, EventReportsErased, delivery.EventType)
			test.Eq(t, &ErasureEvent{Deleted: want}, decodeEvent[ErasureEvent](t, delivery))
			test.StrNotContains(t, string(delivery.Payload), testReporter)
		}
	})

	T.Run("a nil row is refused by name", func(t *testing.T) {
		t.Parallel()

		hooks := newRecordingHooks(t, &ledger{})
		test.ErrorIs(t, hooks.AfterCreateReport(t.Context(), recordingTx(), testScope, nil), ErrNilReport)
		test.ErrorIs(t, hooks.AfterUpdateReport(t.Context(), recordingTx(), testScope, nil, &Report{}), ErrNilReport)
		test.ErrorIs(t, hooks.AfterTransitionReport(t.Context(), recordingTx(), testScope, &Report{}, nil), ErrNilReport)
		test.ErrorIs(t, hooks.AfterArchiveReport(t.Context(), recordingTx(), testScope, nil), ErrNilReport)
	})
}

// hashedChange is the change a write records for a field RecordingHooks hashes:
// the digests audit writes for a Hash rule, never the text.
func hashedChange(t *testing.T, field, old, updated string) audit.Change {
	t.Helper()

	hashed, err := audit.Redaction{Hash: []string{field}}.Apply(map[string]audit.Change{field: {Old: old, New: updated}})
	must.NoError(t, err)

	test.StrHasPrefix(t, "sha256:", hashed[field].Old.(string))

	return hashed[field]
}

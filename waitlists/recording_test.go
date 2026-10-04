package waitlists

import (
	"context"
	"encoding/json"
	"strconv"
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

func decodeList(t *testing.T, delivery *webhooks.Delivery) *ListEvent {
	t.Helper()

	var event ListEvent
	must.NoError(t, json.Unmarshal(delivery.Payload, &event))

	return &event
}

func decodeSignup(t *testing.T, delivery *webhooks.Delivery) *SignupEvent {
	t.Helper()

	var event SignupEvent
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
			EventListCreated, EventListUpdated, EventListArchived,
			EventSignupJoined, EventSignupNotesUpdated, EventSignupConfirmed, EventSignupInvited,
			EventSignupConverted, EventSignupWithdrawn, EventSignupArchived, EventSignupsErased,
		} {
			test.True(t, catalog.Known(eventType), test.Sprintf("%s is not in the catalog", eventType))
			test.NotEqOp(t, "", catalog[eventType].Description)
		}

		test.MapLen(t, 11, catalog)
	})

	T.Run("hands out a fresh copy each time", func(t *testing.T) {
		t.Parallel()

		delete(EventCatalog(), EventListCreated)
		test.True(t, EventCatalog().Known(EventListCreated))
	})
}

func TestRecordingHooks(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("every write records one entry and one event, both naming the row", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		list := mustCreateList(t, env, store, testScope, openList("Launch"))
		entry, delivery := l.last(t)
		test.EqOp(t, ResourceTypeList, entry.ResourceType)
		test.EqOp(t, list.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.EqOp(t, testScope, entry.Scope)
		test.EqOp(t, "operator-1", entry.Actor.ID)
		test.Eq(t, map[string]string{metadataListID: list.ID}, entry.Metadata)
		test.EqOp(t, EventListCreated, delivery.EventType)
		test.EqOp(t, list.ID, delivery.OrderingKey)
		test.Eq(t, &ListEvent{ListID: list.ID, Name: "Launch"}, decodeList(t, delivery))

		list.Description = "rewritten"
		_, err := env.updateList(t, store, testScope, list)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.MapContainsKey(t, entry.Changes, "description")
		test.EqOp(t, "early access to the beta", entry.Changes["description"].Old)
		test.EqOp(t, "rewritten", entry.Changes["description"].New)
		test.MapContainsKey(t, entry.Changes, lastUpdatedAtField)
		test.EqOp(t, EventListUpdated, delivery.EventType)
		// The diff names the timestamp the save stamped; the changed list does not.
		test.Eq(t, []string{"description"}, decodeList(t, delivery).Changed)

		signup := mustJoin(t, env, store, testScope, list.ID, &Signup{
			Contact: "ada@example.com",
			Subject: testSubject,
			Status:  StatusPending,
		})
		entry, delivery = l.last(t)
		test.EqOp(t, ResourceTypeSignup, entry.ResourceType)
		test.EqOp(t, signup.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.Eq(t, map[string]string{
			metadataListID:      list.ID,
			metadataStatus:      "pending",
			metadataSubjectType: "user",
		}, entry.Metadata)
		test.EqOp(t, EventSignupJoined, delivery.EventType)
		test.EqOp(t, signup.ID, delivery.OrderingKey)
		joined := decodeSignup(t, delivery)
		test.EqOp(t, signup.ID, joined.SignupID)
		test.EqOp(t, list.ID, joined.ListID)
		test.EqOp(t, testSubject, joined.Subject)
		test.EqOp(t, signup.ContactDigest, joined.ContactDigest)
		test.EqOp(t, StatusPending, joined.Status)
		test.EqOp(t, Status(""), joined.PreviousStatus)

		_, err = env.updateNotes(t, store, testScope, list.ID, signup.ID, "met at the conference")
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.EqOp(t, "met at the conference", entry.Changes["notes"].New)
		test.EqOp(t, EventSignupNotesUpdated, delivery.EventType)
		test.Eq(t, []string{"notes"}, decodeSignup(t, delivery).Changed)

		_, err = env.confirm(t, store, testScope, list.ID, signup.ID)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, "waiting", entry.Metadata[metadataStatus])
		test.EqOp(t, "pending", entry.Metadata[metadataPreviousStatus])
		test.EqOp(t, EventSignupConfirmed, delivery.EventType)
		confirmed := decodeSignup(t, delivery)
		test.EqOp(t, StatusWaiting, confirmed.Status)
		test.EqOp(t, StatusPending, confirmed.PreviousStatus)

		mustInvite(t, env, store, testScope, list.ID, signup.ID)
		_, delivery = l.last(t)
		test.EqOp(t, EventSignupInvited, delivery.EventType)
		invited := decodeSignup(t, delivery)
		test.EqOp(t, StatusInvited, invited.Status)
		test.EqOp(t, StatusWaiting, invited.PreviousStatus)

		_, err = env.convert(t, store, testScope, list.ID, signup.ID)
		must.NoError(t, err)
		_, delivery = l.last(t)
		test.EqOp(t, EventSignupConverted, delivery.EventType)
		converted := decodeSignup(t, delivery)
		test.EqOp(t, StatusConverted, converted.Status)
		test.EqOp(t, StatusInvited, converted.PreviousStatus)

		mustArchiveSignup(t, env, store, testScope, list.ID, signup.ID)
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, EventSignupArchived, delivery.EventType)

		mustArchiveList(t, env, store, testScope, list.ID)
		entry, delivery = l.last(t)
		test.EqOp(t, ResourceTypeList, entry.ResourceType)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, EventListArchived, delivery.EventType)

		test.SliceLen(t, 9, l.entries)
		test.SliceLen(t, 9, l.deliveries)
	})

	T.Run("a withdrawal names the signup by id and digest, never by address", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		list := mustCreateList(t, env, store, testScope, openList("Launch"))
		signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com", Subject: testSubject})

		mustWithdraw(t, env, store, testScope, list.ID, signup.ID)

		entry, delivery := l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.EqOp(t, "withdrawn", entry.Metadata[metadataStatus])
		test.EqOp(t, "waiting", entry.Metadata[metadataPreviousStatus])
		test.MapNotContainsValue(t, entry.Metadata, testSubject.ID)
		test.MapEmpty(t, entry.Changes)

		test.EqOp(t, EventSignupWithdrawn, delivery.EventType)
		withdrawn := decodeSignup(t, delivery)
		test.EqOp(t, StatusWithdrawn, withdrawn.Status)
		test.EqOp(t, StatusWaiting, withdrawn.PreviousStatus)
		test.EqOp(t, signup.ContactDigest, withdrawn.ContactDigest)
		test.EqOp(t, testSubject, withdrawn.Subject)
		test.StrNotContains(t, string(delivery.Payload), "ada@example.com")
	})

	T.Run("an erasure records its count and the kind of subject, and never the subject, zero included", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		list := mustCreateList(t, env, store, testScope, openList("Launch"))
		mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com", Subject: testSubject})

		for _, want := range []int64{1, 0} {
			must.NoError(t, env.inTx(t, func(tx database.Tx) error {
				withdrawn, err := store.WithdrawSignupsForSubject(t.Context(), tx, testScope, testSubject)
				test.EqOp(t, want, withdrawn)

				return err
			}))

			entry, delivery := l.last(t)
			test.EqOp(t, ResourceTypeSignup, entry.ResourceType)
			test.EqOp(t, "", entry.ResourceID)
			test.EqOp(t, audit.EventUpdated, entry.EventType)
			test.EqOp(t, testScope, entry.Scope)
			test.Eq(t, map[string]string{metadataSubjectType: "user", metadataWithdrawn: strconv.FormatInt(want, 10)}, entry.Metadata)

			test.EqOp(t, EventSignupsErased, delivery.EventType)
			var erased ErasureEvent
			must.NoError(t, json.Unmarshal(delivery.Payload, &erased))
			test.EqOp(t, SubjectUser, erased.SubjectType)
			test.EqOp(t, want, erased.Withdrawn)
			test.StrNotContains(t, string(delivery.Payload), testSubject.ID)
		}

		// One list, one join, two erasures.
		test.SliceLen(t, 4, l.entries)
		test.SliceLen(t, 4, l.deliveries)
	})

	T.Run("a recorder filing by subject puts a signup's entries on the subject's chain and a list's where the write ran", func(t *testing.T) {
		t.Parallel()

		bySubject := func(_ context.Context, scope tenancy.Scope, entry *recording.Entry) tenancy.Scope {
			if entry.SubjectID != "" {
				return tenancy.Of(entry.SubjectID)
			}

			return scope
		}

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l, recording.WithScopeResolver(bySubject))))

		list := mustCreateList(t, env, store, testScope, openList("Launch"))
		mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com", Subject: testSubject})
		mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "visitor@example.com"})

		must.SliceLen(t, 3, l.entries)
		test.EqOp(t, testScope, l.entries[0].Scope)
		test.EqOp(t, tenancy.Of(testSubject.ID), l.entries[1].Scope)
		// A visitor's signup names no subject, so it stays with the write.
		test.EqOp(t, testScope, l.entries[2].Scope)
	})

	T.Run("a refused recording fails the write, and the row with it", func(t *testing.T) {
		t.Parallel()

		l := &ledger{refuse: platformerrors.New("the log said no")}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		created, err := env.createList(t, store, testScope, openList("Launch"))
		test.ErrorIs(t, err, l.refuse)
		test.Nil(t, created)
		test.SliceEmpty(t, l.deliveries)
	})
}

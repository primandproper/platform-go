package passkeys

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

// testPrincipal is the caller every recorded write here is attributed to.
type testPrincipal struct{}

func (testPrincipal) UserID() string          { return aliceID }
func (testPrincipal) Scope() tenancy.Scope    { return testScope }
func (testPrincipal) ActiveAccountID() string { return "" }

func userPrincipal(context.Context) (callers.Principal, bool) { return testPrincipal{}, true }

// ledger is everything the two halves were handed, in order: the entries, the
// messages the outbox was given, and whatever reached a subscriber.
type ledger struct {
	refuse     error
	entries    []*audit.Entry
	published  []outbox.Message
	deliveries []*webhooks.Delivery
}

// newRecordingHooks builds RecordingHooks over mocks that write into the
// ledger, with the dispatcher's catalog exactly what EventCatalog hands it.
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
		EnqueueFunc: func(_ context.Context, _ database.Tx, msgs ...outbox.Message) error {
			l.published = append(l.published, msgs...)

			return nil
		},
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

	recorder, err := recording.New(entries, emitter, userPrincipal)
	must.NoError(t, err)

	hooks, err := NewRecordingHooks(recorder)
	must.NoError(t, err)

	return hooks
}

// last is the most recent entry and published message, which every recorded
// write here produces exactly one of.
func (l *ledger) last(t *testing.T) (*audit.Entry, *CredentialEvent) {
	t.Helper()

	must.SliceNotEmpty(t, l.entries)
	must.SliceNotEmpty(t, l.published)

	// The emitter hands the outbox an envelope around the payload, and this
	// reads it the way a queue consumer would: rendered, then decoded by the
	// event type it names.
	rendered, err := json.Marshal(l.published[len(l.published)-1].Payload)
	must.NoError(t, err)

	var event CredentialEvent

	eventType, matched, err := webhooks.Decode(rendered, &event, EventPasskeyRegistered, EventPasskeyArchived)
	must.NoError(t, err)
	must.True(t, matched, must.Sprintf("published event type is %q", eventType))

	return l.entries[len(l.entries)-1], &event
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

	T.Run("defines every event this package emits, and marks each Internal", func(t *testing.T) {
		t.Parallel()

		emitted := []webhooks.EventType{EventPasskeyRegistered, EventPasskeyArchived}

		catalog := EventCatalog()
		test.MapLen(t, len(emitted), catalog)

		for _, eventType := range emitted {
			test.True(t, catalog.Known(eventType), test.Sprintf("%s is not in the catalog", eventType))
			test.NotEqOp(t, "", catalog[eventType].Description)
			test.True(t, catalog[eventType].Internal, test.Sprintf("%s is not Internal", eventType))
			test.False(t, catalog.Subscribable(eventType), test.Sprintf("%s is subscribable", eventType))
		}
	})

	T.Run("hands out a fresh copy each time", func(t *testing.T) {
		t.Parallel()

		delete(EventCatalog(), EventPasskeyRegistered)
		test.True(t, EventCatalog().Known(EventPasskeyRegistered))
	})
}

func TestRecordingHooks(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("a registration and a revocation are recorded and published, and reach no subscriber", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		f := env.newService(t, WithHooks(newRecordingHooks(t, l)))

		f.mustRegister(t, aliceID)
		_, registered := f.mustRegister(t, aliceID)

		entry, event := l.last(t)
		test.EqOp(t, ResourceTypeCredential, entry.ResourceType)
		test.EqOp(t, registered.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.EqOp(t, testScope, entry.Scope)
		test.EqOp(t, aliceID, entry.Actor.ID)
		test.MapEmpty(t, entry.Metadata)
		test.Eq(t, &CredentialEvent{
			CredentialID: registered.ID,
			UserID:       aliceID,
			FriendlyName: "Phone",
		}, event)
		test.EqOp(t, registered.ID, l.published[len(l.published)-1].Key)

		_, err := f.archive(t, registered.ID, aliceID)
		must.NoError(t, err)

		entry, event = l.last(t)
		test.EqOp(t, registered.ID, entry.ResourceID)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, registered.ID, event.CredentialID)
		test.EqOp(t, "Phone", event.FriendlyName)

		test.SliceLen(t, 3, l.entries)
		test.SliceLen(t, 3, l.published)
		test.SliceEmpty(t, l.deliveries)
	})

	T.Run("a refused login records nothing", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		f := env.newService(t, WithHooks(newRecordingHooks(t, l)))

		f.mustRegister(t, aliceID)
		recorded := len(l.entries)

		// A device nobody registered proves nobody.
		_, err := f.login(t, "alice", newAuthenticator(t, []byte(aliceID)))
		must.ErrorIs(t, err, ErrLoginFailed)

		test.SliceLen(t, recorded, l.entries)
		test.SliceLen(t, recorded, l.published)
	})

	T.Run("a refused recording fails the registration, and the row with it", func(t *testing.T) {
		t.Parallel()

		l := &ledger{refuse: platformerrors.New("the log said no")}
		f := env.newService(t, WithHooks(newRecordingHooks(t, l)))

		_, registered, err := f.register(t, aliceID)
		test.ErrorIs(t, err, l.refuse)
		test.Nil(t, registered)
		test.SliceEmpty(t, l.published)
		test.SliceEmpty(t, f.live(t, aliceID))
	})

	T.Run("a nil row is refused by name", func(t *testing.T) {
		t.Parallel()

		hooks := newRecordingHooks(t, &ledger{})

		test.ErrorIs(t, hooks.AfterRegisterPasskey(t.Context(), nil, testScope, nil), ErrNilCredential)
		test.ErrorIs(t, hooks.AfterArchivePasskey(t.Context(), nil, testScope, nil), ErrNilCredential)
	})
}

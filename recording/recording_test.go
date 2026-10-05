package recording

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	auditmock "github.com/primandproper/platform-go/v14/audit/mock"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/outbox"
	"github.com/primandproper/platform-go/v14/webhooks"
	webhooksmock "github.com/primandproper/platform-go/v14/webhooks/mock"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const (
	testTopic                        = "events"
	testEventType webhooks.EventType = "thing.created"
)

var (
	testScope  = tenancy.Of("acme")
	otherScope = tenancy.Of("other")

	errRefused = platformerrors.New("refused")
)

// principal is the smallest callers.Principal a test can hold.
type principal struct {
	userID string
	scope  tenancy.Scope
}

func (p principal) UserID() string          { return p.userID }
func (p principal) Scope() tenancy.Scope    { return p.scope }
func (p principal) ActiveAccountID() string { return "" }

// somebody is an extractor that always finds the same user.
func somebody(userID string) callers.PrincipalExtractor {
	return func(context.Context) (callers.Principal, bool) {
		return principal{userID: userID, scope: testScope}, true
	}
}

// nobody is an extractor that never finds a principal.
func nobody(context.Context) (callers.Principal, bool) { return nil, false }

// recorded is what one Record call left behind in the two halves.
type recorded struct {
	// batches is every call the audit recorder received, in order: the scope
	// it was asked to chain into and the entries it was handed.
	batches []batch
	// messages is every outbox message the emitter enqueued.
	messages []outbox.Message
	// deliveries is every delivery the dispatcher was handed.
	deliveries []*webhooks.Delivery
}

type batch struct {
	entries []*audit.Entry
	scope   tenancy.Scope
}

// harness is a Recorder over mocks that remember everything, with a switch for
// each half to refuse.
type harness struct {
	recorder *Recorder
	got      *recorded
}

func newHarness(t *testing.T, principals callers.PrincipalExtractor, refuseEntries, refuseEvents bool, opts ...Option) *harness {
	t.Helper()

	got := &recorded{}

	entries := &auditmock.RecorderMock{
		RecordFunc: func(_ context.Context, _ database.Tx, scope tenancy.Scope, entries ...*audit.Entry) error {
			got.batches = append(got.batches, batch{scope: scope, entries: entries})

			if refuseEntries {
				return errRefused
			}

			return nil
		},
	}

	enqueuer := &webhooksmock.EnqueuerMock{
		EnqueueFunc: func(_ context.Context, _ database.Tx, msgs ...outbox.Message) error {
			got.messages = append(got.messages, msgs...)

			if refuseEvents {
				return errRefused
			}

			return nil
		},
	}

	dispatcher := &webhooksmock.DispatcherMock{
		CatalogFunc: func() webhooks.Catalog {
			return webhooks.Catalog{testEventType: {Description: "a thing was created"}}
		},
		DispatchFunc: func(_ context.Context, _ database.Tx, _ tenancy.Scope, delivery *webhooks.Delivery) error {
			got.deliveries = append(got.deliveries, delivery)

			return nil
		},
	}

	emitter, err := webhooks.NewEmitter(enqueuer, dispatcher, testTopic)
	must.NoError(t, err)

	recorder, err := New(entries, emitter, principals, opts...)
	must.NoError(t, err)

	return &harness{recorder: recorder, got: got}
}

// tx is a Tx the mocks never touch.
func tx() database.Tx { return database.NewTxForTesting(nil) }

func anEntry(resourceID string) *Entry {
	return &Entry{
		ResourceType: "thing",
		ResourceID:   resourceID,
		EventType:    audit.EventCreated,
		Metadata:     map[string]string{"why": "because"},
	}
}

func anEvent() *webhooks.Event {
	return &webhooks.Event{EventType: testEventType, OrderingKey: "thing-1", Payload: map[string]string{"id": "thing-1"}}
}

func TestNew(T *testing.T) {
	T.Parallel()

	emitter, err := webhooks.NewEmitter(&webhooksmock.EnqueuerMock{}, &webhooksmock.DispatcherMock{}, testTopic)
	must.NoError(T, err)

	T.Run("builds over the three halves it needs, tolerating a nil option", func(t *testing.T) {
		t.Parallel()

		recorder, buildErr := New(&auditmock.RecorderMock{}, emitter, nobody, nil)
		must.NoError(t, buildErr)
		must.NotNil(t, recorder)
	})

	T.Run("refuses a nil audit recorder", func(t *testing.T) {
		t.Parallel()

		recorder, buildErr := New(nil, emitter, nobody)
		test.Nil(t, recorder)
		test.ErrorIs(t, buildErr, ErrNilAuditRecorder)
		test.ErrorIs(t, buildErr, platformerrors.ErrNilInputParameter)
	})

	T.Run("refuses a nil emitter", func(t *testing.T) {
		t.Parallel()

		recorder, buildErr := New(&auditmock.RecorderMock{}, nil, nobody)
		test.Nil(t, recorder)
		test.ErrorIs(t, buildErr, ErrNilEmitter)
	})

	T.Run("refuses a nil principal extractor", func(t *testing.T) {
		t.Parallel()

		recorder, buildErr := New(&auditmock.RecorderMock{}, emitter, nil)
		test.Nil(t, recorder)
		test.ErrorIs(t, buildErr, ErrNilPrincipalExtractor)
	})
}

func TestRecorder_Record(T *testing.T) {
	T.Parallel()

	T.Run("writes the entries and then the event, under the write's scope, as the caller", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, somebody("user-1"), false, false)

		err := h.recorder.Record(t.Context(), tx(), testScope, anEvent(), anEntry("thing-1"), anEntry("thing-2"))
		must.NoError(t, err)

		must.SliceLen(t, 1, h.got.batches)
		test.EqOp(t, testScope, h.got.batches[0].scope)
		must.SliceLen(t, 2, h.got.batches[0].entries)

		for i, entry := range h.got.batches[0].entries {
			test.EqOp(t, "user-1", entry.Actor.ID)
			test.EqOp(t, audit.ActorUser, entry.Actor.Type)
			test.EqOp(t, testScope, entry.Scope)
			test.EqOp(t, "thing", entry.ResourceType)
			test.EqOp(t, []string{"thing-1", "thing-2"}[i], entry.ResourceID)
			test.EqOp(t, audit.EventCreated, entry.EventType)
			test.Eq(t, map[string]string{"why": "because"}, entry.Metadata)
		}

		must.SliceLen(t, 1, h.got.messages)
		test.EqOp(t, testTopic, h.got.messages[0].Topic)
		test.EqOp(t, "thing-1", h.got.messages[0].Key)

		must.SliceLen(t, 1, h.got.deliveries)
		test.EqOp(t, testEventType, h.got.deliveries[0].EventType)
	})

	T.Run("a context with no principal records the named absence, never an empty actor", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nobody, false, false)

		must.NoError(t, h.recorder.Record(t.Context(), tx(), testScope, nil, anEntry("thing-1")))

		must.SliceLen(t, 1, h.got.batches)
		must.SliceLen(t, 1, h.got.batches[0].entries)
		test.EqOp(t, audit.ActorUnattributed, h.got.batches[0].entries[0].Actor.ID)
		test.EqOp(t, audit.ActorType(audit.ActorUnattributed), h.got.batches[0].entries[0].Actor.Type)
	})

	T.Run("a scope resolver files each entry where it says, one batch per chain, in order of first appearance", func(t *testing.T) {
		t.Parallel()

		bySubject := func(_ context.Context, scope tenancy.Scope, entry *Entry) tenancy.Scope {
			if entry.SubjectID != "" {
				return tenancy.Of(entry.SubjectID)
			}

			return scope
		}

		h := newHarness(t, somebody("user-1"), false, false, WithScopeResolver(bySubject))

		about := anEntry("thing-2")
		about.SubjectID = otherScope.Owner()

		must.NoError(t, h.recorder.Record(t.Context(), tx(), testScope, anEvent(), anEntry("thing-1"), about, anEntry("thing-3")))

		must.SliceLen(t, 2, h.got.batches)

		test.EqOp(t, testScope, h.got.batches[0].scope)
		must.SliceLen(t, 2, h.got.batches[0].entries)
		test.EqOp(t, "thing-1", h.got.batches[0].entries[0].ResourceID)
		test.EqOp(t, "thing-3", h.got.batches[0].entries[1].ResourceID)
		test.EqOp(t, testScope, h.got.batches[0].entries[0].Scope)

		test.EqOp(t, otherScope, h.got.batches[1].scope)
		must.SliceLen(t, 1, h.got.batches[1].entries)
		test.EqOp(t, "thing-2", h.got.batches[1].entries[0].ResourceID)
		test.EqOp(t, otherScope, h.got.batches[1].entries[0].Scope)

		// The event is the write's, not the subject's: it fans out to the
		// tenant the write ran in.
		must.SliceLen(t, 1, h.got.deliveries)
	})

	T.Run("a nil scope resolver keeps the default", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, somebody("user-1"), false, false, WithScopeResolver(nil))

		must.NoError(t, h.recorder.Record(t.Context(), tx(), testScope, nil, anEntry("thing-1")))
		must.SliceLen(t, 1, h.got.batches)
		test.EqOp(t, testScope, h.got.batches[0].scope)
	})

	T.Run("entries alone are recorded and nothing is emitted", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, somebody("user-1"), false, false)

		must.NoError(t, h.recorder.Record(t.Context(), tx(), testScope, nil, anEntry("thing-1")))
		test.SliceLen(t, 1, h.got.batches)
		test.SliceEmpty(t, h.got.messages)
		test.SliceEmpty(t, h.got.deliveries)
	})

	T.Run("an event alone is emitted and nothing is recorded", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, somebody("user-1"), false, false)

		must.NoError(t, h.recorder.Record(t.Context(), tx(), testScope, anEvent()))
		test.SliceEmpty(t, h.got.batches)
		test.SliceLen(t, 1, h.got.messages)
		test.SliceLen(t, 1, h.got.deliveries)
	})

	T.Run("neither is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, somebody("user-1"), false, false)

		err := h.recorder.Record(t.Context(), tx(), testScope, nil)
		test.ErrorIs(t, err, ErrNothingToRecord)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
		test.SliceEmpty(t, h.got.batches)
		test.SliceEmpty(t, h.got.messages)
	})

	T.Run("a nil transaction is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, somebody("user-1"), false, false)

		err := h.recorder.Record(t.Context(), nil, testScope, anEvent(), anEntry("thing-1"))
		test.ErrorIs(t, err, ErrNilExecutor)
		test.SliceEmpty(t, h.got.batches)
		test.SliceEmpty(t, h.got.messages)
	})

	T.Run("an unset scope is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, somebody("user-1"), false, false)

		err := h.recorder.Record(t.Context(), tx(), tenancy.Scope{}, anEvent(), anEntry("thing-1"))
		test.Error(t, err)
		test.SliceEmpty(t, h.got.batches)
		test.SliceEmpty(t, h.got.messages)
	})

	T.Run("a nil entry is refused before anything is written", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, somebody("user-1"), false, false)

		err := h.recorder.Record(t.Context(), tx(), testScope, anEvent(), anEntry("thing-1"), nil)
		test.ErrorIs(t, err, ErrNilEntry)
		test.SliceEmpty(t, h.got.batches)
		test.SliceEmpty(t, h.got.messages)
	})

	T.Run("a refused entry fails the write before the event is enqueued", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, somebody("user-1"), true, false)

		err := h.recorder.Record(t.Context(), tx(), testScope, anEvent(), anEntry("thing-1"))
		test.ErrorIs(t, err, errRefused)
		test.SliceLen(t, 1, h.got.batches)
		test.SliceEmpty(t, h.got.messages)
		test.SliceEmpty(t, h.got.deliveries)
	})

	T.Run("a refused event fails the write", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, somebody("user-1"), false, true)

		err := h.recorder.Record(t.Context(), tx(), testScope, anEvent(), anEntry("thing-1"))
		test.ErrorIs(t, err, errRefused)
		test.SliceLen(t, 1, h.got.batches)
		test.SliceLen(t, 1, h.got.messages)
	})
}

func TestRecorder_RecordAs(T *testing.T) {
	T.Parallel()

	T.Run("files every entry under the named actor, whatever the context says", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, somebody("user-1"), false, false)

		named := audit.Actor{ID: "subject-1", Type: audit.ActorUser, Impersonator: "operator-1"}

		must.NoError(t, h.recorder.RecordAs(t.Context(), tx(), testScope, named, anEvent(), anEntry("thing-1"), anEntry("thing-2")))

		must.SliceLen(t, 1, h.got.batches)
		must.SliceLen(t, 2, h.got.batches[0].entries)

		for _, entry := range h.got.batches[0].entries {
			test.Eq(t, named, entry.Actor)
			test.EqOp(t, testScope, entry.Scope)
		}

		test.SliceLen(t, 1, h.got.messages)
	})

	T.Run("names an actor where the context names nobody", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nobody, false, false)

		must.NoError(t, h.recorder.RecordAs(t.Context(), tx(), testScope, audit.Actor{ID: "subject-1", Type: audit.ActorUser}, nil, anEntry("thing-1")))

		must.SliceLen(t, 1, h.got.batches)
		test.EqOp(t, "subject-1", h.got.batches[0].entries[0].Actor.ID)
	})

	T.Run("an actor with no ID is refused before anything is written", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, somebody("user-1"), false, false)

		err := h.recorder.RecordAs(t.Context(), tx(), testScope, audit.Actor{Type: audit.ActorUser}, anEvent(), anEntry("thing-1"))
		test.ErrorIs(t, err, ErrEmptyActor)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
		test.SliceEmpty(t, h.got.batches)
		test.SliceEmpty(t, h.got.messages)
	})

	T.Run("refuses what Record refuses", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, somebody("user-1"), false, false)
		named := audit.Actor{ID: "subject-1", Type: audit.ActorUser}

		test.ErrorIs(t, h.recorder.RecordAs(t.Context(), nil, testScope, named, anEvent()), ErrNilExecutor)
		test.ErrorIs(t, h.recorder.RecordAs(t.Context(), tx(), testScope, named, nil), ErrNothingToRecord)
		test.SliceEmpty(t, h.got.batches)
		test.SliceEmpty(t, h.got.messages)
	})
}

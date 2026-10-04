package comments

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

// moderatorPrincipal is the caller every recorded write here is attributed to:
// somebody who is neither of the test authors, so an archive's actor and its
// author can be told apart.
type moderatorPrincipal struct{}

func (moderatorPrincipal) UserID() string          { return "moderator-1" }
func (moderatorPrincipal) Scope() tenancy.Scope    { return testScope }
func (moderatorPrincipal) ActiveAccountID() string { return "" }

func moderator(context.Context) (callers.Principal, bool) { return moderatorPrincipal{}, true }

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

	recorder, err := recording.New(entries, emitter, moderator, opts...)
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
			EventCommentCreated, EventCommentUpdated, EventCommentArchived, EventCommentsErased,
		} {
			test.True(t, catalog.Known(eventType), test.Sprintf("%s is not in the catalog", eventType))
			test.NotEqOp(t, "", catalog[eventType].Description)
		}

		test.MapLen(t, 4, catalog)
	})

	T.Run("hands out a fresh copy each time", func(t *testing.T) {
		t.Parallel()

		delete(EventCatalog(), EventCommentCreated)
		test.True(t, EventCatalog().Known(EventCommentCreated))
	})
}

func TestRecordingHooks(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("every write but a sweep records one entry and one event, both naming the comment", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		var subjects []string
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l,
			recording.WithScopeResolver(func(_ context.Context, scope tenancy.Scope, entry *recording.Entry) tenancy.Scope {
				subjects = append(subjects, entry.SubjectID)

				return scope
			}))))

		root := written(t, env, store, newComment(testAuthor, "first draft"))
		entry, delivery := l.last(t)
		test.EqOp(t, ResourceTypeComment, entry.ResourceType)
		test.EqOp(t, root.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.EqOp(t, testScope, entry.Scope)
		test.EqOp(t, "moderator-1", entry.Actor.ID)
		test.Eq(t, map[string]string{
			metadataTargetType: testTarget.Type.String(),
			metadataTargetID:   testTarget.ID,
		}, entry.Metadata)
		test.EqOp(t, EventCommentCreated, delivery.EventType)
		test.EqOp(t, root.ID, delivery.OrderingKey)
		test.Eq(t, &CommentEvent{CommentID: root.ID, Target: testTarget, Author: testAuthor}, decodeEvent[CommentEvent](t, delivery))
		// The body is what the person said, and an event is no place for it.
		test.StrNotContains(t, string(delivery.Payload), "first draft")

		child := written(t, env, store, reply(root.ID, otherAuthor, "a reply"))
		entry, delivery = l.last(t)
		test.EqOp(t, root.ID, entry.Metadata[metadataParentID])
		test.EqOp(t, root.ID, decodeEvent[CommentEvent](t, delivery).ParentID)

		edit := *root
		edit.Body = "second draft"
		_, err := env.update(t, store, testScope, &edit)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.EqOp(t, "first draft", entry.Changes["body"].Old)
		test.EqOp(t, "second draft", entry.Changes["body"].New)
		test.MapContainsKey(t, entry.Changes, lastUpdatedAtField)
		test.EqOp(t, EventCommentUpdated, delivery.EventType)
		test.Eq(t, []string{"body"}, decodeEvent[CommentEvent](t, delivery).Changed)

		// The moderator removes the reply: the actor is who removed it, and the
		// entry and the event still name who wrote it.
		_, err = env.archive(t, store, testScope, child.ID)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, child.ID, entry.ResourceID)
		test.EqOp(t, "moderator-1", entry.Actor.ID)
		test.EqOp(t, EventCommentArchived, delivery.EventType)
		test.EqOp(t, otherAuthor, decodeEvent[CommentEvent](t, delivery).Author)

		test.Eq(t, []string{testAuthor, otherAuthor, testAuthor, otherAuthor}, subjects)

		for _, recorded := range l.entries {
			test.MapNotContainsKey(t, recorded.Metadata, "author")
		}

		// A sweep is the consumer's removal of the target, recorded by the write
		// that removed it.
		test.EqOp(t, int64(2), sweep(t, env, store, testScope, testTarget))
		test.SliceLen(t, 4, l.entries)
		test.SliceLen(t, 4, l.deliveries)
	})

	T.Run("an erasure records the count and nothing that identifies the author, zero included", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		written(t, env, store, newComment(testAuthor, "mine"))
		l.entries, l.deliveries = nil, nil

		for _, want := range []int64{1, 0} {
			deleted, err := eraseAuthor(t, env, store, testScope, testAuthor)
			must.NoError(t, err)
			test.EqOp(t, want, deleted)

			entry, delivery := l.last(t)
			test.EqOp(t, ResourceTypeComment, entry.ResourceType)
			test.EqOp(t, "", entry.ResourceID)
			test.EqOp(t, audit.EventDeleted, entry.EventType)
			test.Eq(t, map[string]string{metadataDeleted: map[int64]string{0: "0", 1: "1"}[want]}, entry.Metadata)
			test.EqOp(t, EventCommentsErased, delivery.EventType)
			test.Eq(t, &ErasureEvent{Deleted: want}, decodeEvent[ErasureEvent](t, delivery))
			test.StrNotContains(t, string(delivery.Payload), testAuthor)
		}
	})

	T.Run("a nil row is refused by name", func(t *testing.T) {
		t.Parallel()

		hooks := newRecordingHooks(t, &ledger{})
		test.ErrorIs(t, hooks.AfterCreateComment(t.Context(), recordingTx(), testScope, nil), ErrNilComment)
		test.ErrorIs(t, hooks.AfterUpdateComment(t.Context(), recordingTx(), testScope, nil, &Comment{}), ErrNilComment)
		test.ErrorIs(t, hooks.AfterArchiveComment(t.Context(), recordingTx(), testScope, nil), ErrNilComment)
	})
}

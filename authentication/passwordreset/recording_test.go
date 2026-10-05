package passwordreset

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
func (recordingPrincipal) Scope() tenancy.Scope    { return testScope() }
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

// last is the most recent entry and delivery, which every recorded write here
// produces exactly one of.
func (l *ledger) last(t *testing.T) (*audit.Entry, *webhooks.Delivery) {
	t.Helper()

	must.SliceNotEmpty(t, l.entries)
	must.SliceNotEmpty(t, l.deliveries)

	return l.entries[len(l.entries)-1], l.deliveries[len(l.deliveries)-1]
}

func decodeToken(t *testing.T, delivery *webhooks.Delivery) *TokenEvent {
	t.Helper()

	var event TokenEvent
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
		for _, eventType := range []webhooks.EventType{EventTokenIssued, EventTokenRedeemed} {
			test.True(t, catalog.Known(eventType), test.Sprintf("%s is not in the catalog", eventType))
			test.NotEqOp(t, "", catalog[eventType].Description)
		}

		test.MapLen(t, 2, catalog)
	})

	T.Run("hands out a fresh copy each time", func(t *testing.T) {
		t.Parallel()

		delete(EventCatalog(), EventTokenIssued)
		test.True(t, EventCatalog().Known(EventTokenIssued))
	})
}

func TestRecordingHooks(T *testing.T) {
	T.Parallel()

	T.Run("an issuance and a redemption each record one entry and one event naming the token", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store, _ := newTestStore(t, WithHooks(newRecordingHooks(t, l)))
		userID := hookUser()

		issuance, err := issueFor(t, store, testScope(), userID, time.Hour)
		must.NoError(t, err)

		entry, delivery := l.last(t)
		test.EqOp(t, ResourceTypeToken, entry.ResourceType)
		test.EqOp(t, issuance.Token.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.EqOp(t, testScope(), entry.Scope)
		test.EqOp(t, "operator-1", entry.Actor.ID)
		test.MapEmpty(t, entry.Changes)
		test.MapEmpty(t, entry.Metadata)

		test.EqOp(t, EventTokenIssued, delivery.EventType)
		test.EqOp(t, issuance.Token.ID, delivery.OrderingKey)
		issued := decodeToken(t, delivery)
		test.EqOp(t, issuance.Token.ID, issued.TokenID)
		test.EqOp(t, userID, issued.UserID)
		test.True(t, issuance.Token.CreatedAt.Equal(issued.IssuedAt))
		test.True(t, issuance.Token.ExpiresAt.Equal(issued.ExpiresAt))
		test.Nil(t, issued.RedeemedAt)
		// The secret is the caller's, and Service mails it; it reaches neither half.
		test.StrNotContains(t, string(delivery.Payload), issuance.Secret)

		consumed, err := consume(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)

		entry, delivery = l.last(t)
		test.EqOp(t, consumed.ID, entry.ResourceID)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		// No diff: the only field that moved is the redemption time, which the
		// entry's own timestamp already says.
		test.MapEmpty(t, entry.Changes)

		test.EqOp(t, EventTokenRedeemed, delivery.EventType)
		redeemed := decodeToken(t, delivery)
		must.NotNil(t, redeemed.RedeemedAt)
		test.True(t, consumed.RedeemedAt.Equal(*redeemed.RedeemedAt))

		test.SliceLen(t, 2, l.entries)
		test.SliceLen(t, 2, l.deliveries)
	})

	T.Run("a completed reset is one redemption, and its revocation adds nothing", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store, _ := newTestStore(t, WithHooks(newRecordingHooks(t, l)))
		userID := hookUser()

		_, err := issueFor(t, store, testScope(), userID, time.Hour)
		must.NoError(t, err)
		issuance, err := issueFor(t, store, testScope(), userID, time.Hour)
		must.NoError(t, err)

		// What Service.Complete does: spend one link and revoke the rest, on one
		// transaction.
		must.NoError(t, withTx(t, store, func(tx database.Tx) error {
			if _, txErr := store.Consume(t.Context(), tx, testScope(), issuance.Secret); txErr != nil {
				return txErr
			}

			revoked, txErr := store.RevokeForUser(t.Context(), tx, testScope(), userID)
			test.EqOp(t, int64(1), revoked)

			return txErr
		}))

		// Two issuances and one redemption.
		test.SliceLen(t, 3, l.entries)
		test.SliceLen(t, 3, l.deliveries)
		_, delivery := l.last(t)
		test.EqOp(t, EventTokenRedeemed, delivery.EventType)
	})

	T.Run("an erasure records nothing, because dataprivacy records the request once", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store, _ := newTestStore(t, WithHooks(newRecordingHooks(t, l)))
		userID := hookUser()

		_, err := issueFor(t, store, testScope(), userID, time.Hour)
		must.NoError(t, err)

		for _, want := range []int64{1, 0} {
			deleted, deleteErr := deleteForUser(t, store, testScope(), userID)
			must.NoError(t, deleteErr)
			test.EqOp(t, want, deleted)
		}

		// The issuance, and nothing for either erasure.
		test.SliceLen(t, 1, l.entries)
		test.SliceLen(t, 1, l.deliveries)
		_, delivery := l.last(t)
		test.EqOp(t, EventTokenIssued, delivery.EventType)
	})

	T.Run("a recorder filing by subject puts a token's entries on its principal's chain", func(t *testing.T) {
		t.Parallel()

		bySubject := func(_ context.Context, scope tenancy.Scope, entry *recording.Entry) tenancy.Scope {
			if entry.SubjectID != "" {
				return tenancy.Of(entry.SubjectID)
			}

			return scope
		}

		l := &ledger{}
		store, _ := newTestStore(t, WithHooks(newRecordingHooks(t, l, recording.WithScopeResolver(bySubject))))
		userID := hookUser()

		_, err := issueFor(t, store, testScope(), userID, time.Hour)
		must.NoError(t, err)

		must.SliceLen(t, 1, l.entries)
		test.EqOp(t, tenancy.Of(userID), l.entries[0].Scope)
	})

	T.Run("a refused recording fails the write, and the row with it", func(t *testing.T) {
		t.Parallel()

		l := &ledger{refuse: platformerrors.New("the log said no")}
		store, _ := newTestStore(t, WithHooks(newRecordingHooks(t, l)))
		userID := hookUser()

		issuance, err := issueFor(t, store, testScope(), userID, time.Hour)
		test.ErrorIs(t, err, l.refuse)
		test.Nil(t, issuance)
		test.SliceEmpty(t, l.deliveries)

		held, err := store.ListForUser(t.Context(), store.db.Writer(), testScope(), userID)
		must.NoError(t, err)
		test.SliceEmpty(t, held)
	})

	T.Run("refuses a nil token by name", func(t *testing.T) {
		t.Parallel()

		hooks := newRecordingHooks(t, &ledger{})

		test.ErrorIs(t, hooks.AfterIssue(t.Context(), nil, testScope(), nil), ErrNilToken)
		test.ErrorIs(t, hooks.AfterConsume(t.Context(), nil, testScope(), nil), ErrNilToken)
	})
}

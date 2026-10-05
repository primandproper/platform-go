package billing

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

	"github.com/primandproper/primitives-go/v2/capitalism"
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

// decode unmarshals a delivery's payload into the event type T.
func decode[T any](t *testing.T, delivery *webhooks.Delivery) *T {
	t.Helper()

	var event T
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
			EventProductCreated, EventProductUpdated, EventProductArchived,
			EventSubscriptionCreated, EventSubscriptionUpdated, EventSubscriptionArchived,
			EventPurchaseCreated, EventPurchaseCompleted, EventPurchaseArchived,
			EventTransactionRecorded, EventTransactionUpdated, EventTransactionArchived,
		} {
			test.True(t, catalog.Known(eventType), test.Sprintf("%s is not in the catalog", eventType))
			test.NotEqOp(t, "", catalog[eventType].Description)
		}

		test.MapLen(t, 12, catalog)
	})

	T.Run("hands out a fresh copy each time", func(t *testing.T) {
		t.Parallel()

		delete(EventCatalog(), EventProductCreated)
		test.True(t, EventCatalog().Known(EventProductCreated))
	})
}

func TestRecordingHooks(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("every write records one entry and one event, both naming the row", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		product := mustCreateProduct(t, env, store, testScope, recurringProduct("monthly"))
		entry, delivery := l.last(t)
		test.EqOp(t, ResourceTypeProduct, entry.ResourceType)
		test.EqOp(t, product.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.EqOp(t, testScope, entry.Scope)
		test.EqOp(t, "operator-1", entry.Actor.ID)
		test.MapEmpty(t, entry.Changes)
		test.Eq(t, map[string]string{metadataAmountCents: "2500", metadataCurrency: "USD"}, entry.Metadata)
		test.EqOp(t, EventProductCreated, delivery.EventType)
		test.EqOp(t, product.ID, delivery.OrderingKey)
		test.Eq(t, &ProductEvent{
			ProductID:   product.ID,
			Name:        "monthly",
			Kind:        KindRecurring,
			Currency:    "USD",
			AmountCents: product.AmountCents,
		}, decode[ProductEvent](t, delivery))

		repriced := *product
		repriced.AmountCents = product.AmountCents + 100
		_, err := env.updateProduct(t, store, testScope, &repriced)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.MapContainsKey(t, entry.Changes, "amountCents")
		test.MapContainsKey(t, entry.Changes, lastUpdatedAtField)
		test.EqOp(t, EventProductUpdated, delivery.EventType)
		updatedProduct := decode[ProductEvent](t, delivery)
		// The diff names the timestamp the save stamped; the changed list does not.
		test.Eq(t, []string{"amountCents"}, updatedProduct.Changed)
		test.EqOp(t, product.AmountCents+100, updatedProduct.AmountCents)

		subscription := mustCreateSubscription(t, env, store, testScope, currentSubscription(product.ID, testAccount))
		entry, delivery = l.last(t)
		test.EqOp(t, ResourceTypeSubscription, entry.ResourceType)
		test.EqOp(t, subscription.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.Eq(t, map[string]string{metadataProductID: product.ID, metadataStatus: "active"}, entry.Metadata)
		test.EqOp(t, EventSubscriptionCreated, delivery.EventType)
		test.Eq(t, &SubscriptionEvent{
			SubscriptionID: subscription.ID,
			AccountID:      testAccount,
			ProductID:      product.ID,
			Status:         capitalism.SubscriptionStatusActive,
		}, decode[SubscriptionEvent](t, delivery))

		extended := *subscription
		extended.CurrentPeriodEnd = subscription.CurrentPeriodEnd.AddDate(0, 1, 0)
		_, err = env.updateSubscription(t, store, testScope, &extended)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.MapContainsKey(t, entry.Changes, "currentPeriodEnd")
		test.MapNotContainsKey(t, entry.Metadata, metadataPreviousStatus)
		test.EqOp(t, EventSubscriptionUpdated, delivery.EventType)
		synced := decode[SubscriptionEvent](t, delivery)
		test.Eq(t, []string{"currentPeriodEnd"}, synced.Changed)
		test.EqOp(t, capitalism.SubscriptionStatus(""), synced.PreviousStatus)

		must.NoError(t, env.setSubscriptionStatus(t, store, testScope, subscription.ID, capitalism.SubscriptionStatusPastDue))
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.EqOp(t, "past_due", entry.Metadata[metadataStatus])
		test.EqOp(t, "active", entry.Metadata[metadataPreviousStatus])
		test.Eq[any](t, capitalism.SubscriptionStatusActive, entry.Changes["status"].Old)
		test.Eq[any](t, capitalism.SubscriptionStatusPastDue, entry.Changes["status"].New)
		test.EqOp(t, EventSubscriptionUpdated, delivery.EventType)
		moved := decode[SubscriptionEvent](t, delivery)
		test.EqOp(t, capitalism.SubscriptionStatusPastDue, moved.Status)
		test.EqOp(t, capitalism.SubscriptionStatusActive, moved.PreviousStatus)
		test.Eq(t, []string{"status"}, moved.Changed)

		transaction := pendingTransaction(testAccount)
		transaction.SubscriptionID = subscription.ID
		recorded := mustRecordTransaction(t, env, store, testScope, transaction)
		entry, delivery = l.last(t)
		test.EqOp(t, ResourceTypeTransaction, entry.ResourceType)
		test.EqOp(t, recorded.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.Eq(t, map[string]string{
			metadataAmountCents:    "999",
			metadataCurrency:       "USD",
			metadataStatus:         "pending",
			metadataSubscriptionID: subscription.ID,
		}, entry.Metadata)
		test.EqOp(t, EventTransactionRecorded, delivery.EventType)
		test.Eq(t, &TransactionEvent{
			TransactionID:  recorded.ID,
			AccountID:      testAccount,
			SubscriptionID: subscription.ID,
			Status:         TransactionPending,
			Currency:       "USD",
			AmountCents:    999,
		}, decode[TransactionEvent](t, delivery))

		must.NoError(t, env.setTransactionStatus(t, store, testScope, recorded.ID, TransactionSucceeded))
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.EqOp(t, TransactionSucceeded.String(), entry.Metadata[metadataStatus])
		test.EqOp(t, TransactionPending.String(), entry.Metadata[metadataPreviousStatus])
		test.MapContainsKey(t, entry.Changes, "status")
		test.EqOp(t, EventTransactionUpdated, delivery.EventType)
		settledTx := decode[TransactionEvent](t, delivery)
		test.EqOp(t, TransactionSucceeded, settledTx.Status)
		test.EqOp(t, TransactionPending, settledTx.PreviousStatus)

		_, err = env.archiveTransaction(t, store, testScope, recorded.ID)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, EventTransactionArchived, delivery.EventType)

		_, err = env.archiveSubscription(t, store, testScope, subscription.ID)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, ResourceTypeSubscription, entry.ResourceType)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, EventSubscriptionArchived, delivery.EventType)

		oneTime := mustCreateProduct(t, env, store, testScope, oneTimeProduct("lifetime"))
		purchase := mustCreatePurchase(t, env, store, testScope, outstandingPurchase(oneTime.ID, testAccount))
		entry, delivery = l.last(t)
		test.EqOp(t, ResourceTypePurchase, entry.ResourceType)
		test.EqOp(t, purchase.ID, entry.ResourceID)
		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.Eq(t, map[string]string{
			metadataAmountCents: "999",
			metadataCurrency:    "USD",
			metadataProductID:   oneTime.ID,
		}, entry.Metadata)
		test.EqOp(t, EventPurchaseCreated, delivery.EventType)
		test.Eq(t, &PurchaseEvent{
			PurchaseID:  purchase.ID,
			AccountID:   testAccount,
			ProductID:   oneTime.ID,
			Currency:    "USD",
			AmountCents: 999,
		}, decode[PurchaseEvent](t, delivery))

		_, err = env.completePurchase(t, store, testScope, purchase.ID, testNow)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		// The row before a completion is implied, so there is nothing to diff.
		test.MapEmpty(t, entry.Changes)
		test.EqOp(t, EventPurchaseCompleted, delivery.EventType)

		_, err = env.archivePurchase(t, store, testScope, purchase.ID)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, EventPurchaseArchived, delivery.EventType)

		_, err = env.archiveProduct(t, store, testScope, product.ID)
		must.NoError(t, err)
		entry, delivery = l.last(t)
		test.EqOp(t, ResourceTypeProduct, entry.ResourceType)
		test.EqOp(t, audit.EventArchived, entry.EventType)
		test.EqOp(t, EventProductArchived, delivery.EventType)

		// Thirteen writes, one product created twice.
		test.SliceLen(t, 14, l.entries)
		test.SliceLen(t, 14, l.deliveries)
	})

	T.Run("no account identifier reaches an entry's metadata, and no provider identifier an event", func(t *testing.T) {
		t.Parallel()

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		provided := recurringProduct("monthly")
		provided.ExternalProductID = "prod_provider"
		product := mustCreateProduct(t, env, store, testScope, provided)

		agreement := currentSubscription(product.ID, testAccount)
		agreement.ExternalSubscriptionID = "sub_provider"
		subscription := mustCreateSubscription(t, env, store, testScope, agreement)
		must.NoError(t, env.setSubscriptionStatus(t, store, testScope, subscription.ID, capitalism.SubscriptionStatusCanceled))

		attempt := pendingTransaction(testAccount)
		attempt.ExternalTransactionID = "pi_provider"
		recorded := mustRecordTransaction(t, env, store, testScope, attempt)
		must.NoError(t, env.setTransactionStatus(t, store, testScope, recorded.ID, TransactionFailed))

		for _, entry := range l.entries {
			test.MapNotContainsValue(t, entry.Metadata, testAccount)
		}

		for _, delivery := range l.deliveries {
			for _, reference := range []string{"prod_provider", "sub_provider", "pi_provider"} {
				test.StrNotContains(t, string(delivery.Payload), reference)
			}
		}
	})

	T.Run("a recorder filing by subject puts an account's rows on its chain and the catalog where the write ran", func(t *testing.T) {
		t.Parallel()

		bySubject := func(_ context.Context, scope tenancy.Scope, entry *recording.Entry) tenancy.Scope {
			if entry.SubjectID != "" {
				return tenancy.Of(entry.SubjectID)
			}

			return scope
		}

		l := &ledger{}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l, recording.WithScopeResolver(bySubject))))

		product := mustCreateProduct(t, env, store, testScope, oneTimeProduct("lifetime"))
		mustCreateSubscription(t, env, store, testScope, currentSubscription(product.ID, testAccount))
		mustCreatePurchase(t, env, store, testScope, outstandingPurchase(product.ID, testAccount))
		mustRecordTransaction(t, env, store, testScope, pendingTransaction(otherAccount))

		must.SliceLen(t, 4, l.entries)
		test.EqOp(t, testScope, l.entries[0].Scope)
		test.EqOp(t, tenancy.Of(testAccount), l.entries[1].Scope)
		test.EqOp(t, tenancy.Of(testAccount), l.entries[2].Scope)
		test.EqOp(t, tenancy.Of(otherAccount), l.entries[3].Scope)
	})

	T.Run("a refused recording fails the write, and the row with it", func(t *testing.T) {
		t.Parallel()

		l := &ledger{refuse: platformerrors.New("the log said no")}
		store := env.newStore(t, WithHooks(newRecordingHooks(t, l)))

		created, err := env.createProduct(t, store, testScope, recurringProduct("monthly"))
		test.ErrorIs(t, err, l.refuse)
		test.Nil(t, created)
		test.SliceEmpty(t, l.deliveries)
	})
}

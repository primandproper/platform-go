package http

import (
	"context"
	"database/sql"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/billing"
	billingmock "github.com/primandproper/platform-go/v14/billing/mock"
	billingsync "github.com/primandproper/platform-go/v14/billing/sync"

	"github.com/primandproper/primitives-go/v2/capitalism"
	capitalismmock "github.com/primandproper/primitives-go/v2/capitalism/mock"
	"github.com/primandproper/primitives-go/v2/database"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var testScope = tenancy.Of("acme")

const (
	testAccount    = "account-1"
	testProduct    = "product-1"
	testExternalID = "sub_KLWtQ1"
)

var (
	periodStart = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	periodEnd   = time.Date(2027, 9, 1, 12, 0, 0, 0, time.UTC)
)

// stubExecutor stands in for the executor a Tx runs on. The store beneath the
// syncer is a mock, so nothing executes through it.
type stubExecutor struct{}

var _ database.SQLQueryExecutor = (*stubExecutor)(nil)

func (*stubExecutor) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("the syncer's store is a mock; nothing runs on this")
}

func (*stubExecutor) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	panic("the syncer's store is a mock; nothing runs on this")
}

func (*stubExecutor) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("the syncer's store is a mock; nothing runs on this")
}

func (*stubExecutor) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("the syncer's store is a mock; nothing runs on this")
}

// transactingClient runs every WithTransaction callback on a stub Tx and
// reports whatever the callback returned, which is what a commit or a rollback
// looks like from the caller's side.
func transactingClient() *databasemock.ClientMock {
	return &databasemock.ClientMock{
		WithTransactionFunc: func(_ context.Context, fn func(database.Tx) error) error {
			return fn(database.NewTxForTesting(&stubExecutor{}))
		},
	}
}

// delivering is a manager whose every verification succeeds with this event.
func delivering(event *capitalism.Event) *capitalismmock.PaymentManagerMock {
	return &capitalismmock.PaymentManagerMock{
		HandleEventWebhookFunc: func(*nethttp.Request) (*capitalism.Event, error) { return event, nil },
	}
}

func subscriptionEvent(status capitalism.SubscriptionStatus) *capitalism.Event {
	return &capitalism.Event{
		ID:   "evt_1",
		Type: "customer.subscription.updated",
		Subscription: &capitalism.SubscriptionState{
			ID:                 testExternalID,
			CustomerID:         "cus_1",
			Status:             status,
			ProviderStatus:     string(status),
			CurrentPeriodStart: &periodStart,
			CurrentPeriodEnd:   &periodEnd,
		},
	}
}

func stored(status capitalism.SubscriptionStatus) *billing.Subscription {
	return &billing.Subscription{
		ID:                     "subscription-1",
		BelongsToAccount:       testAccount,
		ProductID:              testProduct,
		ExternalSubscriptionID: testExternalID,
		Status:                 status,
		CurrentPeriodStart:     periodStart,
		CurrentPeriodEnd:       periodEnd,
		Scope:                  testScope,
	}
}

// absent is a store holding no agreement, which opens one on a create.
func absent() *billingmock.SubscriptionStoreMock {
	return &billingmock.SubscriptionStoreMock{
		GetSubscriptionByExternalIDFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*billing.Subscription, error) {
			return nil, billing.ErrSubscriptionNotFound
		},
		CreateSubscriptionFunc: func(
			_ context.Context, _ database.Tx, _ tenancy.Scope, input *billing.Subscription,
		) (*billing.Subscription, error) {
			created := *input
			created.ID = "subscription-1"

			return &created, nil
		},
	}
}

// holding is a store holding one agreement in a status.
func holding(status capitalism.SubscriptionStatus) *billingmock.SubscriptionStoreMock {
	return &billingmock.SubscriptionStoreMock{
		GetSubscriptionByExternalIDFunc: func(
			context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
		) (*billing.Subscription, error) {
			return stored(status), nil
		},
	}
}

func fixedPlace(
	context.Context, database.SQLQueryExecutor, tenancy.Scope, *capitalism.SubscriptionState,
) (*billingsync.Placement, error) {
	return &billingsync.Placement{AccountID: testAccount, ProductID: testProduct}, nil
}

func fixedScope(context.Context, *capitalism.Event) (tenancy.Scope, error) { return testScope, nil }

func newSyncer(t *testing.T, store billing.SubscriptionStore) *billingsync.Syncer {
	t.Helper()

	syncer, err := billingsync.New(store, fixedPlace)
	must.NoError(t, err)

	return syncer
}

func newHandler(
	t *testing.T,
	manager capitalism.PaymentManager,
	store billing.SubscriptionStore,
	client database.Client,
	opts ...Option,
) *WebhookHandler {
	t.Helper()

	handler, err := NewWebhookHandler(manager, newSyncer(t, store), client,
		append([]Option{WithScopeResolver(fixedScope)}, opts...)...)
	must.NoError(t, err)

	return handler
}

func deliver(t *testing.T, handler nethttp.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), nethttp.MethodPost, target, strings.NewReader(`{"id":"evt_1"}`)))

	return res
}

func TestNewWebhookHandler(T *testing.T) {
	T.Parallel()

	manager := delivering(nil)
	client := transactingClient()

	T.Run("refuses a nil manager", func(t *testing.T) {
		t.Parallel()

		_, err := NewWebhookHandler(nil, newSyncer(t, absent()), client, WithScopeResolver(fixedScope))
		test.ErrorIs(t, err, ErrNilPaymentManager)
	})

	T.Run("refuses a nil syncer", func(t *testing.T) {
		t.Parallel()

		_, err := NewWebhookHandler(manager, nil, client, WithScopeResolver(fixedScope))
		test.ErrorIs(t, err, ErrNilSyncer)
	})

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewWebhookHandler(manager, newSyncer(t, absent()), nil, WithScopeResolver(fixedScope))
		test.ErrorIs(t, err, ErrNilClient)
	})

	T.Run("refuses a handler with no scope resolver", func(t *testing.T) {
		t.Parallel()

		_, err := NewWebhookHandler(manager, newSyncer(t, absent()), client)
		test.ErrorIs(t, err, ErrNilScopeResolver)
	})

	T.Run("tolerates nil options and nil pillars", func(t *testing.T) {
		t.Parallel()

		_, err := NewWebhookHandler(manager, newSyncer(t, absent()), client,
			nil, WithPillars(nil), WithPillars(&observability.Pillars{}),
			WithLogger(nil), WithTracerProvider(nil), WithScopeResolver(GlobalScope))
		test.NoError(t, err)
	})
}

func TestWebhookHandler_ServeHTTP(T *testing.T) {
	T.Parallel()

	T.Run("answers 400 to a delivery that fails verification", func(t *testing.T) {
		t.Parallel()

		manager := &capitalismmock.PaymentManagerMock{
			HandleEventWebhookFunc: func(*nethttp.Request) (*capitalism.Event, error) {
				return nil, platformerrors.New("signature mismatch")
			},
		}
		client := transactingClient()

		res := deliver(t, newHandler(t, manager, absent(), client), "/billing/webhook")

		test.EqOp(t, nethttp.StatusBadRequest, res.Code)
		test.SliceLen(t, 0, client.WithTransactionCalls())
	})

	T.Run("answers 200 to a delivery with nothing to act on", func(t *testing.T) {
		t.Parallel()

		client := transactingClient()

		res := deliver(t, newHandler(t, delivering(nil), absent(), client), "/billing/webhook")

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.SliceLen(t, 0, client.WithTransactionCalls())
	})

	T.Run("answers 200 to an event that is not about a subscription, without resolving a scope", func(t *testing.T) {
		t.Parallel()

		client := transactingClient()
		event := &capitalism.Event{ID: "evt_2", Type: "payment_intent.succeeded"}

		handler, err := NewWebhookHandler(delivering(event), newSyncer(t, absent()), client,
			WithScopeResolver(func(context.Context, *capitalism.Event) (tenancy.Scope, error) {
				t.Error("a delivery with nothing to reconcile consulted the scope resolver")

				return tenancy.Scope{}, platformerrors.New("unreachable")
			}))
		must.NoError(t, err)

		res := deliver(t, handler, "/billing/webhook")

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.SliceLen(t, 0, client.WithTransactionCalls())
	})

	T.Run("answers 200 to a reconciled delivery, on a transaction", func(t *testing.T) {
		t.Parallel()

		store := absent()
		client := transactingClient()

		res := deliver(t, newHandler(t, delivering(subscriptionEvent(capitalism.SubscriptionStatusActive)), store, client), "/billing/webhook")

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.SliceLen(t, 1, client.WithTransactionCalls())
		must.SliceLen(t, 1, store.CreateSubscriptionCalls())
		test.EqOp(t, testScope, store.CreateSubscriptionCalls()[0].Scope)
	})

	T.Run("answers 200 to a redelivery", func(t *testing.T) {
		t.Parallel()

		store := holding(capitalism.SubscriptionStatusActive)
		var outcome billingsync.Outcome

		handler := newHandler(t, delivering(subscriptionEvent(capitalism.SubscriptionStatusActive)), store, transactingClient(),
			WithAfterApply(func(_ context.Context, _ database.Tx, _ tenancy.Scope, _ *capitalism.Event, result *billingsync.Result) error {
				outcome = result.Outcome

				return nil
			}))

		res := deliver(t, handler, "/billing/webhook")

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.EqOp(t, billingsync.OutcomeUnchanged, outcome)
	})

	T.Run("answers 500 when the scope resolver fails", func(t *testing.T) {
		t.Parallel()

		client := transactingClient()

		handler, err := NewWebhookHandler(
			delivering(subscriptionEvent(capitalism.SubscriptionStatusActive)), newSyncer(t, absent()), client,
			WithScopeResolver(func(context.Context, *capitalism.Event) (tenancy.Scope, error) {
				return tenancy.Scope{}, platformerrors.New("customer lookup timed out")
			}))
		must.NoError(t, err)

		res := deliver(t, handler, "/billing/webhook")

		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
		test.SliceLen(t, 0, client.WithTransactionCalls())
	})

	T.Run("answers 500 when the transaction fails", func(t *testing.T) {
		t.Parallel()

		client := &databasemock.ClientMock{
			WithTransactionFunc: func(context.Context, func(database.Tx) error) error {
				return platformerrors.New("connection reset")
			},
		}

		res := deliver(t, newHandler(t, delivering(subscriptionEvent(capitalism.SubscriptionStatusActive)), absent(), client), "/billing/webhook")

		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
	})

	T.Run("answers 500 when the store fails", func(t *testing.T) {
		t.Parallel()

		store := &billingmock.SubscriptionStoreMock{
			GetSubscriptionByExternalIDFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
			) (*billing.Subscription, error) {
				return nil, platformerrors.New("deadlock detected")
			},
		}

		res := deliver(t, newHandler(t, delivering(subscriptionEvent(capitalism.SubscriptionStatusActive)), store, transactingClient()), "/billing/webhook")

		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
	})

	T.Run("answers 500 when the sync refuses a delivery", func(t *testing.T) {
		t.Parallel()

		event := subscriptionEvent(capitalism.SubscriptionStatusActive)
		event.Subscription.CurrentPeriodEnd = nil

		res := deliver(t, newHandler(t, delivering(event), absent(), transactingClient()), "/billing/webhook")

		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
	})

	T.Run("answers 500 and rolls back when AfterApply refuses", func(t *testing.T) {
		t.Parallel()

		var committed error

		client := &databasemock.ClientMock{
			WithTransactionFunc: func(_ context.Context, fn func(database.Tx) error) error {
				committed = fn(database.NewTxForTesting(&stubExecutor{}))

				return committed
			},
		}

		handler := newHandler(t, delivering(subscriptionEvent(capitalism.SubscriptionStatusActive)), absent(), client,
			WithAfterApply(func(context.Context, database.Tx, tenancy.Scope, *capitalism.Event, *billingsync.Result) error {
				return platformerrors.New("audit refused")
			}))

		res := deliver(t, handler, "/billing/webhook")

		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
		test.Error(t, committed)
	})

	T.Run("hands AfterApply the delivery's transaction, scope and result", func(t *testing.T) {
		t.Parallel()

		event := subscriptionEvent(capitalism.SubscriptionStatusActive)

		var (
			sawTx     database.Tx
			sawScope  tenancy.Scope
			sawEvent  *capitalism.Event
			sawResult *billingsync.Result
		)

		store := absent()

		handler := newHandler(t, delivering(event), store, transactingClient(),
			WithAfterApply(func(_ context.Context, tx database.Tx, scope tenancy.Scope, ev *capitalism.Event, result *billingsync.Result) error {
				sawTx, sawScope, sawEvent, sawResult = tx, scope, ev, result

				return nil
			}))

		res := deliver(t, handler, "/billing/webhook")

		test.EqOp(t, nethttp.StatusOK, res.Code)
		must.SliceLen(t, 1, store.CreateSubscriptionCalls())
		test.EqOp(t, store.CreateSubscriptionCalls()[0].Tx, sawTx)
		test.EqOp(t, testScope, sawScope)
		test.EqOp(t, event, sawEvent)
		must.NotNil(t, sawResult)
		test.EqOp(t, billingsync.OutcomeCreated, sawResult.Outcome)
	})

	T.Run("takes the scope from the resolver and nothing from the query string", func(t *testing.T) {
		t.Parallel()

		store := absent()

		var resolvedFrom *capitalism.Event

		handler, err := NewWebhookHandler(
			delivering(subscriptionEvent(capitalism.SubscriptionStatusActive)), newSyncer(t, store), transactingClient(),
			WithScopeResolver(func(_ context.Context, event *capitalism.Event) (tenancy.Scope, error) {
				resolvedFrom = event

				return testScope, nil
			}))
		must.NoError(t, err)

		res := deliver(t, handler, "/billing/webhook?account_id=account-intruder&scope=intruder")

		test.EqOp(t, nethttp.StatusOK, res.Code)
		must.NotNil(t, resolvedFrom)
		must.SliceLen(t, 1, store.CreateSubscriptionCalls())

		call := store.CreateSubscriptionCalls()[0]
		test.EqOp(t, testScope, call.Scope)
		test.EqOp(t, testAccount, call.Subscription.BelongsToAccount)
	})
}

func TestGlobalScope(T *testing.T) {
	T.Parallel()

	T.Run("answers the global scope", func(t *testing.T) {
		t.Parallel()

		scope, err := GlobalScope(t.Context(), &capitalism.Event{})
		must.NoError(t, err)
		test.EqOp(t, tenancy.Global(), scope)
	})
}

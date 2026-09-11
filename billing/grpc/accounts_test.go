package grpc_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/billingpb"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The reads that are somebody's own: what an account has agreed to, bought and
// been charged. The authorization on them is authorizer_test.go's; this file is
// about what they answer with once the caller is permitted.

func TestListSubscriptionsForAccount(T *testing.T) {
	T.Parallel()

	T.Run("pages one account's agreements and not the scope's", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		mine := h.seedSubscription(t, testScope, testAccount)
		h.seedSubscription(t, testScope, otherAccount)

		res, err := h.server.ListSubscriptionsForAccount(h.ctx(t, testUser, testAccount),
			&billingpb.ListSubscriptionsForAccountRequest{AccountId: testAccount})
		must.NoError(t, err)
		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, mine.ID, res.GetResults()[0].GetId())
	})

	T.Run("carries the status as capitalism spells it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seedSubscription(t, testScope, testAccount)

		res, err := h.server.ListSubscriptionsForAccount(h.ctx(t, testUser, testAccount),
			&billingpb.ListSubscriptionsForAccountRequest{AccountId: testAccount})
		must.NoError(t, err)
		must.SliceLen(t, 1, res.GetResults())

		// The wire value is the documented string and not a generated constant,
		// which is the ruling billing.proto's opening comment records.
		test.EqOp(t, capitalism.SubscriptionStatusActive.String(), res.GetResults()[0].GetStatus())
	})

	T.Run("refuses a request that named no account", func(t *testing.T) {
		t.Parallel()

		// Malformed rather than refused, and answered before the authorizer is
		// asked: an empty account is not an account somebody may or may not
		// reach. The authorizer supplied here permits every account, so a
		// handler that had left the check to the seam would have gone on to ask
		// the store for a page of everybody's.
		h := newHarnessWithAuthorizer(t, brokenAuthorizer(nil))

		_, err := h.server.ListSubscriptionsForAccount(h.ctx(t, testUser, testAccount),
			&billingpb.ListSubscriptionsForAccountRequest{})
		must.Error(t, err)
		test.ErrorIs(t, err, billing.ErrEmptyAccount)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestListCurrentSubscriptions(T *testing.T) {
	T.Parallel()

	T.Run("pages the agreements whose paid period covers now", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		current := h.seedSubscription(t, testScope, testAccount)
		h.seedLapsedSubscription(t, testScope, testAccount)

		res, err := h.server.ListCurrentSubscriptions(h.ctx(t, testUser, testAccount),
			&billingpb.ListCurrentSubscriptionsRequest{AccountId: testAccount})
		must.NoError(t, err)
		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, current.ID, res.GetResults()[0].GetId())
	})

	T.Run("does not filter on the status, and says nothing about entitlement", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		pastDue := h.seedSubscriptionWithStatus(t, testScope, testAccount, capitalism.SubscriptionStatusPastDue)

		// A past_due subscription inside its paid period is current by this
		// reading and may well not be entitled. Which statuses leave an account
		// entitled is the consumer's ruling, and this is the read that hands
		// them the fact rather than the answer — which is why there is no
		// standing field on the message to check for.
		res, err := h.server.ListCurrentSubscriptions(h.ctx(t, testUser, testAccount),
			&billingpb.ListCurrentSubscriptionsRequest{AccountId: testAccount})
		must.NoError(t, err)
		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, pastDue.ID, res.GetResults()[0].GetId())
		test.EqOp(t, capitalism.SubscriptionStatusPastDue.String(), res.GetResults()[0].GetStatus())
	})
}

func TestListPurchasesForAccount(T *testing.T) {
	T.Parallel()

	h := newHarness(T)
	mine := h.seedPurchase(T, testScope, testAccount)
	h.seedPurchase(T, testScope, otherAccount)

	res, err := h.server.ListPurchasesForAccount(h.ctx(T, testUser, testAccount),
		&billingpb.ListPurchasesForAccountRequest{AccountId: testAccount})
	must.NoError(T, err)
	must.SliceLen(T, 1, res.GetResults())
	test.EqOp(T, mine.ID, res.GetResults()[0].GetId())

	// An outstanding sale carries no completion time, which is the whole
	// lifecycle this message has: unset means the money has not arrived, and the
	// zero timestamp would have read as 1970.
	test.Nil(T, res.GetResults()[0].GetCompletedAt())
}

func TestListTransactionsForAccount(T *testing.T) {
	T.Parallel()

	h := newHarness(T)
	mine := h.seedTransaction(T, testScope, testAccount)
	h.seedTransaction(T, testScope, otherAccount)

	res, err := h.server.ListTransactionsForAccount(h.ctx(T, testUser, testAccount),
		&billingpb.ListTransactionsForAccountRequest{AccountId: testAccount})
	must.NoError(T, err)
	must.SliceLen(T, 1, res.GetResults())
	test.EqOp(T, mine.ID, res.GetResults()[0].GetId())
	test.EqOp(T, billingpb.TransactionStatus_TRANSACTION_STATUS_SUCCEEDED, res.GetResults()[0].GetStatus())
}

func TestTheArchiveSet(T *testing.T) {
	T.Parallel()

	// The three Archive RPCs on the account-owned tables are administrative and
	// ask no authorizer, which is the deliberate consequence of what archiving
	// is: a cancellation is a fact the provider reports and arrives as a status,
	// and a refund is a transaction of its own. Neither is something an account
	// does to its own row here.
	T.Run("a subscription", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithAuthorizer(t, ownAccountAuthorizer())
		subscription := h.seedSubscription(t, testScope, otherAccount)

		_, err := h.server.ArchiveSubscription(h.ctx(t, testUser, testAccount),
			&billingpb.ArchiveSubscriptionRequest{SubscriptionId: subscription.ID})
		must.NoError(t, err)

		_, readErr := h.store.GetSubscription(t.Context(), h.db.Reader(), testScope, subscription.ID)
		test.ErrorIs(t, readErr, billing.ErrSubscriptionNotFound)
	})

	T.Run("a purchase", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		purchase := h.seedPurchase(t, testScope, testAccount)

		_, err := h.server.ArchivePurchase(h.ctx(t, testUser, testAccount),
			&billingpb.ArchivePurchaseRequest{PurchaseId: purchase.ID})
		must.NoError(t, err)

		_, readErr := h.store.GetPurchase(t.Context(), h.db.Reader(), testScope, purchase.ID)
		test.ErrorIs(t, readErr, billing.ErrPurchaseNotFound)
	})

	T.Run("a ledger row", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		transaction := h.seedTransaction(t, testScope, testAccount)

		_, err := h.server.ArchiveTransaction(h.ctx(t, testUser, testAccount),
			&billingpb.ArchiveTransactionRequest{TransactionId: transaction.ID})
		must.NoError(t, err)

		_, readErr := h.store.GetTransaction(t.Context(), h.db.Reader(), testScope, transaction.ID)
		test.ErrorIs(t, readErr, billing.ErrTransactionNotFound)
	})

	T.Run("and not another tenant's", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		transaction := h.seedTransaction(t, testScope, testAccount)

		_, err := h.server.ArchiveTransaction(h.otherScopeCtx(t),
			&billingpb.ArchiveTransactionRequest{TransactionId: transaction.ID})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})
}

// seedLapsedSubscription writes an agreement whose paid period is over, for the
// read that pages the current ones.
func (h *harness) seedLapsedSubscription(tb testing.TB, scope tenancy.Scope, accountID string) *billing.Subscription {
	tb.Helper()

	var (
		product      = h.seedRecurringProduct(tb, scope)
		subscription *billing.Subscription
		now          = time.Now().UTC()
	)

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var err error
		subscription, err = h.store.CreateSubscription(tb.Context(), tx, scope, &billing.Subscription{
			BelongsToAccount:   accountID,
			ProductID:          product.ID,
			Status:             capitalism.SubscriptionStatusCanceled,
			CurrentPeriodStart: now.Add(-72 * time.Hour),
			CurrentPeriodEnd:   now.Add(-48 * time.Hour),
		})

		return err
	}))
	must.NotNil(tb, subscription)

	return subscription
}

// seedSubscriptionWithStatus writes a current agreement in the named standing,
// for the test that this surface reports it and reads nothing into it.
func (h *harness) seedSubscriptionWithStatus(
	tb testing.TB,
	scope tenancy.Scope,
	accountID string,
	standing capitalism.SubscriptionStatus,
) *billing.Subscription {
	tb.Helper()

	var (
		product      = h.seedRecurringProduct(tb, scope)
		subscription *billing.Subscription
		now          = time.Now().UTC()
	)

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var err error
		subscription, err = h.store.CreateSubscription(tb.Context(), tx, scope, &billing.Subscription{
			BelongsToAccount:   accountID,
			ProductID:          product.ID,
			Status:             standing,
			CurrentPeriodStart: now.Add(-24 * time.Hour),
			CurrentPeriodEnd:   now.Add(24 * time.Hour),
		})

		return err
	}))
	must.NotNil(tb, subscription)

	return subscription
}

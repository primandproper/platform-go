package billing

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/internal/txcount"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errHook is a hook refusing the write it was called from.
var errHook = platformerrors.New("the hook failed")

// hookCall is one hook invocation, as the recording hooks saw it. row is the row
// the hook was handed, and before is the prior row an update is handed beside
// it — nil for every other write.
type hookCall struct {
	before any
	row    any
	name   string
	scope  tenancy.Scope
}

// recordingHooks remembers every call, and fails the one named by failOn.
//
// It implements Hooks outright rather than embedding NoopHooks, so that a method
// added to Hooks later fails this file's build until it is recorded here too,
// rather than arriving as a no-op the suite never asserts on.
type recordingHooks struct {
	failOn string
	calls  []hookCall
}

var _ Hooks = (*recordingHooks)(nil)

func (h *recordingHooks) record(call *hookCall) error {
	h.calls = append(h.calls, *call)

	if call.name == h.failOn {
		return errHook
	}

	return nil
}

func (h *recordingHooks) AfterCreateProduct(_ context.Context, _ database.Tx, scope tenancy.Scope, product *Product) error {
	return h.record(&hookCall{name: "AfterCreateProduct", scope: scope, row: product})
}

func (h *recordingHooks) AfterUpdateProduct(_ context.Context, _ database.Tx, scope tenancy.Scope, before, after *Product) error {
	return h.record(&hookCall{name: "AfterUpdateProduct", scope: scope, row: after, before: before})
}

func (h *recordingHooks) AfterArchiveProduct(_ context.Context, _ database.Tx, scope tenancy.Scope, product *Product) error {
	return h.record(&hookCall{name: "AfterArchiveProduct", scope: scope, row: product})
}

func (h *recordingHooks) AfterCreateSubscription(_ context.Context, _ database.Tx, scope tenancy.Scope, subscription *Subscription) error {
	return h.record(&hookCall{name: "AfterCreateSubscription", scope: scope, row: subscription})
}

func (h *recordingHooks) AfterUpdateSubscription(_ context.Context, _ database.Tx, scope tenancy.Scope, before, after *Subscription) error {
	return h.record(&hookCall{name: "AfterUpdateSubscription", scope: scope, row: after, before: before})
}

func (h *recordingHooks) AfterSetSubscriptionStatus(_ context.Context, _ database.Tx, scope tenancy.Scope, before, after *Subscription) error {
	return h.record(&hookCall{name: "AfterSetSubscriptionStatus", scope: scope, row: after, before: before})
}

func (h *recordingHooks) AfterArchiveSubscription(_ context.Context, _ database.Tx, scope tenancy.Scope, subscription *Subscription) error {
	return h.record(&hookCall{name: "AfterArchiveSubscription", scope: scope, row: subscription})
}

func (h *recordingHooks) AfterCreatePurchase(_ context.Context, _ database.Tx, scope tenancy.Scope, purchase *Purchase) error {
	return h.record(&hookCall{name: "AfterCreatePurchase", scope: scope, row: purchase})
}

func (h *recordingHooks) AfterCompletePurchase(_ context.Context, _ database.Tx, scope tenancy.Scope, purchase *Purchase) error {
	return h.record(&hookCall{name: "AfterCompletePurchase", scope: scope, row: purchase})
}

func (h *recordingHooks) AfterArchivePurchase(_ context.Context, _ database.Tx, scope tenancy.Scope, purchase *Purchase) error {
	return h.record(&hookCall{name: "AfterArchivePurchase", scope: scope, row: purchase})
}

func (h *recordingHooks) AfterRecordTransaction(_ context.Context, _ database.Tx, scope tenancy.Scope, transaction *Transaction) error {
	return h.record(&hookCall{name: "AfterRecordTransaction", scope: scope, row: transaction})
}

func (h *recordingHooks) AfterSetTransactionStatus(_ context.Context, _ database.Tx, scope tenancy.Scope, before, after *Transaction) error {
	return h.record(&hookCall{name: "AfterSetTransactionStatus", scope: scope, row: after, before: before})
}

func (h *recordingHooks) AfterArchiveTransaction(_ context.Context, _ database.Tx, scope tenancy.Scope, transaction *Transaction) error {
	return h.record(&hookCall{name: "AfterArchiveTransaction", scope: scope, row: transaction})
}

// last is the most recent call, failing the test when there was none.
func (h *recordingHooks) last(tb testing.TB) hookCall {
	tb.Helper()

	must.SliceNotEmpty(tb, h.calls)

	return h.calls[len(h.calls)-1]
}

// lastAs is the most recent call, its name checked and its rows asserted to T.
// before is nil for a write that is handed one row.
func lastAs[T any](tb testing.TB, h *recordingHooks, name string) (before, row *T) {
	tb.Helper()

	call := h.last(tb)
	must.EqOp(tb, name, call.name)
	test.EqOp(tb, testScope, call.scope)

	row, ok := call.row.(*T)
	must.True(tb, ok)

	if call.before != nil {
		before, ok = call.before.(*T)
		must.True(tb, ok)
	}

	return before, row
}

// runHooksSuite is every assertion about the hooks a write runs: that each write
// calls its own, with the row it answers with, that an update is handed the row
// from before it, and that a hook's refusal is the write's.
func runHooksSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("every write calls its hook with the row it answers with", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newHookedStore(t, hooks)

		product := mustCreateProduct(t, env, store, testScope, recurringProduct("monthly"))
		before, row := lastAs[Product](t, hooks, "AfterCreateProduct")
		test.Nil(t, before)
		test.EqOp(t, product, row)
		test.False(t, row.CreatedAt.IsZero())

		repriced := *product
		repriced.AmountCents = product.AmountCents + 100
		updatedProduct, err := env.updateProduct(t, store, testScope, &repriced)
		must.NoError(t, err)
		before, row = lastAs[Product](t, hooks, "AfterUpdateProduct")
		test.EqOp(t, updatedProduct, row)
		test.EqOp(t, product.AmountCents+100, row.AmountCents)
		must.NotNil(t, before)
		test.EqOp(t, product.ID, before.ID)
		test.EqOp(t, product.AmountCents, before.AmountCents)

		subscription := mustCreateSubscription(t, env, store, testScope, currentSubscription(product.ID, testAccount))
		beforeSub, sub := lastAs[Subscription](t, hooks, "AfterCreateSubscription")
		test.Nil(t, beforeSub)
		test.EqOp(t, subscription, sub)

		extended := *subscription
		extended.CurrentPeriodEnd = subscription.CurrentPeriodEnd.AddDate(0, 1, 0)
		updatedSub, err := env.updateSubscription(t, store, testScope, &extended)
		must.NoError(t, err)
		beforeSub, sub = lastAs[Subscription](t, hooks, "AfterUpdateSubscription")
		test.EqOp(t, updatedSub, sub)
		test.True(t, sub.CurrentPeriodEnd.Equal(extended.CurrentPeriodEnd))
		must.NotNil(t, beforeSub)
		test.True(t, beforeSub.CurrentPeriodEnd.Equal(subscription.CurrentPeriodEnd))

		must.NoError(t, env.setSubscriptionStatus(t, store, testScope, subscription.ID, capitalism.SubscriptionStatusPastDue))
		beforeSub, sub = lastAs[Subscription](t, hooks, "AfterSetSubscriptionStatus")
		test.EqOp(t, subscription.ID, sub.ID)
		test.EqOp(t, capitalism.SubscriptionStatusPastDue, sub.Status)
		must.NotNil(t, beforeSub)
		test.EqOp(t, capitalism.SubscriptionStatusActive, beforeSub.Status)
		test.EqOp(t, testAccount, beforeSub.BelongsToAccount)

		transaction := pendingTransaction(testAccount)
		transaction.SubscriptionID = subscription.ID
		recorded := mustRecordTransaction(t, env, store, testScope, transaction)
		beforeTx, txRow := lastAs[Transaction](t, hooks, "AfterRecordTransaction")
		test.Nil(t, beforeTx)
		test.EqOp(t, recorded, txRow)

		must.NoError(t, env.setTransactionStatus(t, store, testScope, recorded.ID, TransactionSucceeded))
		beforeTx, txRow = lastAs[Transaction](t, hooks, "AfterSetTransactionStatus")
		test.EqOp(t, TransactionSucceeded, txRow.Status)
		must.NotNil(t, beforeTx)
		test.EqOp(t, TransactionPending, beforeTx.Status)

		retiredTx, err := env.archiveTransaction(t, store, testScope, recorded.ID)
		must.NoError(t, err)
		_, txRow = lastAs[Transaction](t, hooks, "AfterArchiveTransaction")
		test.EqOp(t, retiredTx, txRow)
		test.NotNil(t, txRow.ArchivedAt)

		retiredSub, err := env.archiveSubscription(t, store, testScope, subscription.ID)
		must.NoError(t, err)
		_, sub = lastAs[Subscription](t, hooks, "AfterArchiveSubscription")
		test.EqOp(t, retiredSub, sub)
		test.NotNil(t, sub.ArchivedAt)

		oneTime := mustCreateProduct(t, env, store, testScope, oneTimeProduct("lifetime"))
		purchase := mustCreatePurchase(t, env, store, testScope, outstandingPurchase(oneTime.ID, testAccount))
		_, purchaseRow := lastAs[Purchase](t, hooks, "AfterCreatePurchase")
		test.EqOp(t, purchase, purchaseRow)
		test.Nil(t, purchaseRow.CompletedAt)

		settled, err := env.completePurchase(t, store, testScope, purchase.ID, testNow)
		must.NoError(t, err)
		_, purchaseRow = lastAs[Purchase](t, hooks, "AfterCompletePurchase")
		test.EqOp(t, settled, purchaseRow)
		test.NotNil(t, purchaseRow.CompletedAt)

		retiredPurchase, err := env.archivePurchase(t, store, testScope, purchase.ID)
		must.NoError(t, err)
		_, purchaseRow = lastAs[Purchase](t, hooks, "AfterArchivePurchase")
		test.EqOp(t, retiredPurchase, purchaseRow)
		test.NotNil(t, purchaseRow.ArchivedAt)

		retiredProduct, err := env.archiveProduct(t, store, testScope, product.ID)
		must.NoError(t, err)
		_, row = lastAs[Product](t, hooks, "AfterArchiveProduct")
		test.EqOp(t, retiredProduct, row)
		test.NotNil(t, row.ArchivedAt)

		// Thirteen writes, one product created twice.
		test.SliceLen(t, 14, hooks.calls)
	})

	t.Run("a refused write calls no hook", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newHookedStore(t, hooks)

		product := mustCreateProduct(t, env, store, testScope, oneTimeProduct("lifetime"))
		purchase := mustCreatePurchase(t, env, store, testScope, outstandingPurchase(product.ID, testAccount))
		_, err := env.completePurchase(t, store, testScope, purchase.ID, testNow)
		must.NoError(t, err)

		recorded := mustRecordTransaction(t, env, store, testScope, pendingTransaction(testAccount))
		before := len(hooks.calls)

		// A redelivered settlement.
		_, err = env.completePurchase(t, store, testScope, purchase.ID, testNow)
		must.ErrorIs(t, err, ErrAlreadyCompleted)

		// A redelivered status.
		err = env.setTransactionStatus(t, store, testScope, recorded.ID, TransactionPending)
		must.ErrorIs(t, err, ErrStatusUnchanged)

		// A status move against an agreement nobody has: refused by the read the
		// hooks pay for, with the sentinel the guard would have answered with.
		err = env.setSubscriptionStatus(t, store, testScope, "nobody", capitalism.SubscriptionStatusCanceled)
		must.ErrorIs(t, err, ErrSubscriptionNotFound)

		// An update of a product nobody has, likewise.
		missing := recurringProduct("missing")
		missing.ID = "nobody"
		_, err = env.updateProduct(t, store, testScope, missing)
		must.ErrorIs(t, err, ErrProductNotFound)

		_, err = env.archiveProduct(t, store, otherScope, product.ID)
		must.ErrorIs(t, err, ErrProductNotFound)

		test.SliceLen(t, before, hooks.calls)
	})

	t.Run("a hook's error is the write's, and the write rolls back with it", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterCreateProduct"}
		store := env.newHookedStore(t, hooks)

		proposed := recurringProduct("monthly")
		proposed.ID = "doomed"

		created, err := env.createProduct(t, store, testScope, proposed)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, created)

		_, err = store.GetProduct(t.Context(), env.reader(), testScope, "doomed")
		must.ErrorIs(t, err, ErrProductNotFound)
	})

	t.Run("a status move's hook error is the move's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterSetSubscriptionStatus"}
		store := env.newHookedStore(t, hooks)

		product := mustCreateProduct(t, env, store, testScope, recurringProduct("monthly"))
		subscription := mustCreateSubscription(t, env, store, testScope, currentSubscription(product.ID, testAccount))

		err := env.setSubscriptionStatus(t, store, testScope, subscription.ID, capitalism.SubscriptionStatusCanceled)
		must.ErrorIs(t, err, errHook)

		read, err := store.GetSubscription(t.Context(), env.reader(), testScope, subscription.ID)
		must.NoError(t, err)
		test.EqOp(t, capitalism.SubscriptionStatusActive, read.Status)
	})

	t.Run("an archive's hook error leaves the row live", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterArchiveTransaction"}
		store := env.newHookedStore(t, hooks)

		recorded := mustRecordTransaction(t, env, store, testScope, pendingTransaction(testAccount))

		retired, err := env.archiveTransaction(t, store, testScope, recorded.ID)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, retired)

		_, err = store.GetTransaction(t.Context(), env.reader(), testScope, recorded.ID)
		must.NoError(t, err)
	})

	t.Run("nil hooks are refused", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(env.client, nil)
		must.ErrorIs(t, err, ErrNilHooks)
		test.Nil(t, store)
	})

	t.Run("an update reads its before row only for hooks", func(t *testing.T) {
		t.Parallel()

		// Each runs one update on a fresh store and reports how many statements
		// it sent. installedHooks commits nothing either, so the difference
		// between it and NoopHooks is the before read and nothing else.
		updateProduct := func(t *testing.T, hooks Hooks) int64 {
			t.Helper()

			store := env.newHookedStore(t, hooks)
			product := mustCreateProduct(t, env, store, testScope, recurringProduct("monthly"))
			repriced := *product
			repriced.AmountCents = product.AmountCents + 100

			return countStatements(t, env, func(tx database.Tx) error {
				_, err := store.UpdateProduct(t.Context(), tx, testScope, &repriced)
				return err
			})
		}

		updateSubscription := func(t *testing.T, hooks Hooks) int64 {
			t.Helper()

			store := env.newHookedStore(t, hooks)
			product := mustCreateProduct(t, env, store, testScope, recurringProduct("monthly"))
			subscription := mustCreateSubscription(t, env, store, testScope, currentSubscription(product.ID, testAccount))
			extended := *subscription
			extended.CurrentPeriodEnd = subscription.CurrentPeriodEnd.AddDate(0, 1, 0)

			return countStatements(t, env, func(tx database.Tx) error {
				_, err := store.UpdateSubscription(t.Context(), tx, testScope, &extended)
				return err
			})
		}

		setSubscriptionStatus := func(t *testing.T, hooks Hooks) int64 {
			t.Helper()

			store := env.newHookedStore(t, hooks)
			product := mustCreateProduct(t, env, store, testScope, recurringProduct("monthly"))
			subscription := mustCreateSubscription(t, env, store, testScope, currentSubscription(product.ID, testAccount))

			return countStatements(t, env, func(tx database.Tx) error {
				return store.SetSubscriptionStatus(t.Context(), tx, testScope, subscription.ID, capitalism.SubscriptionStatusPastDue)
			})
		}

		setTransactionStatus := func(t *testing.T, hooks Hooks) int64 {
			t.Helper()

			store := env.newHookedStore(t, hooks)
			recorded := mustRecordTransaction(t, env, store, testScope, pendingTransaction(testAccount))

			return countStatements(t, env, func(tx database.Tx) error {
				return store.SetTransactionStatus(t.Context(), tx, testScope, recorded.ID, TransactionSucceeded)
			})
		}

		test.Less(t, updateProduct(t, installedHooks{}), updateProduct(t, NoopHooks{}), test.Sprint("UpdateProduct"))
		test.Less(t, updateSubscription(t, installedHooks{}), updateSubscription(t, NoopHooks{}), test.Sprint("UpdateSubscription"))

		// The two status writes return only an error, so with installed hooks they
		// read the row after the write as well as before it. Both reads are the
		// hooks', and a Less would still pass with one of them leaked to every
		// store, so the difference is pinned at both.
		test.EqOp(t, setSubscriptionStatus(t, NoopHooks{})+2, setSubscriptionStatus(t, installedHooks{}), test.Sprint("SetSubscriptionStatus"))
		test.EqOp(t, setTransactionStatus(t, NoopHooks{})+2, setTransactionStatus(t, installedHooks{}), test.Sprint("SetTransactionStatus"))
	})
}

// installedHooks is hooks that are not NoopHooks, and so pay for the before
// read, while committing nothing of their own.
type installedHooks struct{ NoopHooks }

// countStatements runs fn in a transaction of its own and reports how many
// statements it sent through it.
func countStatements(tb testing.TB, env *storeEnv, fn func(tx database.Tx) error) int64 {
	tb.Helper()

	var counted *txcount.Tx

	must.NoError(tb, env.inTx(tb, func(tx database.Tx) error {
		counted = txcount.Wrap(tx)

		return fn(counted)
	}))

	return counted.Statements()
}

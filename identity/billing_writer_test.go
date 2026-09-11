package identity

import (
	"testing"
	"time"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runBillingWriterSuite covers the surface a processor webhook reaches, which is
// why what each method can and cannot write is worth stating case by case.
func runBillingWriterSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a delivery writes standing, plan and stamp together", func(t *testing.T) {
		t.Parallel()

		clk := newFixedClock(baseTime)

		store, err := NewSQLStore(env.client, WithTablePrefix(env.migrate(t)), WithClock(clk))
		must.NoError(t, err)

		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		must.NoError(t, env.setAccountPaymentProcessorCustomerID(t, store, testScope, account.ID, "cus_123"))
		must.NoError(t, env.recordAccountSubscription(t, store, testScope, account.ID, BillingTrial, "plan_pro"))

		// The three columns of the delivery move as one fact, so there is no
		// state in which the standing has moved and the plan has not.
		full, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.EqOp(t, BillingTrial, full.BillingStatus)
		must.NotNil(t, full.SubscriptionPlanID)
		test.EqOp(t, "plan_pro", *full.SubscriptionPlanID)
		must.NotNil(t, full.LastPaymentProviderSyncedAt)
		test.EqOp(t, baseTime, *full.LastPaymentProviderSyncedAt)

		// And the customer attachment, which no delivery restates, survives it.
		test.EqOp(t, "cus_123", full.PaymentProcessorCustomerID)

		// A plan change is the same write, and it carries its own stamp.
		clk.advance(time.Hour)
		must.NoError(t, env.recordAccountSubscription(t, store, testScope, account.ID, BillingPaid, "plan_basic"))

		moved, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.EqOp(t, BillingPaid, moved.BillingStatus)
		must.NotNil(t, moved.SubscriptionPlanID)
		test.EqOp(t, "plan_basic", *moved.SubscriptionPlanID)
		must.NotNil(t, moved.LastPaymentProviderSyncedAt)
		test.EqOp(t, baseTime.Add(time.Hour), *moved.LastPaymentProviderSyncedAt)
		test.EqOp(t, "cus_123", moved.PaymentProcessorCustomerID)
	})

	t.Run("an ended subscription leaves the account on no plan", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		must.NoError(t, env.recordAccountSubscription(t, store, testScope, account.ID, BillingPaid, "plan_pro"))
		must.NoError(t, env.recordAccountSubscriptionEnded(t, store, testScope, account.ID, BillingUnpaid))

		// NULL rather than the empty string, and rather than the plan the
		// account has stopped paying for. This is the write the tempting static
		// rewrite of the old conditional SET could not express: under a
		// COALESCE(narg, column) encoding the NULL that says "set it to nothing"
		// is the same NULL that says "leave it alone".
		cancelled, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.Nil(t, cancelled.SubscriptionPlanID)
		test.EqOp(t, BillingUnpaid, cancelled.BillingStatus)
		must.NotNil(t, cancelled.LastPaymentProviderSyncedAt)

		// Re-subscribing after a cancellation writes a plan again, so the
		// column's whole domain stays reachable from this surface.
		must.NoError(t, env.recordAccountSubscription(t, store, testScope, account.ID, BillingPaid, "plan_basic"))

		resubscribed, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		must.NotNil(t, resubscribed.SubscriptionPlanID)
		test.EqOp(t, "plan_basic", *resubscribed.SubscriptionPlanID)
	})

	t.Run("an operator's suspension moves the standing alone", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		must.NoError(t, env.recordAccountSubscription(t, store, testScope, account.ID, BillingPaid, "plan_pro"))

		before, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		must.NotNil(t, before.LastPaymentProviderSyncedAt)

		must.NoError(t, env.setAccountBillingStatus(t, store, testScope, account.ID, BillingSuspended))

		// The plan stays, because a suspension is not a cancellation, and the
		// stamp stays where the last delivery left it, because nothing was asked
		// of the processor.
		suspended, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.EqOp(t, BillingSuspended, suspended.BillingStatus)
		must.NotNil(t, suspended.SubscriptionPlanID)
		test.EqOp(t, "plan_pro", *suspended.SubscriptionPlanID)
		must.NotNil(t, suspended.LastPaymentProviderSyncedAt)
		test.EqOp(t, *before.LastPaymentProviderSyncedAt, *suspended.LastPaymentProviderSyncedAt)
	})

	t.Run("stamps a reconciliation that moved nothing", func(t *testing.T) {
		t.Parallel()

		clk := newFixedClock(baseTime)

		store, err := NewSQLStore(env.client, WithTablePrefix(env.migrate(t)), WithClock(clk))
		must.NoError(t, err)

		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		before, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.Nil(t, before.LastPaymentProviderSyncedAt)

		stamped, err := env.markAccountBillingSynced(t, store, testScope, account.ID)
		must.NoError(t, err)

		// The stamp is the whole content of the write and it is a read of this
		// Store's clock, so a reconciler that had to read the account again to
		// learn when it last reconciled would be paying for that answer twice.
		must.NotNil(t, stamped.LastPaymentProviderSyncedAt)
		test.EqOp(t, baseTime, *stamped.LastPaymentProviderSyncedAt)

		synced, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		must.NotNil(t, synced.LastPaymentProviderSyncedAt)
		test.EqOp(t, baseTime, *synced.LastPaymentProviderSyncedAt)

		// Nothing else moved with it: the stamp is the whole of what this write
		// says.
		test.EqOp(t, BillingUnpaid, synced.BillingStatus)
		test.Nil(t, synced.SubscriptionPlanID)

		clk.advance(time.Hour)

		again, err := env.markAccountBillingSynced(t, store, testScope, account.ID)
		must.NoError(t, err)
		must.NotNil(t, again.LastPaymentProviderSyncedAt)
		test.EqOp(t, baseTime.Add(time.Hour), *again.LastPaymentProviderSyncedAt)

		// A refusal answers with no account: the row comes back only beside a
		// nil error.
		missing, err := env.markAccountBillingSynced(t, store, otherScope, account.ID)
		must.ErrorIs(t, err, ErrAccountNotFound)
		test.Nil(t, missing)
	})

	t.Run("refuses a value that would be a clear dressed as a write", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		// An empty plan is an ended subscription, and ending has its own method.
		must.ErrorIs(t,
			env.recordAccountSubscription(t, store, testScope, account.ID, BillingPaid, ""),
			platformerrors.ErrEmptyInputParameter,
		)

		// The empty customer identifier is how "not created at the processor" is
		// stored, so writing it here would be a detachment.
		must.ErrorIs(t,
			env.setAccountPaymentProcessorCustomerID(t, store, testScope, account.ID, ""),
			platformerrors.ErrEmptyInputParameter,
		)

		nonsense := BillingStatus("nonsense")

		must.ErrorIs(t,
			env.setAccountBillingStatus(t, store, testScope, account.ID, nonsense),
			platformerrors.ErrUnrecognizedInputValue,
		)
		must.ErrorIs(t,
			env.recordAccountSubscription(t, store, testScope, account.ID, nonsense, "plan_pro"),
			platformerrors.ErrUnrecognizedInputValue,
		)
		must.ErrorIs(t,
			env.recordAccountSubscriptionEnded(t, store, testScope, account.ID, nonsense),
			platformerrors.ErrUnrecognizedInputValue,
		)

		// None of the refusals reached the database.
		unchanged, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.EqOp(t, BillingUnpaid, unchanged.BillingStatus)
		test.Nil(t, unchanged.SubscriptionPlanID)
		test.EqOp(t, "", unchanged.PaymentProcessorCustomerID)
		test.Nil(t, unchanged.LastPaymentProviderSyncedAt)
	})

	t.Run("refuses a write aimed at another directory", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		// The scope is in every one of these predicates, so each matches
		// nothing — and reporting success for a write that touched no row is
		// what would tell the caller their change landed.
		must.ErrorIs(t,
			env.setAccountBillingStatus(t, store, otherScope, account.ID, BillingPaid),
			ErrAccountNotFound,
		)
		must.ErrorIs(t,
			env.recordAccountSubscription(t, store, otherScope, account.ID, BillingPaid, "plan_pro"),
			ErrAccountNotFound,
		)
		must.ErrorIs(t,
			env.recordAccountSubscriptionEnded(t, store, otherScope, account.ID, BillingUnpaid),
			ErrAccountNotFound,
		)
		must.ErrorIs(t,
			env.setAccountPaymentProcessorCustomerID(t, store, otherScope, account.ID, "cus_123"),
			ErrAccountNotFound,
		)
		must.ErrorIs(t,
			env.markAccountBillingSyncedErr(t, store, otherScope, account.ID),
			ErrAccountNotFound,
		)

		unchanged, err := store.GetAccount(t.Context(), env.reader(), testScope, account.ID)
		must.NoError(t, err)
		test.EqOp(t, BillingUnpaid, unchanged.BillingStatus)
	})

	t.Run("refuses an unset scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		var unset tenancy.Scope

		must.Error(t, env.setAccountBillingStatus(t, store, unset, account.ID, BillingPaid))
		must.Error(t, env.recordAccountSubscription(t, store, unset, account.ID, BillingPaid, "plan_pro"))
		must.Error(t, env.recordAccountSubscriptionEnded(t, store, unset, account.ID, BillingUnpaid))
		must.Error(t, env.setAccountPaymentProcessorCustomerID(t, store, unset, account.ID, "cus_123"))
		must.Error(t, env.markAccountBillingSyncedErr(t, store, unset, account.ID))
	})
}

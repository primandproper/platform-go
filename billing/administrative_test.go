package billing

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runAdministrativeSuite is the reads keyed on a provider's identifier and the
// two archives that retire a row by hand.
//
// They are grouped because they share a promise the per-entity suites do not
// make: each of these statements sees archived rows — the by-external-id read
// because it is also the collision check a write runs — and it is the Go around
// them, not the SQL, that decides what an archived row means to a caller.
func runAdministrativeSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("reads a purchase by the provider's payment identifier", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		product := mustCreateProduct(t, env, store, testScope, oneTimeProduct("lifetime"))

		purchase := outstandingPurchase(product.ID, testAccount)
		purchase.ExternalTransactionID = "pi_stripe_1"
		created := mustCreatePurchase(t, env, store, testScope, purchase)

		read, err := store.GetPurchaseByExternalID(t.Context(), env.reader(), testScope, "pi_stripe_1")
		must.NoError(t, err)
		test.EqOp(t, created.ID, read.ID)
	})

	t.Run("does not report an archived purchase, though the statement can see it", func(t *testing.T) {
		t.Parallel()

		// The read behind this is the write's collision check, so it returns
		// archived rows on purpose. A caller asking for a live purchase is not
		// the caller asking whether an identifier is taken.
		store := env.newStore(t)
		product := mustCreateProduct(t, env, store, testScope, oneTimeProduct("lifetime"))

		purchase := outstandingPurchase(product.ID, testAccount)
		purchase.ExternalTransactionID = "pi_stripe_archived"
		created := mustCreatePurchase(t, env, store, testScope, purchase)

		must.NoError(t, env.archivePurchaseErr(t, store, testScope, created.ID))

		_, err := store.GetPurchaseByExternalID(t.Context(), env.reader(), testScope, "pi_stripe_archived")
		test.ErrorIs(t, err, ErrPurchaseNotFound)
	})

	t.Run("keeps a payment identifier taken after the purchase is archived", func(t *testing.T) {
		t.Parallel()

		// The other side of the same fact: a provider that redelivers a charge
		// for a sale somebody archived must not be able to write it again.
		store := env.newStore(t)
		product := mustCreateProduct(t, env, store, testScope, oneTimeProduct("lifetime"))

		first := outstandingPurchase(product.ID, testAccount)
		first.ExternalTransactionID = "pi_stripe_reused"
		created := mustCreatePurchase(t, env, store, testScope, first)

		must.NoError(t, env.archivePurchaseErr(t, store, testScope, created.ID))

		second := outstandingPurchase(product.ID, testAccount)
		second.ExternalTransactionID = "pi_stripe_reused"

		_, err := env.createPurchase(t, store, testScope, second)
		test.ErrorIs(t, err, ErrPurchaseExists)
	})

	t.Run("reports a purchase nobody has", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.GetPurchaseByExternalID(t.Context(), env.reader(), testScope, "pi_nobody_has")
		test.ErrorIs(t, err, ErrPurchaseNotFound)
	})

	t.Run("refuses a purchase lookup with no payment identifier", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.GetPurchaseByExternalID(t.Context(), env.reader(), testScope, "")
		test.ErrorIs(t, err, ErrEmptyExternalID)
	})

	t.Run("refuses an unscoped purchase lookup", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.GetPurchaseByExternalID(t.Context(), env.reader(), tenancy.Scope{}, "pi_stripe_1")
		must.Error(t, err)
	})

	t.Run("does not reach another scope's sales by payment identifier", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		product := mustCreateProduct(t, env, store, testScope, oneTimeProduct("lifetime"))

		purchase := outstandingPurchase(product.ID, testAccount)
		purchase.ExternalTransactionID = "pi_stripe_scoped"
		mustCreatePurchase(t, env, store, testScope, purchase)

		_, err := store.GetPurchaseByExternalID(t.Context(), env.reader(), otherScope, "pi_stripe_scoped")
		test.ErrorIs(t, err, ErrPurchaseNotFound)
	})

	t.Run("archives a purchase without touching the ledger", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		product := mustCreateProduct(t, env, store, testScope, oneTimeProduct("lifetime"))
		created := mustCreatePurchase(t, env, store, testScope, outstandingPurchase(product.ID, testAccount))

		ledger := pendingTransaction(testAccount)
		ledger.PurchaseID = created.ID
		recorded := mustRecordTransaction(t, env, store, testScope, ledger)

		retired, err := env.archivePurchase(t, store, testScope, created.ID)
		must.NoError(t, err)

		// Whether the money ever arrived is on the row the archive answered
		// with: to every read by id afterwards, a retired sale that settled and
		// one that never did are the same absence.
		must.NotNil(t, retired)
		must.NotNil(t, retired.ArchivedAt)
		test.False(t, retired.Complete())
		test.EqOp(t, created.AmountCents, retired.AmountCents)

		_, err = store.GetPurchase(t.Context(), env.reader(), testScope, created.ID)
		test.ErrorIs(t, err, ErrPurchaseNotFound)

		// The money that moved is not undone by retiring the sale it paid for.
		stillThere, err := store.GetTransaction(t.Context(), env.reader(), testScope, recorded.ID)
		must.NoError(t, err)
		test.EqOp(t, created.ID, stillThere.PurchaseID)
	})

	t.Run("reports a purchase archive nobody can act on", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		test.ErrorIs(t, env.archivePurchaseErr(t, store, testScope, "purchase-nobody-has"), ErrPurchaseNotFound)
	})

	t.Run("refuses an unscoped purchase archive", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.Error(t, env.archivePurchaseErr(t, store, tenancy.Scope{}, "purchase-1"))
	})

	t.Run("does not archive another scope's sale", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		product := mustCreateProduct(t, env, store, testScope, oneTimeProduct("lifetime"))
		created := mustCreatePurchase(t, env, store, testScope, outstandingPurchase(product.ID, testAccount))

		test.ErrorIs(t, env.archivePurchaseErr(t, store, otherScope, created.ID), ErrPurchaseNotFound)

		survivor, err := store.GetPurchase(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, created.ID, survivor.ID)
	})

	t.Run("archives a ledger row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		recorded := mustRecordTransaction(t, env, store, testScope, pendingTransaction(testAccount))

		retired, err := env.archiveTransaction(t, store, testScope, recorded.ID)
		must.NoError(t, err)

		// A ledger row taken out of a reconciliation is an amount that stops
		// being counted, and this is the last read by id that can say which.
		must.NotNil(t, retired)
		must.NotNil(t, retired.ArchivedAt)
		test.EqOp(t, recorded.AmountCents, retired.AmountCents)
		test.EqOp(t, recorded.Currency, retired.Currency)

		_, err = store.GetTransaction(t.Context(), env.reader(), testScope, recorded.ID)
		test.ErrorIs(t, err, ErrTransactionNotFound)
	})

	t.Run("reports a ledger archive nobody can act on", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		test.ErrorIs(t,
			env.archiveTransactionErr(t, store, testScope, "transaction-nobody-has"),
			ErrTransactionNotFound)
	})

	t.Run("refuses an unscoped ledger archive", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.Error(t, env.archiveTransactionErr(t, store, tenancy.Scope{}, "transaction-1"))
	})

	t.Run("does not archive another scope's ledger row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		recorded := mustRecordTransaction(t, env, store, testScope, pendingTransaction(testAccount))

		test.ErrorIs(t, env.archiveTransactionErr(t, store, otherScope, recorded.ID), ErrTransactionNotFound)

		survivor, err := store.GetTransaction(t.Context(), env.reader(), testScope, recorded.ID)
		must.NoError(t, err)
		test.EqOp(t, recorded.ID, survivor.ID)
	})

	t.Run("lets a subscription keep its own provider subscription through an update", func(t *testing.T) {
		t.Parallel()

		// The update asks whether the identifier is free, and the row that
		// already holds it is not a collision with itself.
		store := env.newStore(t)
		product := mustCreateProduct(t, env, store, testScope, recurringProduct("pro"))

		subscription := currentSubscription(product.ID, testAccount)
		subscription.ExternalSubscriptionID = "sub_stripe_kept"
		created := mustCreateSubscription(t, env, store, testScope, subscription)

		created.CurrentPeriodEnd = created.CurrentPeriodEnd.Add(24 * time.Hour)

		synced, err := env.updateSubscription(t, store, testScope, created)
		must.NoError(t, err)
		must.NotNil(t, synced)
		test.EqOp(t, "sub_stripe_kept", synced.ExternalSubscriptionID)
	})

	t.Run("refuses an update claiming another subscription's provider subscription", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		product := mustCreateProduct(t, env, store, testScope, recurringProduct("pro"))

		first := currentSubscription(product.ID, testAccount)
		first.ExternalSubscriptionID = "sub_stripe_taken"
		mustCreateSubscription(t, env, store, testScope, first)

		second := currentSubscription(product.ID, otherAccount)
		created := mustCreateSubscription(t, env, store, testScope, second)

		created.ExternalSubscriptionID = "sub_stripe_taken"

		test.ErrorIs(t, env.updateSubscriptionErr(t, store, testScope, created), ErrSubscriptionExists)
	})

	t.Run("an update with no provider subscription is always free", func(t *testing.T) {
		t.Parallel()

		// An empty identifier is stored as NULL and NULL repeats, which is what
		// makes an agreement granted by hand storable at all — so several of
		// them can be updated without ever colliding.
		store := env.newStore(t)
		product := mustCreateProduct(t, env, store, testScope, recurringProduct("pro"))

		first := mustCreateSubscription(t, env, store, testScope, currentSubscription(product.ID, testAccount))
		second := mustCreateSubscription(t, env, store, testScope, currentSubscription(product.ID, otherAccount))

		must.NoError(t, env.updateSubscriptionErr(t, store, testScope, first))
		must.NoError(t, env.updateSubscriptionErr(t, store, testScope, second))
	})
}

package billing

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runCallerTransactionSuite is the whole store driven from inside one
// transaction the caller owns, which is the only way it can be driven.
//
// What is under test is the commit boundary — that a write lands with the
// caller's own rows, and that a caller's failure takes it back — and the three
// reads a write makes on that same executor: the product check the two creates
// are gated on, the attribution an insert-ignore makes when it loses, and the
// read that tells a guarded write's replay from a row nobody has. The reads are
// here too, because widening them to database.SQLQueryExecutor is what lets a
// caller see what its own transaction has written and not yet committed.
func runCallerTransactionSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("the thirteen writes commit with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// Everything the transaction below edits, retires or settles, written
		// and committed first — so what the assertions afterwards find is what
		// that transaction did rather than what set it up.
		catalog := mustCreateProduct(t, env, store, testScope, recurringProduct("catalog"))
		withdrawn := mustCreateProduct(t, env, store, testScope, oneTimeProduct("withdrawn"))

		synced := mustCreateSubscription(t, env, store, testScope, currentSubscription(catalog.ID, testAccount))
		moved := mustCreateSubscription(t, env, store, testScope, currentSubscription(catalog.ID, testAccount))
		retiredSubscription := mustCreateSubscription(t, env, store, testScope,
			currentSubscription(catalog.ID, testAccount))

		settling := mustCreatePurchase(t, env, store, testScope, outstandingPurchase(withdrawn.ID, testAccount))
		retiredPurchase := mustCreatePurchase(t, env, store, testScope, outstandingPurchase(withdrawn.ID, testAccount))

		succeeding := mustRecordTransaction(t, env, store, testScope, pendingTransaction(testAccount))
		retiredLedgerRow := mustRecordTransaction(t, env, store, testScope, pendingTransaction(testAccount))

		// The provider's own settlement time, older than the store's clock, as
		// a completion arriving by webhook always is.
		settledAt := testNow.Add(-2 * time.Hour)

		var (
			stocked *Product
			opened  *Subscription
			sold    *Purchase
			charged *Transaction

			// What the seven writes that move an existing row answered with.
			// Each is read back on this transaction, which for the four
			// archives is the only executor that can see the row at all.
			repriced       *Product
			shelved        *Product
			lapsed         *Subscription
			retiredAccount *Subscription
			settled        *Purchase
			retiredSale    *Purchase
			retiredCharge  *Transaction
		)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			var txErr error

			if stocked, txErr = store.CreateProduct(t.Context(), tx, testScope,
				recurringProduct("pro")); txErr != nil {
				return txErr
			}

			// The product check both creates are gated on runs on tx, so it
			// finds a product nothing outside this transaction can see yet.
			if opened, txErr = store.CreateSubscription(t.Context(), tx, testScope,
				currentSubscription(stocked.ID, otherAccount)); txErr != nil {
				return txErr
			}

			if sold, txErr = store.CreatePurchase(t.Context(), tx, testScope,
				outstandingPurchase(stocked.ID, otherAccount)); txErr != nil {
				return txErr
			}

			// And the ledger row points at the sale this same transaction made,
			// which is the shape a checkout path writes in.
			ledgerRow := pendingTransaction(otherAccount)
			ledgerRow.PurchaseID = sold.ID

			if charged, txErr = store.RecordTransaction(t.Context(), tx, testScope, ledgerRow); txErr != nil {
				return txErr
			}

			repricing := *catalog
			repricing.AmountCents = 3_500

			if repriced, txErr = store.UpdateProduct(t.Context(), tx, testScope, &repricing); txErr != nil {
				return txErr
			}

			if shelved, txErr = store.ArchiveProduct(t.Context(), tx, testScope, withdrawn.ID); txErr != nil {
				return txErr
			}

			lapsing := *synced
			lapsing.Status = capitalism.SubscriptionStatusPastDue

			if lapsed, txErr = store.UpdateSubscription(t.Context(), tx, testScope, &lapsing); txErr != nil {
				return txErr
			}

			if txErr = store.SetSubscriptionStatus(t.Context(), tx, testScope, moved.ID,
				capitalism.SubscriptionStatusCanceled); txErr != nil {
				return txErr
			}

			if retiredAccount, txErr = store.ArchiveSubscription(t.Context(), tx, testScope,
				retiredSubscription.ID); txErr != nil {
				return txErr
			}

			if settled, txErr = store.CompletePurchase(t.Context(), tx, testScope,
				settling.ID, settledAt); txErr != nil {
				return txErr
			}

			if retiredSale, txErr = store.ArchivePurchase(t.Context(), tx, testScope,
				retiredPurchase.ID); txErr != nil {
				return txErr
			}

			if txErr = store.SetTransactionStatus(t.Context(), tx, testScope, succeeding.ID,
				TransactionSucceeded); txErr != nil {
				return txErr
			}

			retiredCharge, txErr = store.ArchiveTransaction(t.Context(), tx, testScope, retiredLedgerRow.ID)

			return txErr
		}))

		// The seven writes that move a row answered with the row, read back on
		// the transaction that moved it. For the four archives that is the only
		// executor that ever could: every read by id here skips an archived row,
		// so once this transaction committed nothing below could be fetched
		// again.
		test.EqOp(t, int64(3_500), repriced.AmountCents)
		test.EqOp(t, catalog.Name, repriced.Name)
		test.NotNil(t, repriced.LastUpdatedAt)

		must.NotNil(t, shelved.ArchivedAt)
		test.EqOp(t, "withdrawn", shelved.Name)

		test.EqOp(t, capitalism.SubscriptionStatusPastDue, lapsed.Status)
		test.EqOp(t, testAccount, lapsed.BelongsToAccount)

		must.NotNil(t, retiredAccount.ArchivedAt)
		test.EqOp(t, retiredSubscription.ID, retiredAccount.ID)

		must.NotNil(t, settled.CompletedAt)
		test.True(t, settledAt.Equal(settled.CompletedAt.UTC()))

		must.NotNil(t, retiredSale.ArchivedAt)
		test.EqOp(t, retiredPurchase.ID, retiredSale.ID)

		must.NotNil(t, retiredCharge.ArchivedAt)
		test.EqOp(t, retiredLedgerRow.ID, retiredCharge.ID)

		// The creation times were read back through the caller's executor
		// rather than left waiting on a commit.
		test.NotEqOp(t, "", stocked.ID)
		test.False(t, stocked.CreatedAt.IsZero())
		test.False(t, opened.CreatedAt.IsZero())
		test.False(t, sold.CreatedAt.IsZero())
		test.False(t, charged.CreatedAt.IsZero())

		readProduct, err := store.GetProduct(t.Context(), env.reader(), testScope, stocked.ID)
		must.NoError(t, err)
		test.EqOp(t, "pro", readProduct.Name)

		readProduct, err = store.GetProduct(t.Context(), env.reader(), testScope, catalog.ID)
		must.NoError(t, err)
		test.EqOp(t, int64(3_500), readProduct.AmountCents)

		_, err = store.GetProduct(t.Context(), env.reader(), testScope, withdrawn.ID)
		test.ErrorIs(t, err, ErrProductNotFound)

		readSubscription, err := store.GetSubscription(t.Context(), env.reader(), testScope, opened.ID)
		must.NoError(t, err)
		test.EqOp(t, stocked.ID, readSubscription.ProductID)

		readSubscription, err = store.GetSubscription(t.Context(), env.reader(), testScope, synced.ID)
		must.NoError(t, err)
		test.EqOp(t, capitalism.SubscriptionStatusPastDue, readSubscription.Status)

		readSubscription, err = store.GetSubscription(t.Context(), env.reader(), testScope, moved.ID)
		must.NoError(t, err)
		test.EqOp(t, capitalism.SubscriptionStatusCanceled, readSubscription.Status)

		_, err = store.GetSubscription(t.Context(), env.reader(), testScope, retiredSubscription.ID)
		test.ErrorIs(t, err, ErrSubscriptionNotFound)

		readPurchase, err := store.GetPurchase(t.Context(), env.reader(), testScope, sold.ID)
		must.NoError(t, err)
		test.False(t, readPurchase.Complete())

		readPurchase, err = store.GetPurchase(t.Context(), env.reader(), testScope, settling.ID)
		must.NoError(t, err)
		must.NotNil(t, readPurchase.CompletedAt)
		test.True(t, settledAt.Equal(readPurchase.CompletedAt.UTC()))

		_, err = store.GetPurchase(t.Context(), env.reader(), testScope, retiredPurchase.ID)
		test.ErrorIs(t, err, ErrPurchaseNotFound)

		readLedgerRow, err := store.GetTransaction(t.Context(), env.reader(), testScope, charged.ID)
		must.NoError(t, err)
		test.EqOp(t, sold.ID, readLedgerRow.PurchaseID)

		readLedgerRow, err = store.GetTransaction(t.Context(), env.reader(), testScope, succeeding.ID)
		must.NoError(t, err)
		test.EqOp(t, TransactionSucceeded, readLedgerRow.Status)

		_, err = store.GetTransaction(t.Context(), env.reader(), testScope, retiredLedgerRow.ID)
		test.ErrorIs(t, err, ErrTransactionNotFound)
	})

	t.Run("a rolled back transaction takes every write with it", func(t *testing.T) {
		t.Parallel()

		// The whole point of the shape, seen from the side that matters: the
		// consumer's audit entry fails, and the ledger row goes back with it
		// rather than surviving in a transaction it was never part of.
		store := env.newStore(t)

		catalog := mustCreateProduct(t, env, store, testScope, recurringProduct("catalog"))
		subscription := mustCreateSubscription(t, env, store, testScope,
			currentSubscription(catalog.ID, testAccount))
		purchase := mustCreatePurchase(t, env, store, testScope, outstandingPurchase(catalog.ID, testAccount))
		ledgerRow := mustRecordTransaction(t, env, store, testScope, pendingTransaction(testAccount))

		var (
			stocked *Product
			charged *Transaction
		)

		err := env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			var txErr error

			if stocked, txErr = store.CreateProduct(t.Context(), tx, testScope,
				recurringProduct("pro")); txErr != nil {
				return txErr
			}

			if charged, txErr = store.RecordTransaction(t.Context(), tx, testScope,
				pendingTransaction(otherAccount)); txErr != nil {
				return txErr
			}

			if txErr = store.SetSubscriptionStatus(t.Context(), tx, testScope, subscription.ID,
				capitalism.SubscriptionStatusCanceled); txErr != nil {
				return txErr
			}

			if _, txErr = store.CompletePurchase(t.Context(), tx, testScope, purchase.ID, testNow); txErr != nil {
				return txErr
			}

			if _, txErr = store.ArchiveTransaction(t.Context(), tx, testScope, ledgerRow.ID); txErr != nil {
				return txErr
			}

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		// The ids were minted onto the returned values on the way through.
		// Nothing undoes that, and nothing should: what rolled back is the row.
		test.NotEqOp(t, "", stocked.ID)
		test.NotEqOp(t, "", charged.ID)

		_, err = store.GetProduct(t.Context(), env.reader(), testScope, stocked.ID)
		test.ErrorIs(t, err, ErrProductNotFound)

		_, err = store.GetTransaction(t.Context(), env.reader(), testScope, charged.ID)
		test.ErrorIs(t, err, ErrTransactionNotFound)

		readSubscription, err := store.GetSubscription(t.Context(), env.reader(), testScope, subscription.ID)
		must.NoError(t, err)
		test.EqOp(t, capitalism.SubscriptionStatusActive, readSubscription.Status)

		readPurchase, err := store.GetPurchase(t.Context(), env.reader(), testScope, purchase.ID)
		must.NoError(t, err)
		test.False(t, readPurchase.Complete())

		readLedgerRow, err := store.GetTransaction(t.Context(), env.reader(), testScope, ledgerRow.ID)
		must.NoError(t, err)
		test.Nil(t, readLedgerRow.ArchivedAt)
	})

	t.Run("a redelivery is attributed against the transaction's own rows", func(t *testing.T) {
		t.Parallel()

		// The attribution read every insert-ignore makes on the losing path
		// runs on the caller's executor. Both deliveries are in one uncommitted
		// transaction here, so a read through the store's own reader would find
		// neither row and blame the id rather than the provider's identifier.
		store := env.newStore(t)

		var (
			productReplay      error
			subscriptionReplay error
			purchaseReplay     error
			ledgerReplay       error
		)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			first := recurringProduct("pro")
			first.ExternalProductID = "prod_stripe_1"

			stocked, txErr := store.CreateProduct(t.Context(), tx, testScope, first)
			if txErr != nil {
				return txErr
			}

			second := recurringProduct("pro")
			second.ExternalProductID = "prod_stripe_1"
			_, productReplay = store.CreateProduct(t.Context(), tx, testScope, second)

			opening := currentSubscription(stocked.ID, testAccount)
			opening.ExternalSubscriptionID = "sub_stripe_1"

			if _, txErr = store.CreateSubscription(t.Context(), tx, testScope, opening); txErr != nil {
				return txErr
			}

			reopening := currentSubscription(stocked.ID, testAccount)
			reopening.ExternalSubscriptionID = "sub_stripe_1"
			_, subscriptionReplay = store.CreateSubscription(t.Context(), tx, testScope, reopening)

			selling := outstandingPurchase(stocked.ID, testAccount)
			selling.ExternalTransactionID = "pi_stripe_1"

			if _, txErr = store.CreatePurchase(t.Context(), tx, testScope, selling); txErr != nil {
				return txErr
			}

			reselling := outstandingPurchase(stocked.ID, testAccount)
			reselling.ExternalTransactionID = "pi_stripe_1"
			_, purchaseReplay = store.CreatePurchase(t.Context(), tx, testScope, reselling)

			charge := pendingTransaction(testAccount)
			charge.ExternalTransactionID = "ch_stripe_1"

			if _, txErr = store.RecordTransaction(t.Context(), tx, testScope, charge); txErr != nil {
				return txErr
			}

			recharge := pendingTransaction(testAccount)
			recharge.ExternalTransactionID = "ch_stripe_1"
			_, ledgerReplay = store.RecordTransaction(t.Context(), tx, testScope, recharge)

			return nil
		}))

		test.ErrorIs(t, productReplay, ErrProductExists)
		test.ErrorIs(t, subscriptionReplay, ErrSubscriptionExists)
		test.ErrorIs(t, purchaseReplay, ErrPurchaseExists)
		test.ErrorIs(t, ledgerReplay, ErrTransactionExists)

		// One of each went in, and the redeliveries wrote nothing.
		ledger, err := store.ListTransactions(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, ledger.Data)

		purchases, err := store.ListPurchases(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, purchases.Data)

		subscriptions, err := store.ListSubscriptions(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, subscriptions.Data)
	})

	t.Run("a guarded write is answered by the row its transaction wrote", func(t *testing.T) {
		t.Parallel()

		// The other read that has to be the caller's. A guard reports zero for
		// a row that is not there and for one already holding the value, and
		// the read that tells those apart is made on the losing path — against
		// rows this transaction has written and not committed.
		store := env.newStore(t)

		var (
			unchangedSubscription error
			alreadyCompleted      error
			unchangedLedgerRow    error
		)

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			stocked, txErr := store.CreateProduct(t.Context(), tx, testScope, recurringProduct("pro"))
			if txErr != nil {
				return txErr
			}

			opened, txErr := store.CreateSubscription(t.Context(), tx, testScope,
				currentSubscription(stocked.ID, testAccount))
			if txErr != nil {
				return txErr
			}

			// The status it was opened at, which is the redelivery a webhook
			// handler acknowledges rather than retries.
			unchangedSubscription = store.SetSubscriptionStatus(t.Context(), tx, testScope, opened.ID,
				capitalism.SubscriptionStatusActive)

			sold, txErr := store.CreatePurchase(t.Context(), tx, testScope,
				outstandingPurchase(stocked.ID, testAccount))
			if txErr != nil {
				return txErr
			}

			if _, txErr = store.CompletePurchase(t.Context(), tx, testScope, sold.ID, testNow); txErr != nil {
				return txErr
			}

			_, alreadyCompleted = store.CompletePurchase(t.Context(), tx, testScope, sold.ID, testNow)

			charged, txErr := store.RecordTransaction(t.Context(), tx, testScope,
				pendingTransaction(testAccount))
			if txErr != nil {
				return txErr
			}

			unchangedLedgerRow = store.SetTransactionStatus(t.Context(), tx, testScope, charged.ID,
				TransactionPending)

			return nil
		}))

		test.ErrorIs(t, unchangedSubscription, ErrStatusUnchanged)
		test.ErrorIs(t, alreadyCompleted, ErrAlreadyCompleted)
		test.ErrorIs(t, unchangedLedgerRow, ErrStatusUnchanged)
	})

	t.Run("every method refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		// All thirty, not a representative one. There is no connection of this
		// store's own to fall back to, so a method that quietly found one when
		// handed nothing would be running outside the transaction its caller
		// believes it is in.
		store := env.newStore(t)

		_, err := store.CreateProduct(t.Context(), nil, testScope, recurringProduct("pro"))
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.UpdateProduct(t.Context(), nil, testScope, recurringProduct("pro"))
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ArchiveProduct(t.Context(), nil, testScope, "whatever")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.CreateSubscription(t.Context(), nil, testScope, currentSubscription("p", testAccount))
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.UpdateSubscription(t.Context(), nil, testScope, currentSubscription("p", testAccount))
		test.ErrorIs(t, err, ErrNilExecutor)

		test.ErrorIs(t,
			store.SetSubscriptionStatus(t.Context(), nil, testScope, "whatever",
				capitalism.SubscriptionStatusActive),
			ErrNilExecutor)

		_, err = store.ArchiveSubscription(t.Context(), nil, testScope, "whatever")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.CreatePurchase(t.Context(), nil, testScope, outstandingPurchase("p", testAccount))
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.CompletePurchase(t.Context(), nil, testScope, "whatever", testNow)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ArchivePurchase(t.Context(), nil, testScope, "whatever")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.RecordTransaction(t.Context(), nil, testScope, pendingTransaction(testAccount))
		test.ErrorIs(t, err, ErrNilExecutor)

		test.ErrorIs(t,
			store.SetTransactionStatus(t.Context(), nil, testScope, "whatever", TransactionSucceeded),
			ErrNilExecutor)

		_, err = store.ArchiveTransaction(t.Context(), nil, testScope, "whatever")
		test.ErrorIs(t, err, ErrNilExecutor)

		// And the seventeen reads, which have the same nothing to fall back to.
		_, err = store.GetProduct(t.Context(), nil, testScope, "whatever")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetProductByExternalID(t.Context(), nil, testScope, "prod_abc")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ProductExists(t.Context(), nil, testScope, "whatever")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListProducts(t.Context(), nil, testScope, nil)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetSubscription(t.Context(), nil, testScope, "whatever")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetSubscriptionByExternalID(t.Context(), nil, testScope, "sub_abc")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListSubscriptions(t.Context(), nil, testScope, nil)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListSubscriptionsForAccount(t.Context(), nil, testScope, testAccount, nil)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListCurrentSubscriptions(t.Context(), nil, testScope, testAccount, nil)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetPurchase(t.Context(), nil, testScope, "whatever")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetPurchaseByExternalID(t.Context(), nil, testScope, "pi_abc")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListPurchases(t.Context(), nil, testScope, nil)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListPurchasesForAccount(t.Context(), nil, testScope, testAccount, nil)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetTransaction(t.Context(), nil, testScope, "whatever")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetTransactionByExternalID(t.Context(), nil, testScope, "ch_abc")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListTransactions(t.Context(), nil, testScope, nil)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListTransactionsForAccount(t.Context(), nil, testScope, testAccount, nil)
		test.ErrorIs(t, err, ErrNilExecutor)

		// And nothing was written on the way to refusing.
		products, err := store.ListProducts(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, products.Data)

		ledger, err := store.ListTransactions(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, ledger.Data)
	})

	t.Run("a read on the transaction sees its writes and one outside does not", func(t *testing.T) {
		t.Parallel()

		// The property the reads were widened for. A caller that has just
		// written a row and re-reads it inside the same transaction gets the row
		// it wrote; anybody reading from outside gets the database as it was
		// until the commit.
		store := env.newStore(t)

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			stocked, txErr := store.CreateProduct(t.Context(), tx, testScope, recurringProduct("pro"))
			if txErr != nil {
				return txErr
			}

			// On tx: the row this transaction wrote.
			read, txErr := store.GetProduct(t.Context(), tx, testScope, stocked.ID)
			if txErr != nil {
				return txErr
			}

			test.EqOp(t, "pro", read.Name)

			exists, txErr := store.ProductExists(t.Context(), tx, testScope, stocked.ID)
			if txErr != nil {
				return txErr
			}

			test.True(t, exists)

			page, txErr := store.ListProducts(t.Context(), tx, testScope, nil)
			if txErr != nil {
				return txErr
			}

			test.SliceLen(t, 1, page.Data)

			// And the same reads, on the client, cannot see it: the
			// transaction has not committed, so this is the other half of the
			// same fact rather than a second one.
			outside, txErr := store.ListProducts(t.Context(), env.reader(), testScope, nil)
			if txErr != nil {
				return txErr
			}

			test.SliceEmpty(t, outside.Data)

			return nil
		}))

		// After the commit both executors agree, which is what makes the reading
		// above about visibility rather than about two different rows.
		committed, err := store.ListProducts(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, committed.Data)
	})

	t.Run("the writes refuse what this package will not store", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		catalog := mustCreateProduct(t, env, store, testScope, recurringProduct("catalog"))
		subscription := mustCreateSubscription(t, env, store, testScope,
			currentSubscription(catalog.ID, testAccount))

		var unset tenancy.Scope

		// Collected inside one transaction and asserted outside it, so a failed
		// check does not abort the transaction the next one needs. None of
		// these reaches a statement the database refuses: each is a check this
		// package makes, or a read that finds nothing. Every one of them is
		// refused inside the caller's transaction, which is the only place a
		// write can be refused now.
		var (
			nilProduct, unscopedProduct, unnamedProduct           error
			unidentifiedProductEdit, absentProductEdit            error
			absentProductArchive, foreignProductArchive           error
			nilSubscription, accountlessSubscription              error
			unstockedSubscription, backwardsSubscription          error
			absentSubscriptionEdit, unknownStatus, absentStatus   error
			absentSubscriptionArchive                             error
			nilPurchase, unstockedPurchase, absentCompletion      error
			absentPurchaseArchive                                 error
			nilLedgerRow, ambiguousLedgerRow, unpricedLedgerRow   error
			absentLedgerStatus, invalidLedgerStatus, absentLedger error
		)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			// The row each refusal from a write that answers with one, checked
			// as it is collected. That is the other half of what these
			// signatures promise: the row comes back only alongside a nil
			// error, so a caller that checked the error has nothing to guard
			// against.
			var (
				refusedProduct      *Product
				refusedSubscription *Subscription
				refusedPurchase     *Purchase
				refusedLedgerRow    *Transaction
			)

			_, nilProduct = store.CreateProduct(t.Context(), tx, testScope, nil)
			_, unscopedProduct = store.CreateProduct(t.Context(), tx, unset, recurringProduct("pro"))

			unnamed := recurringProduct("pro")
			unnamed.Name = ""
			_, unnamedProduct = store.CreateProduct(t.Context(), tx, testScope, unnamed)

			refusedProduct, unidentifiedProductEdit = store.UpdateProduct(t.Context(), tx, testScope,
				recurringProduct("pro"))
			test.Nil(t, refusedProduct)

			absent := recurringProduct("pro")
			absent.ID = "prod_never_written"
			refusedProduct, absentProductEdit = store.UpdateProduct(t.Context(), tx, testScope, absent)
			test.Nil(t, refusedProduct)

			refusedProduct, absentProductArchive = store.ArchiveProduct(t.Context(), tx, testScope,
				"prod_never_written")
			test.Nil(t, refusedProduct)

			refusedProduct, foreignProductArchive = store.ArchiveProduct(t.Context(), tx, otherScope, catalog.ID)
			test.Nil(t, refusedProduct)

			_, nilSubscription = store.CreateSubscription(t.Context(), tx, testScope, nil)

			accountless := currentSubscription(catalog.ID, "")
			_, accountlessSubscription = store.CreateSubscription(t.Context(), tx, testScope, accountless)

			_, unstockedSubscription = store.CreateSubscription(t.Context(), tx, testScope,
				currentSubscription("prod_never_written", testAccount))

			backwards := currentSubscription(catalog.ID, testAccount)
			backwards.CurrentPeriodEnd = backwards.CurrentPeriodStart.Add(-time.Hour)
			_, backwardsSubscription = store.CreateSubscription(t.Context(), tx, testScope, backwards)

			absentEdit := currentSubscription(catalog.ID, testAccount)
			absentEdit.ID = "sub_never_written"
			refusedSubscription, absentSubscriptionEdit = store.UpdateSubscription(t.Context(), tx, testScope,
				absentEdit)
			test.Nil(t, refusedSubscription)

			unknownStatus = store.SetSubscriptionStatus(t.Context(), tx, testScope, subscription.ID,
				capitalism.SubscriptionStatus("whatever"))
			absentStatus = store.SetSubscriptionStatus(t.Context(), tx, testScope, "sub_never_written",
				capitalism.SubscriptionStatusCanceled)

			refusedSubscription, absentSubscriptionArchive = store.ArchiveSubscription(t.Context(), tx, testScope,
				"sub_never_written")
			test.Nil(t, refusedSubscription)

			_, nilPurchase = store.CreatePurchase(t.Context(), tx, testScope, nil)
			_, unstockedPurchase = store.CreatePurchase(t.Context(), tx, testScope,
				outstandingPurchase("prod_never_written", testAccount))

			refusedPurchase, absentCompletion = store.CompletePurchase(t.Context(), tx, testScope,
				"pur_never_written", testNow)
			test.Nil(t, refusedPurchase)

			refusedPurchase, absentPurchaseArchive = store.ArchivePurchase(t.Context(), tx, testScope,
				"pur_never_written")
			test.Nil(t, refusedPurchase)

			_, nilLedgerRow = store.RecordTransaction(t.Context(), tx, testScope, nil)

			ambiguous := pendingTransaction(testAccount)
			ambiguous.SubscriptionID = subscription.ID
			ambiguous.PurchaseID = "pur_never_written"
			_, ambiguousLedgerRow = store.RecordTransaction(t.Context(), tx, testScope, ambiguous)

			unpriced := pendingTransaction(testAccount)
			unpriced.Currency = ""
			_, unpricedLedgerRow = store.RecordTransaction(t.Context(), tx, testScope, unpriced)

			invalidLedgerStatus = store.SetTransactionStatus(t.Context(), tx, testScope, "txn_never_written",
				TransactionStatus("whatever"))
			absentLedgerStatus = store.SetTransactionStatus(t.Context(), tx, testScope, "txn_never_written",
				TransactionSucceeded)
			refusedLedgerRow, absentLedger = store.ArchiveTransaction(t.Context(), tx, testScope,
				"txn_never_written")
			test.Nil(t, refusedLedgerRow)

			return nil
		}))

		test.ErrorIs(t, nilProduct, ErrNilProduct)
		test.ErrorIs(t, unscopedProduct, tenancy.ErrNoScope)
		test.ErrorIs(t, unnamedProduct, ErrEmptyProductName)
		test.ErrorIs(t, unidentifiedProductEdit, platformerrors.ErrInvalidIDProvided)
		test.ErrorIs(t, absentProductEdit, ErrProductNotFound)
		test.ErrorIs(t, absentProductArchive, ErrProductNotFound)
		test.ErrorIs(t, foreignProductArchive, ErrProductNotFound)

		test.ErrorIs(t, nilSubscription, ErrNilSubscription)
		test.ErrorIs(t, accountlessSubscription, ErrEmptyAccount)
		test.ErrorIs(t, unstockedSubscription, ErrProductNotFound)
		test.ErrorIs(t, backwardsSubscription, ErrBackwardsPeriod)
		test.ErrorIs(t, absentSubscriptionEdit, ErrSubscriptionNotFound)
		test.ErrorIs(t, unknownStatus, ErrInvalidStatus)
		test.ErrorIs(t, absentStatus, ErrSubscriptionNotFound)
		test.ErrorIs(t, absentSubscriptionArchive, ErrSubscriptionNotFound)

		test.ErrorIs(t, nilPurchase, ErrNilPurchase)
		test.ErrorIs(t, unstockedPurchase, ErrProductNotFound)
		test.ErrorIs(t, absentCompletion, ErrPurchaseNotFound)
		test.ErrorIs(t, absentPurchaseArchive, ErrPurchaseNotFound)

		test.ErrorIs(t, nilLedgerRow, ErrNilTransaction)
		test.ErrorIs(t, ambiguousLedgerRow, ErrAmbiguousTransaction)
		test.ErrorIs(t, unpricedLedgerRow, ErrInvalidCurrency)
		test.ErrorIs(t, invalidLedgerStatus, ErrInvalidStatus)
		test.ErrorIs(t, absentLedgerStatus, ErrTransactionNotFound)
		test.ErrorIs(t, absentLedger, ErrTransactionNotFound)

		// The transaction committed with nothing in it: every refusal was
		// refused before a row changed.
		products, err := store.ListProducts(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, products.Data)

		subscriptions, err := store.ListSubscriptions(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, subscriptions.Data)

		read, err := store.GetSubscription(t.Context(), env.reader(), testScope, subscription.ID)
		must.NoError(t, err)
		test.EqOp(t, capitalism.SubscriptionStatusActive, read.Status)

		ledger, err := store.ListTransactions(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, ledger.Data)
	})
}

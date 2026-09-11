package billing_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/migrations"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Example shows the flow this package exists for: a catalog is stocked, a
// customer's subscription arrives from a payment provider, and the charge that
// renewed it is recorded in the ledger.
func Example() {
	ctx := context.Background()
	store, client := exampleWiring()

	// One catalog, so the scope is global — the shape a single-tenant
	// application has, behaving exactly as it would without the column.
	scope := tenancy.Global()

	// Every write takes the caller's transaction, so each of these opens one.
	// A consumer with something to commit alongside — an audit entry, an outbox
	// event — passes that same Tx to both; Example_callerTransaction is that
	// shape.
	var product *billing.Product

	if err := client.WithTransaction(ctx, func(tx database.Tx) error {
		// The administrative half. Written once, by whoever decides what is on
		// offer, rather than on a request path.
		var txErr error

		product, txErr = store.CreateProduct(ctx, tx, scope, &billing.Product{
			Name:                  "Pro",
			Kind:                  billing.KindRecurring,
			AmountCents:           2_500,
			Currency:              "usd", // upper-cased on write
			BillingIntervalMonths: 1,
			ExternalProductID:     "prod_abc",
		})

		return txErr
	}); err != nil {
		panic(err)
	}

	fmt.Println("selling:", product.Name, product.Currency, product.AmountCents)

	var subscription *billing.Subscription

	if err := client.WithTransaction(ctx, func(tx database.Tx) error {
		// The webhook half. What the provider reports is stored as reported;
		// nothing here decides what the status means.
		var txErr error

		subscription, txErr = store.CreateSubscription(ctx, tx, scope, &billing.Subscription{
			BelongsToAccount:       "account-1",
			ProductID:              product.ID,
			ExternalSubscriptionID: "sub_abc",
			Status:                 capitalism.SubscriptionStatusActive,
			CurrentPeriodStart:     time.Now().Add(-24 * time.Hour),
			CurrentPeriodEnd:       time.Now().Add(30 * 24 * time.Hour),
		})

		return txErr
	}); err != nil {
		panic(err)
	}

	// The ledger. The provider's own identifier is unique within the scope, so
	// the second delivery of one charge collides instead of recording it twice.
	charge := func() *billing.Transaction {
		return &billing.Transaction{
			BelongsToAccount:      "account-1",
			SubscriptionID:        subscription.ID,
			ExternalTransactionID: "ch_abc",
			Status:                billing.TransactionSucceeded,
			AmountCents:           2_500,
			Currency:              "USD",
		}
	}

	if err := client.WithTransaction(ctx, func(tx database.Tx) error {
		_, txErr := store.RecordTransaction(ctx, tx, scope, charge())

		return txErr
	}); err != nil {
		panic(err)
	}

	// The redelivery, in the transaction its own handler opened. It writes
	// nothing, and the transaction it takes back with it holds nothing either.
	err := client.WithTransaction(ctx, func(tx database.Tx) error {
		_, txErr := store.RecordTransaction(ctx, tx, scope, charge())

		return txErr
	})
	fmt.Println("redelivery recorded once:", errors.Is(err, billing.ErrTransactionExists))

	ledger, err := store.ListTransactionsForAccount(ctx, client.Reader(), scope, "account-1", nil)
	if err != nil {
		panic(err)
	}

	fmt.Println("ledger rows:", len(ledger.Data))

	// Output:
	// selling: Pro USD 2500
	// redelivery recorded once: true
	// ledger rows: 1
}

// Example_entitlement shows the seam this package fills and the one it
// deliberately does not.
//
// Which subscriptions cover this instant is a fact, and this store answers it.
// Which of their statuses leaves an account entitled is policy, and stays with
// the caller — see billing/plans, which is the same reading as a
// entitlements.PlanSource.
func Example_entitlement() {
	ctx := context.Background()
	store, client := exampleWiring()
	scope := tenancy.Global()

	var subscription *billing.Subscription

	// The catalog and the agreement, in one transaction: the product check the
	// subscription is gated on runs on that same executor, so it finds a product
	// nothing outside the transaction can see yet.
	if err := client.WithTransaction(ctx, func(tx database.Tx) error {
		product, txErr := store.CreateProduct(ctx, tx, scope, &billing.Product{
			Name:                  "Pro",
			Kind:                  billing.KindRecurring,
			AmountCents:           2_500,
			Currency:              "USD",
			BillingIntervalMonths: 1,
		})
		if txErr != nil {
			return txErr
		}

		subscription, txErr = store.CreateSubscription(ctx, tx, scope, &billing.Subscription{
			BelongsToAccount:   "account-1",
			ProductID:          product.ID,
			Status:             capitalism.SubscriptionStatusPastDue,
			CurrentPeriodStart: time.Now().Add(-24 * time.Hour),
			CurrentPeriodEnd:   time.Now().Add(7 * 24 * time.Hour),
		})

		return txErr
	}); err != nil {
		panic(err)
	}

	// The read holds no transaction, so it passes the client's reader. It would
	// take the Tx above just as well, and see the row before it committed.
	current, err := store.ListCurrentSubscriptions(ctx, client.Reader(), scope, "account-1", nil)
	if err != nil {
		panic(err)
	}

	fmt.Println("inside its paid period:", len(current.Data) == 1)
	fmt.Println("what the processor says:", subscription.Status)

	// The judgement, which is the caller's. Keeping a past_due customer working
	// through the dunning window is a decision a deployment makes; this package
	// stores the fact it is made from.
	entitled := subscription.Status == capitalism.SubscriptionStatusActive ||
		subscription.Status == capitalism.SubscriptionStatusPastDue

	fmt.Println("our rule says entitled:", entitled)

	// Output:
	// inside its paid period: true
	// what the processor says: past_due
	// our rule says entitled: true
}

// Example_redeliveredStatus shows the guard every provider-driven write carries:
// the second delivery of one event writes nothing and says so.
func Example_redeliveredStatus() {
	ctx := context.Background()
	store, client := exampleWiring()
	scope := tenancy.Global()

	var subscription *billing.Subscription

	if err := client.WithTransaction(ctx, func(tx database.Tx) error {
		product, txErr := store.CreateProduct(ctx, tx, scope, &billing.Product{
			Name:                  "Pro",
			Kind:                  billing.KindRecurring,
			AmountCents:           2_500,
			Currency:              "USD",
			BillingIntervalMonths: 1,
		})
		if txErr != nil {
			return txErr
		}

		subscription, txErr = store.CreateSubscription(ctx, tx, scope, &billing.Subscription{
			BelongsToAccount:   "account-1",
			ProductID:          product.ID,
			Status:             capitalism.SubscriptionStatusActive,
			CurrentPeriodStart: time.Now().Add(-24 * time.Hour),
			CurrentPeriodEnd:   time.Now().Add(7 * 24 * time.Hour),
		})

		return txErr
	}); err != nil {
		panic(err)
	}

	// One transaction per delivery, which is what a webhook endpoint opens.
	cancel := func(subscriptionID string) error {
		return client.WithTransaction(ctx, func(tx database.Tx) error {
			return store.SetSubscriptionStatus(ctx, tx, scope, subscriptionID,
				capitalism.SubscriptionStatusCanceled)
		})
	}

	if err := cancel(subscription.ID); err != nil {
		panic(err)
	}

	// An answer rather than a failure: the work the event describes has already
	// been done, and a handler acknowledges the delivery on it.
	fmt.Println("already recorded:", errors.Is(cancel(subscription.ID), billing.ErrStatusUnchanged))

	// A write against a subscription nobody has is a different thing entirely,
	// and is reported as one.
	fmt.Println("no such subscription:",
		errors.Is(cancel("sub-nobody-has"), billing.ErrSubscriptionNotFound))

	// Output:
	// already recorded: true
	// no such subscription: true
}

// Example_callerTransaction shows what the caller's transaction is for: a charge
// and what a consumer records about it, in one transaction.
//
// A ledger row that committed ahead of its audit entry is a charge nothing
// downstream heard about, and no amount of care outside this package could close
// that window while the store owned the transaction. Taking a database.Tx is how
// it hands that over.
func Example_callerTransaction() {
	ctx := context.Background()
	client := exampleDatabase(ctx)
	scope := tenancy.Global()

	// The consumer's own table, standing in for whatever a real application
	// writes beside a ledger row: an audit entry, a data change event on an
	// outbox a webhook dispatcher fans out.
	if _, err := client.Writer().ExecContext(ctx,
		`CREATE TABLE audit_log (account_id TEXT NOT NULL, event TEXT NOT NULL, amount_cents INTEGER NOT NULL)`); err != nil {
		panic(err)
	}

	store, err := billing.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		// The catalog, the sale and the charge, all in the caller's
		// transaction — so the product check behind the sale finds a product
		// nothing outside this transaction can see yet.
		product, txErr := store.CreateProduct(ctx, tx, scope, &billing.Product{
			Name:        "Lifetime",
			Kind:        billing.KindOneTime,
			AmountCents: 9_900,
			Currency:    "USD",
		})
		if txErr != nil {
			return txErr
		}

		purchase, txErr := store.CreatePurchase(ctx, tx, scope, &billing.Purchase{
			BelongsToAccount: "account-1",
			ProductID:        product.ID,
			AmountCents:      product.AmountCents,
			Currency:         product.Currency,
		})
		if txErr != nil {
			return txErr
		}

		charge, txErr := store.RecordTransaction(ctx, tx, scope, &billing.Transaction{
			BelongsToAccount:      "account-1",
			PurchaseID:            purchase.ID,
			ExternalTransactionID: "ch_abc",
			Status:                billing.TransactionSucceeded,
			AmountCents:           product.AmountCents,
			Currency:              product.Currency,
		})
		if txErr != nil {
			return txErr
		}

		// A failure here takes the charge back with it, which is the whole
		// reason this is one transaction rather than two.
		_, txErr = tx.ExecContext(ctx,
			`INSERT INTO audit_log (account_id, event, amount_cents) VALUES (?, ?, ?)`,
			charge.BelongsToAccount, "purchase.completed", charge.AmountCents)

		return txErr
	}); err != nil {
		panic(err)
	}

	var audited int64
	if err = client.Reader().QueryRowContext(ctx,
		`SELECT amount_cents FROM audit_log WHERE account_id = ?`, "account-1").Scan(&audited); err != nil {
		panic(err)
	}

	ledger, err := store.ListTransactionsForAccount(ctx, client.Reader(), tenancy.Global(), "account-1", nil)
	if err != nil {
		panic(err)
	}

	fmt.Println("ledger rows:", len(ledger.Data))
	fmt.Println("audited:", audited)

	// Output:
	// ledger rows: 1
	// audited: 9900
}

// exampleWiring builds a store over a throwaway SQLite database, and hands back
// the client alongside it.
//
// The store keeps no reference to the client — it takes it for the dialect and
// nothing else — so a caller needs it in its own hands: to open the transaction
// every write takes, and to supply the executor every read takes.
func exampleWiring() (billing.Store, database.Client) {
	client := exampleDatabase(context.Background())

	store, err := billing.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	return store, client
}

// exampleDatabase is a throwaway SQLite database with the billing tables in it.
func exampleDatabase(ctx context.Context) database.Client {
	dir, err := os.MkdirTemp("", "billing-example")
	if err != nil {
		panic(err)
	}

	client, err := sqlite.NewDatabaseClient(ctx, &exampleClientConfig{
		connectionString: filepath.Join(dir, "billing.db"),
	})
	if err != nil {
		panic(err)
	}

	stmts, err := migrations.Statements(dialect.SQLite, billing.DefaultTablePrefix)
	if err != nil {
		panic(err)
	}

	for _, stmt := range stmts {
		if _, err = client.Writer().ExecContext(ctx, stmt); err != nil {
			panic(err)
		}
	}

	return client
}

// exampleClientConfig is the minimum database.ClientConfig a SQLite client
// needs.
type exampleClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*exampleClientConfig)(nil)

func (c *exampleClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *exampleClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *exampleClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *exampleClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *exampleClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *exampleClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *exampleClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

package sync_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/migrations"
	billingsync "github.com/primandproper/platform-go/v14/billing/sync"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Example is a webhook endpoint's whole billing half: four deliveries, one call
// each, on the transaction the handler opened.
//
// The first opens the agreement, the second is the provider sending it again,
// the third is next year's renewal, and the fourth is a status this deployment's
// adapter could not place — which writes nothing at all rather than reading as
// the most permissive standing there is.
func Example() {
	ctx := context.Background()
	store, client := exampleWiring(ctx)
	scope := tenancy.Global()

	var product *billing.Product

	if err := client.WithTransaction(ctx, func(tx database.Tx) error {
		var txErr error

		product, txErr = store.CreateProduct(ctx, tx, scope, &billing.Product{
			Name:                  "Pro",
			Kind:                  billing.KindRecurring,
			AmountCents:           2_500,
			Currency:              "USD",
			BillingIntervalMonths: 12,
			ExternalProductID:     "prod_abc",
		})

		return txErr
	}); err != nil {
		panic(err)
	}

	// The one thing a delivery cannot say. A real deployment reads its account
	// by the customer id capitalism reported, on the executor it is handed, so
	// the lookup sees whatever this transaction has already written.
	//
	// A deployment that also stores identity's coarse standing adds
	// billingsync.WithStanding(identityStore, standing.Strict), and the standing
	// is written on this same transaction.
	syncer, err := billingsync.New(store, func(
		context.Context, database.SQLQueryExecutor, tenancy.Scope, *capitalism.SubscriptionState,
	) (*billingsync.Placement, error) {
		return &billingsync.Placement{AccountID: "account-1", ProductID: product.ID}, nil
	})
	if err != nil {
		panic(err)
	}

	// The window the provider reported. Nothing below derives one: an annual
	// plan renewed today ends a year from today, and the guess every
	// hand-written version makes is a month.
	start := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)

	apply := func(event *capitalism.Event) *billingsync.Result {
		var result *billingsync.Result

		// The handler's transaction. An audit entry or an outbox event goes in
		// this same callback, and commits with the row or not at all.
		if txErr := client.WithTransaction(ctx, func(tx database.Tx) error {
			var applyErr error
			result, applyErr = syncer.Apply(ctx, tx, scope, event)

			return applyErr
		}); txErr != nil {
			panic(txErr)
		}

		return result
	}

	opened := apply(subscriptionEvent(capitalism.SubscriptionStatusActive, "active", &start, &end))
	fmt.Println("opened:", opened.Outcome, opened.Subscription.CurrentPeriodEnd.Format(time.DateOnly))

	fmt.Println("redelivered:", apply(subscriptionEvent(
		capitalism.SubscriptionStatusActive, "active", &start, &end)).Outcome)

	renewedStart, renewedEnd := end, end.AddDate(1, 0, 0)
	renewed := apply(subscriptionEvent(capitalism.SubscriptionStatusActive, "active", &renewedStart, &renewedEnd))
	fmt.Println("renewed:", renewed.Outcome, renewed.Subscription.CurrentPeriodEnd.Format(time.DateOnly))

	// A word this deployment's adapter has never seen. The account keeps the
	// standing it had, and the provider's own spelling is on the span for
	// whoever adds the mapping.
	unplaced := apply(subscriptionEvent(
		capitalism.SubscriptionStatusUnknown, "gracefully_lapsing", &renewedStart, &renewedEnd))
	fmt.Println("unplaced:", unplaced.Outcome, unplaced.Subscription == nil)

	stored, err := store.GetSubscriptionByExternalID(ctx, client.Reader(), scope, "sub_abc")
	if err != nil {
		panic(err)
	}

	fmt.Println("stored:", stored.Status, stored.CurrentPeriodEnd.Format(time.DateOnly))

	// Output:
	// opened: created 2027-09-19
	// redelivered: unchanged
	// renewed: updated 2028-09-19
	// unplaced: unplaced true
	// stored: active 2028-09-19
}

// subscriptionEvent is one delivery about the same agreement.
func subscriptionEvent(
	status capitalism.SubscriptionStatus,
	providerStatus string,
	start, end *time.Time,
) *capitalism.Event {
	return &capitalism.Event{
		ID:   "evt_" + providerStatus,
		Type: "customer.subscription.updated",
		Subscription: &capitalism.SubscriptionState{
			ID:                 "sub_abc",
			CustomerID:         "cus_abc",
			Status:             status,
			ProviderStatus:     providerStatus,
			CurrentPeriodStart: start,
			CurrentPeriodEnd:   end,
		},
	}
}

// exampleWiring is a billing store over a throwaway SQLite database.
func exampleWiring(ctx context.Context) (billing.Store, database.Client) {
	dir, err := os.MkdirTemp("", "billing-sync-example")
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

	store, err := billing.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	return store, client
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

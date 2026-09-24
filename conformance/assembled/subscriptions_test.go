package assembled_test

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/billing"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// subscribe is the Subscribed action: what a consumer's payment provider
// webhook handler does when a checkout completes, which is to stock the plan
// that was bought and record the agreement against it, in one transaction on
// the deployment's own client.
//
// The paid period is whole seconds either side of now, so that no dialect's
// precision decides whether the agreement is current.
func subscribe(db database.Client, store billing.Store) func(context.Context, tenancy.Scope, string) (*billing.Subscription, error) {
	return func(ctx context.Context, scope tenancy.Scope, accountID string) (*billing.Subscription, error) {
		now := time.Now().UTC().Truncate(time.Second)

		var subscription *billing.Subscription

		err := db.WithTransaction(context.WithoutCancel(ctx), func(tx database.Tx) error {
			plan, err := store.CreateProduct(ctx, tx, scope, &billing.Product{
				Name:                  "conformance plan",
				Kind:                  billing.KindRecurring,
				Currency:              "USD",
				AmountCents:           1000,
				BillingIntervalMonths: 1,
				ExternalProductID:     "prod_" + identifiers.New(),
			})
			if err != nil {
				return err
			}

			subscription, err = store.CreateSubscription(ctx, tx, scope, &billing.Subscription{
				BelongsToAccount:       accountID,
				ProductID:              plan.ID,
				ExternalSubscriptionID: "sub_" + identifiers.New(),
				Status:                 capitalism.SubscriptionStatusActive,
				CurrentPeriodStart:     now.Add(-24 * time.Hour),
				CurrentPeriodEnd:       now.Add(24 * time.Hour),
			})

			return err
		})
		if err != nil {
			return nil, err
		}

		return subscription, nil
	}
}

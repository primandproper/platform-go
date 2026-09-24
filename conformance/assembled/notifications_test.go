package assembled_test

import (
	"context"

	"github.com/primandproper/platform-go/v14/notifications"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// notify is the Notified action: what a consumer's application does when
// something happens that a person should hear about, which is to file a
// notification in their inbox inside a transaction on the deployment's own
// client. No surface files one, so the inbox the composition root built is the
// end of the path every deployment takes.
func notify(db database.Client, inbox notifications.Inbox) func(context.Context, tenancy.Scope, string) (string, error) {
	return func(ctx context.Context, scope tenancy.Scope, userID string) (string, error) {
		var filed *notifications.Notification

		err := db.WithTransaction(context.WithoutCancel(ctx), func(tx database.Tx) error {
			created, err := inbox.CreateNotification(ctx, tx, scope, &notifications.Notification{
				Principal: userID,
				Topic:     "conformance.happened",
				Title:     "something happened",
				Body:      "something the conformance suite made happen",
				Link:      "/conformance",
			})
			if err != nil {
				return err
			}

			filed = created

			return nil
		})
		if err != nil {
			return "", err
		}

		return filed.ID, nil
	}
}

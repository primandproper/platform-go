package notifications_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/primandproper/platform-go/v14/notifications"
	"github.com/primandproper/platform-go/v14/notifications/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	"github.com/primandproper/primitives-go/notifications/mobile"
	"github.com/primandproper/primitives-go/tenancy"
)

// Telling somebody something is two writes: the inbox row they will see next
// time they open the app, and the push to whatever handsets they have
// registered.
//
// Both take the caller's transaction. A real service opens that transaction to
// write the order, and the notification about the order goes in beside it — so
// an order that rolls back never told anybody it shipped. Here there is nothing
// else to commit with, which is what Client.WithTransaction is for.
func Example() {
	ctx := context.Background()

	client := exampleClient(ctx)

	store, err := notifications.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	scope := tenancy.Of("acct_1")

	// The inbox row. Topic is the application's own category; the store stores
	// it and never interprets it.
	notification := &notifications.Notification{
		Principal: "user_1",
		Topic:     "order.shipped",
		Title:     "Your order shipped",
		Body:      "Arriving Thursday.",
		Link:      "/orders/1",
	}

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		if createErr := store.CreateNotification(ctx, tx, scope, notification); createErr != nil {
			return createErr
		}

		// The handsets. A device registers itself on every app launch, and the
		// write converges on the token, so this is the same call whether it is
		// the first registration or the thousandth.
		return store.RegisterDevice(ctx, tx, scope, &notifications.Device{
			Principal: "user_1",
			Platform:  notifications.PlatformIOS,
			Token:     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		})
	}); err != nil {
		panic(err)
	}

	// The reads take the wider executor, so this one runs on the client now that
	// the transaction has committed. Passed the tx above, it would have seen the
	// same rows before the commit.
	devices, err := store.ListDevicesByPrincipals(ctx, client.Reader(), scope, []string{"user_1"})
	if err != nil {
		panic(err)
	}

	fmt.Println("devices to push to:", len(devices))

	unread, err := store.ListUnreadNotifications(ctx, client.Reader(), scope, "user_1", nil)
	if err != nil {
		panic(err)
	}

	fmt.Println("badge count:", unread.FilteredCount)

	// Output:
	// devices to push to: 1
	// badge count: 1
}

// Wiring the store into a sender is what closes the loop the registry exists
// for: a provider rejecting a token permanently is what removes the row, and a
// registry nothing tells keeps addressing pushes to handsets that no longer
// exist.
func ExampleRegistry_InvalidateDeviceToken() {
	ctx := context.Background()

	client := exampleClient(ctx)

	store, err := notifications.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	scope := tenancy.Of("acct_1")

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		return store.RegisterDevice(ctx, tx, scope, &notifications.Device{
			Principal: "user_1",
			Platform:  notifications.PlatformAndroid,
			Token:     "a-token-the-app-was-uninstalled-from",
		})
	}); err != nil {
		panic(err)
	}

	// What a real deployment wires, once, at startup. A send that comes back
	// UNREGISTERED then prunes the row on its way out.
	_ = mobile.NewMultiPlatformPushSender(nil, nil, mobile.WithTokenInvalidator(store))

	// Standing in for that send here, because reaching FCM would need
	// credentials: this is the call the sender makes. It takes no transaction,
	// because the sender is mid round trip to a provider and has none.
	if err = store.InvalidateDeviceToken(ctx, "android", "a-token-the-app-was-uninstalled-from"); err != nil {
		panic(err)
	}

	devices, err := store.ListDevicesByPrincipals(ctx, client.Reader(), scope, []string{"user_1"})
	if err != nil {
		panic(err)
	}

	fmt.Println("devices left:", len(devices))

	// Output:
	// devices left: 0
}

// exampleClient is a throwaway SQLite database with the notifications tables in
// it, so the examples above run as written.
func exampleClient(ctx context.Context) database.Client {
	dir, err := os.MkdirTemp("", "notifications-example")
	if err != nil {
		panic(err)
	}

	client, err := sqlite.NewDatabaseClient(ctx,
		&exampleClientConfig{connectionString: filepath.Join(dir, "notifications.db")})
	if err != nil {
		panic(err)
	}

	stmts, err := migrations.Statements(dialect.SQLite, notifications.DefaultTablePrefix)
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

type exampleClientConfig struct {
	connectionString string
}

func (c *exampleClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *exampleClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *exampleClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *exampleClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *exampleClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *exampleClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *exampleClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

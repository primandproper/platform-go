package push_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/primandproper/platform-go/v14/notifications"
	"github.com/primandproper/platform-go/v14/notifications/migrations"
	"github.com/primandproper/platform-go/v14/notifications/push"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/notifications/mobile"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Two people, three handsets, one announcement — and one of the handsets has had
// the app uninstalled since it last registered.
//
// The fan-out is the loop: one query for the tokens, one send each, and the row
// behind the dead token removed on the provider's word. The push that failed is
// still reported as a failure, because it is one; what the prune settles is that
// the next announcement will not address it again.
func Example() {
	ctx := context.Background()

	client := exampleClient(ctx)

	store, err := notifications.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	scope := tenancy.Of("acct_1")

	registrations := []*notifications.Device{
		{Principal: "user_1", Platform: notifications.PlatformIOS, Token: "live-ios-token"},
		{Principal: "user_1", Platform: notifications.PlatformAndroid, Token: "uninstalled-android-token"},
		{Principal: "user_2", Platform: notifications.PlatformIOS, Token: "another-live-ios-token"},
	}

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		for _, registration := range registrations {
			if _, registerErr := store.RegisterDevice(ctx, tx, scope, registration); registerErr != nil {
				return registerErr
			}
		}

		return nil
	}); err != nil {
		panic(err)
	}

	// A real deployment passes mobile.NewMultiPlatformPushSender here. This one
	// stands in for it, answering the way an adapter does: the typed sentinel
	// wrapped around what the provider actually said.
	fanout, err := push.NewFanout(store, &exampleSender{})
	if err != nil {
		panic(err)
	}

	result, err := fanout.Push(ctx, client.Reader(), scope,
		[]string{"user_1", "user_2"},
		mobile.PushMessage{Title: "Your order shipped", Body: "Arriving Thursday."})

	// Both answers. The error says a push did not arrive; the result says which
	// ones did.
	fmt.Println("a push failed:", err != nil)
	fmt.Println("sent:", result.Sent, "failed:", result.Failed, "pruned:", result.Invalidated)

	// The dead token is gone from the registry, so the next announcement resolves
	// to two handsets rather than three.
	devices, err := store.ListDevicesByPrincipals(ctx, client.Reader(), scope, []string{"user_1", "user_2"})
	if err != nil {
		panic(err)
	}

	fmt.Println("handsets left:", len(devices))

	// Output:
	// a push failed: true
	// sent: 2 failed: 1 pruned: 1
	// handsets left: 2
}

// exampleSender is what a provider adapter does in two lines: deliver, and
// classify a permanent rejection as mobile.ErrTokenInvalid.
type exampleSender struct{}

func (*exampleSender) SendPush(_ context.Context, _, token string, _ mobile.PushMessage) error {
	if token == "uninstalled-android-token" {
		return platformerrors.Wrap(mobile.ErrTokenInvalid, "fcm: UNREGISTERED")
	}

	return nil
}

// exampleClient is a throwaway SQLite database with the notifications tables in
// it, so the example above runs as written.
func exampleClient(ctx context.Context) database.Client {
	dir, err := os.MkdirTemp("", "notifications-push-example")
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

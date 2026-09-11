package notificationscfg_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v14/notifications"
	notificationscfg "github.com/primandproper/platform-go/v14/notifications/config"
	"github.com/primandproper/platform-go/v14/notifications/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/notifications/mobile"
	"github.com/primandproper/primitives-go/v2/notifications/mobile/apns"
	mobilecfg "github.com/primandproper/primitives-go/v2/notifications/mobile/config"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The wiring this package exists for, exercised from a container rather than
// from a call: notifications/mobile prunes a dead token only if it has been
// handed the registry holding it, and before there was a notifications/config
// that handoff could not be spelled from configuration at all.

const wiringPrefix = "wiring"

// newMigratedClient returns a SQLite client with the notifications tables
// already created under wiringPrefix.
func newMigratedClient(t *testing.T) database.Client {
	t.Helper()

	path := filepath.Join(t.TempDir(), "notifications.db")
	client, err := databasecfg.NewDatabase(t.Context(), &databasecfg.Config{
		Provider:        databasecfg.ProviderSQLite,
		ReadConnection:  databasecfg.ConnectionDetails{Database: path},
		WriteConnection: databasecfg.ConnectionDetails{Database: path},
	}, nil)
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	stmts, err := migrations.Statements(dialect.SQLite, wiringPrefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	return client
}

// apnsConfig is enough iOS credentials to build a sender offline: the key is
// generated here and never presented to Apple, and apns.NewSender opens no
// connection.
func apnsConfig(t *testing.T) *apns.Config {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must.NoError(t, err)

	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	must.NoError(t, err)

	path := filepath.Join(t.TempDir(), "AuthKey.p8")
	must.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}), 0o600))

	return &apns.Config{AuthKeyPath: path, KeyID: "K1", TeamID: "T1", BundleID: "com.example.app"}
}

func TestBothHalvesCloseTheFeedbackLoop(t *testing.T) {
	t.Parallel()

	client := newMigratedClient(t)

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	do.ProvideValue[database.Client](i, client)
	do.ProvideValue(i, &notificationscfg.Config{TablePrefix: wiringPrefix})
	do.ProvideValue(i, &mobilecfg.Config{Provider: mobilecfg.ProviderAPNs, APNs: apnsConfig(t)})

	notificationscfg.RegisterStore(i)
	mobilecfg.RegisterPushSender(i)

	registry, err := do.Invoke[notifications.Registry](i)
	must.NoError(t, err)

	sender, err := do.Invoke[mobile.PushNotificationSender](i)
	must.NoError(t, err)

	multi, ok := sender.(*mobile.MultiPlatformPushSender)
	must.True(t, ok)

	// The handoff: the sender is pruning the registry this container registered,
	// and nothing in either wiring site named the other. The key the sender
	// resolved is mobile.TokenInvalidator, which RegisterStore registers as a
	// fourth narrowing of the same store — so the three resolutions below are
	// one value, and the sender never named the package that owns the table.
	invalidator, err := do.Invoke[mobile.TokenInvalidator](i)
	must.NoError(t, err)
	test.True(t, invalidator == mobile.TokenInvalidator(registry))
	test.True(t, multi.TokenInvalidator() == invalidator)

	// And the loop runs. What a provider hands a sender is a platform and a
	// token, which is exactly what reaches the registry here, and the row it
	// names is gone afterwards.
	scope := tenancy.Of("acct_1")
	device := &notifications.Device{
		Principal: "user_1",
		Platform:  notifications.PlatformIOS,
		Token:     "token-from-a-handset-since-wiped",
	}
	must.NoError(t, client.WithTransaction(t.Context(), func(tx database.Tx) error {
		_, registerErr := registry.RegisterDevice(t.Context(), tx, scope, device)

		return registerErr
	}))

	before, err := registry.ListDevicesByPrincipals(t.Context(), client.Reader(), scope, []string{"user_1"})
	must.NoError(t, err)
	must.SliceLen(t, 1, before)

	must.NoError(t, multi.TokenInvalidator().InvalidateDeviceToken(
		t.Context(), device.Platform.String(), device.Token))

	after, err := registry.ListDevicesByPrincipals(t.Context(), client.Reader(), scope, []string{"user_1"})
	must.NoError(t, err)
	test.SliceEmpty(t, after)
}

func TestMobileAloneIsUnchanged(t *testing.T) {
	t.Parallel()

	// The other half of the promise: a container that registers no store gets
	// the sender it always got. The classification still reaches the caller as
	// an error; nothing prunes.
	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	do.ProvideValue(i, &mobilecfg.Config{Provider: mobilecfg.ProviderAPNs, APNs: apnsConfig(t)})

	mobilecfg.RegisterPushSender(i)

	sender, err := do.Invoke[mobile.PushNotificationSender](i)
	must.NoError(t, err)

	multi, ok := sender.(*mobile.MultiPlatformPushSender)
	must.True(t, ok)
	test.Nil(t, multi.TokenInvalidator())
}

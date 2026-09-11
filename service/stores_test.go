package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/comments"
	commentscfg "github.com/primandproper/platform-go/v14/comments/config"
	"github.com/primandproper/platform-go/v14/identity"
	identitycfg "github.com/primandproper/platform-go/v14/identity/config"
	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportscfg "github.com/primandproper/platform-go/v14/issuereports/config"
	"github.com/primandproper/platform-go/v14/notifications"
	notificationscfg "github.com/primandproper/platform-go/v14/notifications/config"
	"github.com/primandproper/platform-go/v14/retention"
	retentioncfg "github.com/primandproper/platform-go/v14/retention/config"
	"github.com/primandproper/platform-go/v14/settings"
	settingscfg "github.com/primandproper/platform-go/v14/settings/config"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistscfg "github.com/primandproper/platform-go/v14/waitlists/config"

	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/notifications/mobile"
	"github.com/primandproper/primitives-go/v2/notifications/mobile/apns"
	mobilenotifcfg "github.com/primandproper/primitives-go/v2/notifications/mobile/config"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/caarlos0/env/v11"
	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// storePrefix is what these tests name their tables with. It has to be spelled
// out rather than left to default, because a sub-config holding nothing but the
// library's own defaults is what Config.ValidateWithContext releases — the same
// normalization every other store in the walk is subject to.
const storePrefix = "svc"

// sqliteDatabase returns a database sub-config pointed at a file of this test's
// own. The stores below open no connection while they are being built — every
// one of them composes statements and nothing else — so no migration is needed
// to prove the walk reaches them.
func sqliteDatabase(t *testing.T) *databasecfg.Config {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")

	return &databasecfg.Config{
		Provider:        databasecfg.ProviderSQLite,
		ReadConnection:  databasecfg.ConnectionDetails{Database: path},
		WriteConnection: databasecfg.ConnectionDetails{Database: path},
	}
}

// TestRegisterStores covers the subsystems the composition root reached last:
// the identity, issue report, comment, settings, notifications and waitlist
// stores, and the retention sweeper.
//
// The reflection-driven tests above already assert that a field on Config is
// validated and registers something. What they cannot say is that what it
// registers is the thing the package's own Register bridge builds, which is the
// point of a walk a consumer never reads — so each one is invoked here.
func TestRegisterStores(T *testing.T) {
	T.Parallel()

	T.Run("builds the stores the config names", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Name:          "example",
			Database:      sqliteDatabase(t),
			Identity:      &identitycfg.Config{TablePrefix: storePrefix},
			IssueReports:  &issuereportscfg.Config{TablePrefix: storePrefix},
			Comments:      &commentscfg.Config{TablePrefix: storePrefix},
			Settings:      &settingscfg.Config{TablePrefix: storePrefix},
			Notifications: &notificationscfg.Config{TablePrefix: storePrefix},
			Waitlists:     &waitlistscfg.Config{TablePrefix: storePrefix},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)

		// The comment store's one dependency the environment cannot supply:
		// which kinds of thing accept comments, each type optionally carrying a
		// function that reads the application's own tables.
		do.ProvideValue(i, comments.Targets{comments.TargetType("recipe"): {Description: "a recipe"}})

		identityStore, err := do.Invoke[identity.Store](i)
		must.NoError(t, err)
		test.NotNil(t, identityStore)

		settingsStore, err := do.Invoke[settings.Store](i)
		must.NoError(t, err)
		test.NotNil(t, settingsStore)

		reportStore, err := do.Invoke[issuereports.Store](i)
		must.NoError(t, err)
		test.NotNil(t, reportStore)

		commentStore, err := do.Invoke[comments.Store](i)
		must.NoError(t, err)
		test.NotNil(t, commentStore)

		waitlistStore, err := do.Invoke[waitlists.Store](i)
		must.NoError(t, err)
		test.NotNil(t, waitlistStore)

		// The notifications store is registered under three keys, and the two
		// interfaces are narrowings of the one concrete registration rather
		// than stores of their own.
		concrete, err := do.Invoke[*notifications.SQLStore](i)
		must.NoError(t, err)
		must.NotNil(t, concrete)

		inbox, err := do.Invoke[notifications.Inbox](i)
		must.NoError(t, err)
		test.True(t, inbox == notifications.Inbox(concrete))

		registry, err := do.Invoke[notifications.Registry](i)
		must.NoError(t, err)
		test.True(t, registry == notifications.Registry(concrete))
	})

	T.Run("the environment alone names the newest stores", func(t *testing.T) {
		t.Parallel()

		// The whole point of the entry in Config: a deployment that wants an
		// identity store says so in its environment, and nothing in the
		// adoption diff hand-registers one.
		cfg := &Config{Name: "example"}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{Environment: map[string]string{
			"IDENTITY_TABLE_PREFIX":        storePrefix,
			"ISSUE_REPORTS_TABLE_PREFIX":   storePrefix,
			"COMMENTS_TABLE_PREFIX":        storePrefix,
			"SETTINGS_TABLE_PREFIX":        storePrefix,
			"NOTIFICATIONS_TABLE_PREFIX":   storePrefix,
			"WAITLISTS_TABLE_PREFIX":       storePrefix,
			"RETENTION_SWEEPER_BATCH_SIZE": "500",
		}}))

		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		test.Eq(t, []string{"Comments", "Identity", "IssueReports", "Notifications", "Retention", "Settings", "Waitlists"}, present(t, cfg))
	})

	T.Run("builds the retention sweeper over the application's policies", func(t *testing.T) {
		t.Parallel()

		// What a deployment is allowed to keep is not a platform decision, so
		// the policy set arrives from the application rather than from the
		// config — which is the one thing about this entry that differs from
		// the stores above.
		cfg := &Config{
			Name:      "example",
			Database:  sqliteDatabase(t),
			Retention: &retentioncfg.Config{Sweeper: retention.SweeperConfig{BatchSize: 500}},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)
		do.ProvideValue(i, []retention.Policy{{
			Name:   "expired-oauth2-tokens",
			Scope:  tenancy.Global(),
			Target: retention.Table{Name: "oauth2_client_tokens", Column: "expires_at"},
			Age:    24 * time.Hour,
			Basis:  "an expired access token cannot authorize anything",
		}})

		sweeper, err := do.Invoke[*retention.Sweeper](i)
		must.NoError(t, err)
		must.NotNil(t, sweeper)
		test.SliceLen(t, 1, sweeper.Policies())
	})

	T.Run("the notifications store closes the push sender's feedback loop", func(t *testing.T) {
		t.Parallel()

		// The wiring notifications/doc.go leads with, reached from nothing but
		// a Config: a provider that answers a push with "this handset is gone"
		// gets the device row deleted, because the sender was handed the
		// registry holding it. Neither sub-config names the other.
		cfg := &Config{
			Name:                "example",
			Database:            sqliteDatabase(t),
			Notifications:       &notificationscfg.Config{TablePrefix: storePrefix},
			MobileNotifications: &mobilenotifcfg.Config{Provider: mobilenotifcfg.ProviderAPNs, APNs: offlineAPNs(t)},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)

		registry, err := do.Invoke[notifications.Registry](i)
		must.NoError(t, err)

		sender, err := do.Invoke[mobile.PushNotificationSender](i)
		must.NoError(t, err)

		multi, ok := sender.(*mobile.MultiPlatformPushSender)
		must.True(t, ok)
		test.True(t, multi.TokenInvalidator() == mobile.TokenInvalidator(registry))
	})

	T.Run("a push sender without the store half is what it always was", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Name:                "example",
			MobileNotifications: &mobilenotifcfg.Config{Provider: mobilenotifcfg.ProviderAPNs, APNs: offlineAPNs(t)},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		sender, err := do.Invoke[mobile.PushNotificationSender](newInjector(t, cfg))
		must.NoError(t, err)

		multi, ok := sender.(*mobile.MultiPlatformPushSender)
		must.True(t, ok)
		test.Nil(t, multi.TokenInvalidator())
	})
}

// offlineAPNs is enough iOS credentials to build a sender: the key is generated
// here and never presented to Apple, and apns.NewSender opens no connection.
func offlineAPNs(t *testing.T) *apns.Config {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must.NoError(t, err)

	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	must.NoError(t, err)

	path := filepath.Join(t.TempDir(), "AuthKey.p8")
	must.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}), 0o600))

	return &apns.Config{AuthKeyPath: path, KeyID: "K1", TeamID: "T1", BundleID: "com.example.app"}
}

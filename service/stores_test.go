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

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	oauth2clientscfg "github.com/primandproper/platform-go/v14/authentication/oauth2clients/config"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	passwordresetcfg "github.com/primandproper/platform-go/v14/authentication/passwordreset/config"
	"github.com/primandproper/platform-go/v14/comments"
	commentscfg "github.com/primandproper/platform-go/v14/comments/config"
	"github.com/primandproper/platform-go/v14/identity"
	identitycfg "github.com/primandproper/platform-go/v14/identity/config"
	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportscfg "github.com/primandproper/platform-go/v14/issuereports/config"
	"github.com/primandproper/platform-go/v14/links"
	linkscfg "github.com/primandproper/platform-go/v14/links/config"
	linksdatabase "github.com/primandproper/platform-go/v14/links/database"
	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistrycfg "github.com/primandproper/platform-go/v14/mediaregistry/config"
	"github.com/primandproper/platform-go/v14/notifications"
	notificationscfg "github.com/primandproper/platform-go/v14/notifications/config"
	"github.com/primandproper/platform-go/v14/retention"
	retentioncfg "github.com/primandproper/platform-go/v14/retention/config"
	"github.com/primandproper/platform-go/v14/settings"
	settingscfg "github.com/primandproper/platform-go/v14/settings/config"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistscfg "github.com/primandproper/platform-go/v14/waitlists/config"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/authentication/argon2"
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
// the identity, issue report, comment, settings, notifications, waitlist,
// password reset, oauth2 client and media registry stores, the links minter,
// and the retention sweeper.
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
			MediaRegistry: &mediaregistrycfg.Config{TablePrefix: storePrefix},
			PasswordReset: &passwordresetcfg.Config{TablePrefix: storePrefix},
			OAuth2Clients: &oauth2clientscfg.Config{TablePrefix: storePrefix},
			Links: &linkscfg.Config{
				Database: linksdatabase.Config{TablePrefix: storePrefix},
				// A minter with an empty registry mints nothing, so the
				// constructor refuses one — which makes an action the links
				// entry's equivalent of the comment store's Targets below, and
				// the reason Config.Actions carries no env tag: where a
				// magic-login link points and how long it lives is a policy
				// written in a file somebody reviews.
				Actions: map[links.Action]links.ActionPolicy{
					"magic_login": {URL: "https://example.com/auth/magic/{token}", TTL: links.Duration(15 * time.Minute)},
				},
			},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		i := newInjector(t, cfg)

		// The comment store's one dependency the environment cannot supply:
		// which kinds of thing accept comments, each type optionally carrying a
		// function that reads the application's own tables.
		do.ProvideValue(i, comments.Targets{comments.TargetType("recipe"): {Description: "a recipe"}})

		// The reset flow's two: what delivers a link, and the engine sign-in
		// hashes with, so a reset writes a password sign-in can verify.
		do.ProvideValue[passwordreset.Mailer](i, stubResetMailer{})
		do.ProvideValue[authentication.Authenticator](i, argon2.NewArgon2Authenticator())

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

		mediaStore, err := do.Invoke[mediaregistry.Store](i)
		must.NoError(t, err)
		test.NotNil(t, mediaStore)

		// The client registry registers a service as well as a store, because
		// its surface mounts over the pair.
		clientStore, err := do.Invoke[oauth2clients.Store](i)
		must.NoError(t, err)
		test.NotNil(t, clientStore)

		clientService, err := do.Invoke[*oauth2clients.Service](i)
		must.NoError(t, err)
		test.NotNil(t, clientService)

		// So does password reset, because the reset surface mounts over the
		// service.
		resetStore, err := do.Invoke[passwordreset.Store](i)
		must.NoError(t, err)
		test.NotNil(t, resetStore)

		resetService, err := do.Invoke[*passwordreset.Service](i)
		must.NoError(t, err)
		test.NotNil(t, resetService)

		// The minter rather than a store, because that is what the bridge
		// registers: the table is behind it, and so is the sweeper an
		// unconfigured interval starts.
		minter, err := do.Invoke[*links.Minter](i)
		must.NoError(t, err)
		test.NotNil(t, minter)

		// The notifications store is registered under four keys, and the two
		// halves are narrowings of the one notifications.Store registration
		// rather than stores of their own.
		notificationStore, err := do.Invoke[notifications.Store](i)
		must.NoError(t, err)
		must.NotNil(t, notificationStore)

		inbox, err := do.Invoke[notifications.Inbox](i)
		must.NoError(t, err)
		test.True(t, inbox == notifications.Inbox(notificationStore))

		registry, err := do.Invoke[notifications.Registry](i)
		must.NoError(t, err)
		test.True(t, registry == notifications.Registry(notificationStore))
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
			"OAUTH2_CLIENTS_TABLE_PREFIX":  storePrefix,
			"PASSWORD_RESET_TABLE_PREFIX":  storePrefix,
			"WAITLISTS_TABLE_PREFIX":       storePrefix,
			"RETENTION_SWEEPER_BATCH_SIZE": "500",
			// A nested setting, which is enough for the sign-in block to
			// survive normalization. Rotation, recovery codes and registration
			// are on without it.
			"SIGN_IN_REFRESH_TOKENS_TABLE_PREFIX": storePrefix,
		}}))

		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		test.Eq(t, []string{"Comments", "Identity", "IssueReports", "Notifications", "OAuth2Clients", "PasswordReset", "Retention", "Settings", "SignIn", "Waitlists"}, present(t, cfg))

		test.EqOp(t, storePrefix, cfg.SignIn.RefreshTokens.TablePrefix)
		test.Nil(t, cfg.SignIn.MagicLinks)
		test.False(t, cfg.SignIn.Registration.Disabled)
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

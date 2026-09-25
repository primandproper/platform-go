package signincfg

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"
	identitymock "github.com/primandproper/platform-go/v14/identity/mock"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/caarlos0/env/v11"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testDBClient is a SQLite database nothing has migrated. The doors these
// tests knock on refuse before they read a table, or are asserted only not to
// refuse as unconfigured.
func testDBClient(t *testing.T) database.Client {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	client, err := databasecfg.NewDatabase(t.Context(), &databasecfg.Config{
		Provider:        databasecfg.ProviderSQLite,
		ReadConnection:  databasecfg.ConnectionDetails{Database: path},
		WriteConnection: databasecfg.ConnectionDetails{Database: path},
	}, nil)
	must.NoError(t, err)

	return client
}

// stubIssuer mints a token nobody reads.
type stubIssuer struct{}

func (stubIssuer) IssueToken(context.Context, string, time.Duration, map[string]any) (tokenStr, jti string, err error) {
	return "token", "jti", nil
}

// discardingMailer delivers nothing.
type discardingMailer struct{}

func (discardingMailer) SendMagicLink(context.Context, *signin.MagicLinkMail) error { return nil }

// stubRegistrar is a registrar these tests attach and never reach.
type stubRegistrar struct{ signin.Registrar }

// everyBlock is a config with each optional door switched on, and with the
// sweepers off so no test leaves a goroutine reading an unmigrated table.
func everyBlock() *Config {
	return &Config{
		RefreshTokens: &RefreshTokensConfig{TablePrefix: "ddb", SweepInterval: pointer.To(time.Duration(0))},
		MagicLinks: &MagicLinksConfig{
			TablePrefix:   "ddb",
			SweepInterval: pointer.To(time.Duration(0)),
			RequestFloor:  time.Millisecond,
		},
		RecoveryCodes: &RecoveryCodesConfig{TablePrefix: "ddb"},
		Registration:  &RegistrationConfig{VerificationLinkTTL: time.Hour},
	}
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a zero config and a filled one", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, (&Config{}).ValidateWithContext(t.Context()))

		cfg := everyBlock()
		cfg.SecondFactor = SecondFactorRequired
		cfg.TOTPIssuer = "Example"
		cfg.AdminServiceRoles = []string{"service_admin"}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("defaults a present block's sweep interval and leaves a zero one", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			RefreshTokens: &RefreshTokensConfig{TablePrefix: "ddb"},
			MagicLinks:    &MagicLinksConfig{TablePrefix: "ddb", SweepInterval: pointer.To(time.Duration(0))},
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		test.EqOp(t, DefaultSweepInterval, pointer.Dereference(cfg.RefreshTokens.SweepInterval))
		test.EqOp(t, time.Duration(0), pointer.Dereference(cfg.MagicLinks.SweepInterval))
	})

	T.Run("refuses an unknown second factor policy", func(t *testing.T) {
		t.Parallel()

		err := (&Config{SecondFactor: "sometimes"}).ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), SecondFactorRequired)
	})

	T.Run("refuses a negative lifetime", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{TokenTTL: -time.Second}).ValidateWithContext(t.Context()))
		test.Error(t, (&Config{
			RefreshTokens: &RefreshTokensConfig{TablePrefix: "ddb", AdminTTL: -time.Second},
		}).ValidateWithContext(t.Context()))
		test.Error(t, (&Config{
			Registration: &RegistrationConfig{VerificationLinkTTL: -time.Second},
		}).ValidateWithContext(t.Context()))
	})

	T.Run("refuses a negative floor, count or sweep interval", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{
			MagicLinks: &MagicLinksConfig{TablePrefix: "ddb", RequestFloor: -time.Second},
		}).ValidateWithContext(t.Context()))
		test.Error(t, (&Config{
			RecoveryCodes: &RecoveryCodesConfig{TablePrefix: "ddb", Count: -1},
		}).ValidateWithContext(t.Context()))
		test.Error(t, (&Config{
			RefreshTokens: &RefreshTokensConfig{TablePrefix: "ddb", SweepInterval: pointer.To(-time.Second)},
		}).ValidateWithContext(t.Context()))
	})

	T.Run("refuses a prefix that cannot render, in each store's block", func(t *testing.T) {
		t.Parallel()

		const bad = "ends_in_"

		test.Error(t, (&Config{RefreshTokens: &RefreshTokensConfig{TablePrefix: bad}}).ValidateWithContext(t.Context()))
		test.Error(t, (&Config{MagicLinks: &MagicLinksConfig{TablePrefix: bad}}).ValidateWithContext(t.Context()))
		test.Error(t, (&Config{RecoveryCodes: &RecoveryCodesConfig{TablePrefix: bad}}).ValidateWithContext(t.Context()))
	})
}

// TestConfig_PresenceFromTheEnvironment is the property the nested blocks
// exist for: environment parsing allocates every one, and only the ones that
// name something survive validation.
func TestConfig_PresenceFromTheEnvironment(T *testing.T) {
	T.Parallel()

	parse := func(t *testing.T, environment map[string]string) *Config {
		t.Helper()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{Environment: environment}))
		must.NotNil(t, cfg.RefreshTokens, must.Sprint("env:\",init\" should allocate every block"))
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		return cfg
	}

	T.Run("an empty environment switches nothing on", func(t *testing.T) {
		t.Parallel()

		cfg := parse(t, map[string]string{})
		test.Nil(t, cfg.RefreshTokens)
		test.Nil(t, cfg.MagicLinks)
		test.Nil(t, cfg.RecoveryCodes)
		test.Nil(t, cfg.Registration)
	})

	T.Run("naming anything in a block switches that block on and no other", func(t *testing.T) {
		t.Parallel()

		cfg := parse(t, map[string]string{
			"REFRESH_TOKENS_TABLE_PREFIX":        "ddb",
			"REGISTRATION_VERIFICATION_LINK_TTL": "24h",
			"TOTP_ISSUER":                        "Example",
			"ADMIN_SERVICE_ROLES":                "service_admin,operator",
		})

		must.NotNil(t, cfg.RefreshTokens)
		test.EqOp(t, "ddb", cfg.RefreshTokens.TablePrefix)
		must.NotNil(t, cfg.Registration)
		test.EqOp(t, 24*time.Hour, cfg.Registration.VerificationLinkTTL)
		test.Nil(t, cfg.MagicLinks)
		test.Nil(t, cfg.RecoveryCodes)
		test.EqOp(t, "Example", cfg.TOTPIssuer)
		test.Eq(t, []string{"service_admin", "operator"}, cfg.AdminServiceRoles)
	})
}

func TestNewService(T *testing.T) {
	T.Parallel()

	build := func(t *testing.T, cfg *Config, opts ...Option) (*signin.Service, error) {
		t.Helper()

		return NewService(t.Context(), cfg, testDBClient(t), &identitymock.StoreMock{},
			argon2.NewArgon2Authenticator(), stubIssuer{}, opts...)
	}

	scope := tenancy.Of("tenant")

	T.Run("a zero config leaves every optional door refusing as unconfigured", func(t *testing.T) {
		t.Parallel()

		svc, err := build(t, &Config{})
		must.NoError(t, err)

		test.ErrorIs(t, svc.SignOut(t.Context(), scope, ""), signin.ErrRefreshTokensNotConfigured)
		test.ErrorIs(t, svc.RequestMagicLink(t.Context(), scope, ""), signin.ErrMagicLinksNotConfigured)

		_, err = svc.RecoveryCodesRemaining(t.Context(), scope, "user")
		test.ErrorIs(t, err, signin.ErrRecoveryCodesNotConfigured)

		_, err = svc.Register(t.Context(), scope, &signin.Registration{User: &identity.User{}})
		test.ErrorIs(t, err, signin.ErrRegistrationNotConfigured)
	})

	T.Run("each present block switches its door on", func(t *testing.T) {
		t.Parallel()

		svc, err := build(t, everyBlock(),
			WithRegistrar(stubRegistrar{}),
			WithMagicLinkMailer(discardingMailer{}),
		)
		must.NoError(t, err)

		// Each door now gets past its configuration check to the argument
		// check behind it, or to the table nobody migrated.
		test.ErrorIs(t, svc.SignOut(t.Context(), scope, ""), signin.ErrEmptyRefreshToken)
		test.ErrorIs(t, svc.RequestMagicLink(t.Context(), scope, ""), signin.ErrEmptyHandle)

		_, err = svc.RecoveryCodesRemaining(t.Context(), scope, "user")
		must.Error(t, err)
		test.False(t, errors.Is(err, signin.ErrRecoveryCodesNotConfigured))
	})

	T.Run("refuses a registration block with no registrar", func(t *testing.T) {
		t.Parallel()

		svc, err := build(t, &Config{Registration: &RegistrationConfig{VerificationLinkTTL: time.Hour}})
		must.Error(t, err)
		test.Nil(t, svc)
		test.StrContains(t, err.Error(), "registrar")
	})

	T.Run("refuses a magic links block with no mailer", func(t *testing.T) {
		t.Parallel()

		svc, err := build(t, &Config{MagicLinks: &MagicLinksConfig{TablePrefix: "ddb"}})
		must.Error(t, err)
		test.Nil(t, svc)
		test.StrContains(t, err.Error(), "mailer")
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		svc, err := build(t, nil)
		test.Error(t, err)
		test.Nil(t, svc)
	})

	T.Run("refuses an invalid config", func(t *testing.T) {
		t.Parallel()

		svc, err := build(t, &Config{SecondFactor: "sometimes"})
		test.Error(t, err)
		test.Nil(t, svc)
	})

	T.Run("refuses a missing authenticator rather than defaulting one", func(t *testing.T) {
		t.Parallel()

		svc, err := NewService(t.Context(), &Config{}, testDBClient(t), &identitymock.StoreMock{}, nil, stubIssuer{})
		test.ErrorIs(t, err, signin.ErrNilAuthenticator)
		test.Nil(t, svc)
	})

	T.Run("refuses a refresh lifetime shorter than the token it replaces", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			TokenTTL:      time.Hour,
			RefreshTokens: &RefreshTokensConfig{TablePrefix: "ddb", TTL: time.Minute, SweepInterval: pointer.To(time.Duration(0))},
		}

		svc, err := build(t, cfg)
		test.ErrorIs(t, err, signin.ErrRefreshTokenTTLTooShort)
		test.Nil(t, svc)
	})

	T.Run("applies explicit service options after the config's", func(t *testing.T) {
		t.Parallel()

		// The config names a refresh lifetime the service would refuse; an
		// explicit option after it replaces the token lifetime that made it too
		// short, which only works if the explicit one is applied last.
		cfg := &Config{
			TokenTTL:      time.Hour,
			RefreshTokens: &RefreshTokensConfig{TablePrefix: "ddb", TTL: time.Minute, SweepInterval: pointer.To(time.Duration(0))},
		}

		svc, err := build(t, cfg, WithServiceOptions(signin.WithTokenTTL(time.Second)))
		must.NoError(t, err)
		test.NotNil(t, svc)
	})
}

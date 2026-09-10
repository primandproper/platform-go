package oauth2serverstorecfg

import (
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The Config was one struct and is now two, split across the tier line, and the
// embedded half is untagged so that env parsing reads straight through it. This
// pins the property that split depends on: every variable an operator sets today
// lands on the same field, at the same name, whichever half declares it.
//
// The environment is supplied to the parser rather than to the process, so the
// test stays parallel-safe and does not read whatever the developer happens to
// have exported.
func TestConfigParsesTheSameVariablesAcrossTheEmbedBoundary(T *testing.T) {
	T.Parallel()

	T.Run("every variable lands on the field it always did", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix: "OAUTH2_",
			Environment: map[string]string{
				// Declared on this struct.
				"OAUTH2_PROVIDER":              "memory",
				"OAUTH2_DATABASE_TABLE_PREFIX": "oauth_",
				// Declared on the embedded oauth2servercfg.Config, and reached
				// through the promotion rather than through names of their own.
				"OAUTH2_ISSUER":                       "https://auth.example",
				"OAUTH2_SCOPES":                       "read,write",
				"OAUTH2_RESOURCES":                    "https://api.example",
				"OAUTH2_ACCESS_TOKEN_TTL":             "42s",
				"OAUTH2_SWEEP_INTERVAL":               "10m",
				"OAUTH2_CLIENT_REGISTRATION_TTL":      "0s",
				"OAUTH2_SERVICE_DOCUMENTATION":        "https://docs.example",
				"OAUTH2_DISABLE_DYNAMIC_REGISTRATION": "true",
			},
		}))

		test.EqOp(t, ProviderMemory, cfg.Provider)
		test.EqOp(t, "oauth_", cfg.Database.TablePrefix)
		test.EqOp(t, "https://auth.example", cfg.Issuer)
		test.Eq(t, []string{"read", "write"}, cfg.Scopes)
		test.Eq(t, []string{"https://api.example"}, cfg.Resources)
		test.EqOp(t, 42*time.Second, cfg.AccessTokenTTL)
		test.EqOp(t, "https://docs.example", cfg.ServiceDocumentation)
		test.True(t, cfg.DisableDynamicRegistration)

		// The two pointers are the fields for which unset and zero are
		// different answers, so what they parsed to is asserted rather than
		// dereferenced past.
		must.NotNil(t, cfg.SweepInterval)
		test.EqOp(t, 10*time.Minute, *cfg.SweepInterval)
		must.NotNil(t, cfg.ClientRegistrationTTL)
		test.EqOp(t, time.Duration(0), *cfg.ClientRegistrationTTL)
	})

	// The provider's default is declared on this struct and is the one variable
	// whose absence means something, so it is asserted rather than assumed.
	T.Run("an unset provider still defaults to the tables", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix:      "OAUTH2_",
			Environment: map[string]string{},
		}))

		test.EqOp(t, ProviderDatabase, cfg.Provider)

		// Unset, not zero: the pointers stay nil until EnsureDefaults reads them.
		test.Nil(t, cfg.SweepInterval)
		test.Nil(t, cfg.ClientRegistrationTTL)
	})

	// Parsing and then validating is the sequence a real load performs, so it is
	// the sequence asserted: `env:",init"` populates the database block whether
	// or not the operator configured one, and the Skip rule is what keeps that
	// from failing a perfectly good memory configuration.
	T.Run("an unconfigured database block does not fail the memory provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix:      "OAUTH2_",
			Environment: map[string]string{"OAUTH2_PROVIDER": "memory"},
		}))

		cfg.EnsureDefaults()
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

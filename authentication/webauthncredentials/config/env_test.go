package webauthncredentialscfg

import (
	"testing"
	"time"

	cachecfg "github.com/primandproper/primitives-go/v2/cache/config"

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
			Prefix: "WEBAUTHN_",
			Environment: map[string]string{
				// Declared on this struct.
				"WEBAUTHN_PROVIDER":              "cache",
				"WEBAUTHN_SWEEP_INTERVAL":        "10m",
				"WEBAUTHN_DATABASE_TABLE_PREFIX": "wa_",
				// Declared on the embedded webauthncfg.Config, and reached
				// through the promotion rather than through names of their own.
				"WEBAUTHN_RP_ID":           "example.com",
				"WEBAUTHN_RP_DISPLAY_NAME": "Example",
				"WEBAUTHN_RP_ORIGINS":      "https://example.com",
				"WEBAUTHN_CACHE_PROVIDER":  cachecfg.ProviderMemory,
			},
		}))

		test.EqOp(t, ProviderCache, cfg.Provider)
		must.NotNil(t, cfg.SweepInterval)
		test.EqOp(t, 10*time.Minute, *cfg.SweepInterval)
		test.EqOp(t, "wa_", cfg.Database.TablePrefix)
		test.EqOp(t, "example.com", cfg.RelyingParty.RPID)
		test.SliceLen(t, 1, cfg.RelyingParty.RPOrigins)
		test.EqOp(t, cachecfg.ProviderMemory, cfg.Cache.Provider)
	})

	// The provider's default is declared on this struct and is the one variable
	// whose absence means something, so it is asserted rather than assumed.
	T.Run("an unset provider still defaults to the table", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix:      "WEBAUTHN_",
			Environment: map[string]string{},
		}))

		test.EqOp(t, ProviderDatabase, cfg.Provider)
	})

	// Parsing and then validating is the sequence a real load performs, so it is
	// the sequence asserted: `env:",init"` populates the sub-config for the
	// provider nobody selected, and the Skip rules are what keep that from
	// failing a perfectly good configuration.
	T.Run("an unconfigured cache block does not fail the database provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix: "WEBAUTHN_",
			Environment: map[string]string{
				"WEBAUTHN_RP_ID":           "example.com",
				"WEBAUTHN_RP_DISPLAY_NAME": "Example",
				"WEBAUTHN_RP_ORIGINS":      "https://example.com",
			},
		}))

		cfg.EnsureDefaults()
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

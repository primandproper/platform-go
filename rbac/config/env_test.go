package rbaccfg

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
// service.Config nests this one at envPrefix:"AUTHORIZATION_", so that is the
// prefix parsed under. The environment is supplied to the parser rather than to
// the process, so the test stays parallel-safe and does not read whatever the
// developer happens to have exported.
func TestConfigParsesTheSameVariablesAcrossTheEmbedBoundary(T *testing.T) {
	T.Parallel()

	T.Run("every variable lands on the field it always did", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix: "AUTHORIZATION_",
			Environment: map[string]string{
				// Declared on this struct.
				"AUTHORIZATION_PROVIDER": "database",
				// Declared on the embedded authorizationcfg.Config, and reached
				// through the promotion rather than through a name of its own.
				"AUTHORIZATION_CACHE_TTL": "90s",
				// Declared on the store's own Config, under this struct's
				// nested prefix.
				"AUTHORIZATION_DATABASE_TABLE_PREFIX": "authz_",
			},
		}))

		test.EqOp(t, "database", cfg.Provider)
		test.EqOp(t, 90*time.Second, cfg.CacheTTL)
		must.NotNil(t, cfg.Database)
		test.EqOp(t, "authz_", cfg.Database.TablePrefix)
	})

	// The whole config still parses under an application-wide prefix, which is
	// how config.WithPrefix composes with the one service.Config sets.
	T.Run("composes with an outer prefix", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix: "MYAPP_AUTHORIZATION_",
			Environment: map[string]string{
				"MYAPP_AUTHORIZATION_CACHE_TTL":             "30s",
				"MYAPP_AUTHORIZATION_DATABASE_TABLE_PREFIX": "authz_",
			},
		}))

		test.EqOp(t, 30*time.Second, cfg.CacheTTL)
		must.NotNil(t, cfg.Database)
		test.EqOp(t, "authz_", cfg.Database.TablePrefix)
	})

	// The `env:",init"` on the Database block allocates it whether or not the
	// operator configured one, which is what ValidateWithContext's ZeroToNil
	// undoes. Parsing and then validating is the sequence a real load performs,
	// so it is the sequence asserted.
	T.Run("an unconfigured database block does not fail the static provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix:      "AUTHORIZATION_",
			Environment: map[string]string{"AUTHORIZATION_PROVIDER": "static"},
		}))

		must.NotNil(t, cfg.Database)
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

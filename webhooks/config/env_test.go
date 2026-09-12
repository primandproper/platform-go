package webhookscfg

import (
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The two nested prefixes here were HTTP_ and CIRCUIT_BREAKER_, which are not
// what service.Config spells for the same two configs at the top level. This
// pins what they are now, in the form an operator meets them: whole variable
// names, under the WEBHOOKS_ prefix service.Config nests this config at.
//
// The environment is handed to the parser rather than exported into the process,
// so the test stays parallel-safe and reads nothing the developer happens to
// have set.
func TestNestedPrefixesAreSpelledAsTheTopLevelSpellsThem(T *testing.T) {
	T.Parallel()

	T.Run("every variable lands on the field it names", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix: "WEBHOOKS_",
			Environment: map[string]string{
				"WEBHOOKS_TABLE_PREFIX":                "wh_",
				"WEBHOOKS_HTTP_CLIENT_TIMEOUT":         "17s",
				"WEBHOOKS_HTTP_CLIENT_MAX_IDLE_CONNS":  "11",
				"WEBHOOKS_CIRCUIT_BREAKING_NAME":       "deliveries",
				"WEBHOOKS_CIRCUIT_BREAKING_ERROR_RATE": "0.25",
			},
		}))

		test.EqOp(t, "wh_", cfg.TablePrefix)
		test.EqOp(t, 17*time.Second, cfg.HTTPClient.Timeout)
		test.EqOp(t, 11, cfg.HTTPClient.MaxIdleConns)
		test.EqOp(t, "deliveries", cfg.CircuitBreaker.Name)
		test.EqOp(t, 0.25, cfg.CircuitBreaker.ErrorRate)
	})

	// The names they were nested under read as nothing now. Env parsing ignores
	// a variable no field claims, so the failure this half describes is silent:
	// a deployment that still sets the old name boots on defaults and looks
	// configured. It is here so that the rename cannot be half-undone.
	T.Run("the names they used to carry are claimed by no field", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix: "WEBHOOKS_",
			Environment: map[string]string{
				"WEBHOOKS_HTTP_TIMEOUT":               "17s",
				"WEBHOOKS_CIRCUIT_BREAKER_NAME":       "deliveries",
				"WEBHOOKS_CIRCUIT_BREAKER_ERROR_RATE": "0.25",
			},
		}))

		test.EqOp(t, time.Duration(0), cfg.HTTPClient.Timeout)
		test.EqOp(t, "", cfg.CircuitBreaker.Name)
		test.EqOp(t, float64(0), cfg.CircuitBreaker.ErrorRate)
	})

	// The whole config still parses under an application-wide prefix, which is
	// how a caller's own prefix composes with the one service.Config sets.
	T.Run("composes with an outer prefix", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix: "MYAPP_WEBHOOKS_",
			Environment: map[string]string{
				"MYAPP_WEBHOOKS_HTTP_CLIENT_TIMEOUT":   "5s",
				"MYAPP_WEBHOOKS_CIRCUIT_BREAKING_NAME": "deliveries",
			},
		}))

		test.EqOp(t, 5*time.Second, cfg.HTTPClient.Timeout)
		test.EqOp(t, "deliveries", cfg.CircuitBreaker.Name)
	})
}

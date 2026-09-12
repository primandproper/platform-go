package outboxcfg

import (
	"testing"

	messagequeuecfg "github.com/primandproper/primitives-go/v2/messagequeue/config"

	"github.com/caarlos0/env/v11"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// Config.Queue was a whole messagequeuecfg.Config and is now its publisher half,
// because a relay publishes and never consumes. These pin what an operator meets
// as a result: the publisher's settings lose the PUBLISHER_ segment, and the
// consumer's stop being variables at all.
//
// service.Config nests this config at envPrefix:"OUTBOX_", so that is the prefix
// parsed under. The environment is supplied to the parser rather than to the
// process, so the test stays parallel-safe and does not read whatever the
// developer happens to have exported.
func TestConfigParsesThePublisherHalfAlone(T *testing.T) {
	T.Parallel()

	T.Run("the publisher's settings sit directly under the queue prefix", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix: "OUTBOX_",
			Environment: map[string]string{
				"OUTBOX_QUEUE_PROVIDER":              "redis",
				"OUTBOX_QUEUE_REDIS_QUEUE_ADDRESSES": "localhost:6379",
				"OUTBOX_TABLE_PREFIX":                "example",
			},
		}))

		test.EqOp(t, messagequeuecfg.ProviderRedis, cfg.Queue.Provider)
		test.Eq(t, []string{"localhost:6379"}, cfg.Queue.Redis.QueueAddresses)
		test.EqOp(t, "example", cfg.Relay.TablePrefix)
	})

	// The name this config answered to before. It is worth pinning as an
	// absence: the rename is a deployment break, and a variable that still
	// parsed into a field nobody reads would be the same silent failure in the
	// other direction.
	T.Run("the old publisher-nested name lands on nothing", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix: "OUTBOX_",
			Environment: map[string]string{
				"OUTBOX_QUEUE_PUBLISHER_PROVIDER": "redis",
			},
		}))

		var unset Config
		test.EqOp(t, unset.Queue.Provider, cfg.Queue.Provider)
	})

	// The point of the narrowing. A relay consumes nothing, so there is no
	// consumer for an operator to configure, and the settings that used to
	// parse into one — a provider and four providers' worth of connection
	// details beneath it, read by nothing and validated by nothing — are gone
	// rather than inert.
	T.Run("there is no consumer to configure", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix: "OUTBOX_",
			Environment: map[string]string{
				"OUTBOX_QUEUE_CONSUMER_PROVIDER":              "redis",
				"OUTBOX_QUEUE_CONSUMER_REDIS_QUEUE_ADDRESSES": "localhost:6379",
			},
		}))

		var unset Config
		test.EqOp(t, unset.Queue.Provider, cfg.Queue.Provider)
	})

	// The whole config still parses under an application-wide prefix, which is
	// how config.WithPrefix composes with the one service.Config sets.
	T.Run("composes with an outer prefix", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{
			Prefix: "MYAPP_OUTBOX_",
			Environment: map[string]string{
				"MYAPP_OUTBOX_QUEUE_PROVIDER": "noop",
			},
		}))

		test.EqOp(t, messagequeuecfg.ProviderNoop, cfg.Queue.Provider)
	})
}

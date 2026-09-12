package service

import (
	"reflect"
	"testing"
	"time"

	outboxcfg "github.com/primandproper/platform-go/v14/outbox/config"

	"github.com/primandproper/primitives-go/v2/encoding"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	messagequeuecfg "github.com/primandproper/primitives-go/v2/messagequeue/config"
	secretscfg "github.com/primandproper/primitives-go/v2/secrets/config"

	"github.com/caarlos0/env/v11"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// subConfigFields returns the name of every pointer sub-config field on Config,
// so the tests below cover fields added after they were written.
func subConfigFields(t *testing.T) []string {
	t.Helper()

	var names []string
	for field := range reflect.TypeFor[Config]().Fields() {
		if field.IsExported() && field.Type.Kind() == reflect.Pointer {
			names = append(names, field.Name)
		}
	}

	must.SliceNotEmpty(t, names)

	return names
}

// present reports the sub-configs that survived normalization.
func present(t *testing.T, cfg *Config) []string {
	t.Helper()

	v := reflect.ValueOf(cfg).Elem()

	var names []string
	for _, name := range subConfigFields(t) {
		if !v.FieldByName(name).IsNil() {
			names = append(names, name)
		}
	}

	return names
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("a service configuring nothing is made of nothing", func(t *testing.T) {
		t.Parallel()

		// The load-bearing case. `env:",init"` allocates every sub-config
		// during parsing, and several of them are non-zero the moment they are
		// allocated — nested ",init" pointers, envDefault values — so without
		// normalization a service that configured nothing would be composed of
		// everything.
		cfg := &Config{Name: "example"}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{Environment: map[string]string{}}))
		must.SliceLen(t, len(subConfigFields(t)), present(t, cfg))

		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		test.SliceEmpty(t, present(t, cfg))
	})

	T.Run("keeps and validates the subsystems the environment configured", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Name: "example"}
		must.NoError(t, env.ParseWithOptions(cfg, env.Options{Environment: map[string]string{
			"DATABASE_PROVIDER":                 "sqlite",
			"DATABASE_READ_CONNECTION_DATABASE": "test.db",
			"HTTP_SERVER_PORT":                  "8080",
		}}))

		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		test.Eq(t, []string{"Database", "HTTPServer"}, present(t, cfg))
		must.NotNil(t, cfg.Database)
		test.EqOp(t, "sqlite", cfg.Database.Provider)
	})

	T.Run("keeps a subsystem the caller assigned rather than parsed", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Name: "example", Encoding: &encoding.Config{ContentType: "application/json"}}

		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		test.Eq(t, []string{"Encoding"}, present(t, cfg))
	})

	T.Run("reports a present subsystem's own validation failure", func(t *testing.T) {
		t.Parallel()

		// A secrets block naming a provider nobody implements is a configured
		// subsystem that is wrong, which is the sub-config's error to report —
		// not something normalization should quietly turn into an absence.
		cfg := &Config{Name: "example", Secrets: &secretscfg.Config{Provider: "nonesuch"}}

		// ozzo collects field errors into its own map rather than wrapping, so
		// the sentinel survives as text rather than as an errors.Is match.
		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), platformerrors.ErrUnknownProvider.Error())
		test.StrContains(t, err.Error(), "secrets")
	})

	T.Run("applies a present subsystem's own defaults before validating it", func(t *testing.T) {
		t.Parallel()

		// A composition root validates sub-configs it did not construct, and
		// every constructor in this module defaults before it validates. Skip
		// that here and an outbox configured from the environment is rejected
		// for seven knobs the library has documented defaults for.
		cfg := &Config{
			Name: "example",
			Outbox: &outboxcfg.Config{
				// One publisher, because that is all a relay has. The
				// consumer this used to name alongside it was read by
				// nothing.
				Queue: messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderNoop},
			},
		}

		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		must.NotNil(t, cfg.Outbox)
		test.Positive(t, cfg.Outbox.Relay.BatchSize)
	})

	T.Run("defaults the shutdown budget rather than failing on it", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Name: "example"}

		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		test.EqOp(t, DefaultShutdownTimeout, cfg.ShutdownTimeout)
	})

	T.Run("rejects a negative shutdown budget", func(t *testing.T) {
		t.Parallel()

		// Zero is an operator who said nothing and gets the default; negative
		// is an operator who said something impossible.
		cfg := &Config{Name: "example", ShutdownTimeout: -time.Second}

		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "shutdownTimeout")
	})

	T.Run("requires a name", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		// The field as ozzo names it, from the json tag. Asserting on "Name"
		// used to pass on a coincidence: the logging pillar required a
		// serviceName of every config, so every failure mentioned one.
		test.StrContains(t, err.Error(), "name: cannot be blank")
	})
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("names the observability pillars after the service", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Name: "example"}
		cfg.EnsureDefaults()

		test.EqOp(t, "example", cfg.Observability.Logging.ServiceName)
		test.EqOp(t, "example", cfg.Observability.Metrics.ServiceName)
		test.EqOp(t, "example", cfg.Observability.Tracing.ServiceName)
		test.EqOp(t, "example", cfg.Observability.Profiling.ServiceName)
	})

	T.Run("leaves a pillar that named itself alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Name: "example"}
		cfg.Observability.Tracing.ServiceName = "example-traces"
		cfg.EnsureDefaults()

		test.EqOp(t, "example-traces", cfg.Observability.Tracing.ServiceName)
		test.EqOp(t, "example", cfg.Observability.Logging.ServiceName)
	})

	T.Run("has nothing to propagate without a name", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, "", cfg.Observability.Logging.ServiceName)
	})
}

// TestConfig_AsyncNotificationsEnvironmentComposesAcrossTheModuleBoundary
// asserts that the variables an operator already sets for async notifications
// still reach the config now that the package they configure is primitives-go's.
//
// `caarlos0/env` reads struct tags and does not care which module declared the
// struct, so the composition of ASYNC_NOTIFICATIONS_ with the provider's own
// PUSHER_ is unaffected by the move on paper. It is pinned here anyway because
// this is the one kind of breakage the move could cause that no build would
// report: a prefix that stopped composing hands the operator the default and no
// error, in a deployment that looks configured. Both halves are spelled out, so
// the pair is checked end to end rather than one level at a time.
func TestConfig_AsyncNotificationsEnvironmentComposesAcrossTheModuleBoundary(t *testing.T) {
	t.Parallel()

	cfg := &Config{Name: "example"}
	must.NoError(t, env.ParseWithOptions(cfg, env.Options{Environment: map[string]string{
		"ASYNC_NOTIFICATIONS_PROVIDER":       "pusher",
		"ASYNC_NOTIFICATIONS_TOPOLOGY":       "fleet",
		"ASYNC_NOTIFICATIONS_PUSHER_APP_ID":  "app",
		"ASYNC_NOTIFICATIONS_PUSHER_KEY":     "key",
		"ASYNC_NOTIFICATIONS_PUSHER_SECRET":  "secret",
		"ASYNC_NOTIFICATIONS_PUSHER_CLUSTER": "us-east-1",
	}}))

	must.NoError(t, cfg.ValidateWithContext(t.Context()))

	test.Eq(t, []string{"AsyncNotifications"}, present(t, cfg))

	must.NotNil(t, cfg.AsyncNotifications)
	test.EqOp(t, "pusher", cfg.AsyncNotifications.Provider)
	test.EqOp(t, "fleet", cfg.AsyncNotifications.Topology)

	must.NotNil(t, cfg.AsyncNotifications.Pusher)
	test.EqOp(t, "app", cfg.AsyncNotifications.Pusher.AppID)
	test.EqOp(t, "key", cfg.AsyncNotifications.Pusher.Key)
	test.EqOp(t, "secret", cfg.AsyncNotifications.Pusher.Secret)
	test.EqOp(t, "us-east-1", cfg.AsyncNotifications.Pusher.Cluster)
}

// TestConfig_TheSixLateSubsystemsParseAtTheirOwnPrefix pins the environment
// prefix each of the six subsystems wired last reads at.
//
// A prefix is decided once. It is the name an operator writes in a manifest and
// the name every deployment keeps writing afterwards, so changing one later is a
// breaking change to something no Go compiler can see — which makes the day the
// field lands the only cheap moment to get it right, and this the test that says
// what was chosen.
//
// Two of the six read through an embedded struct rather than a field of their
// own. WEBAUTHN_RP_ID and OAUTH2_SERVER_ISSUER are declared on primitives-go's
// half of a config split across the module boundary, promoted into this module's
// half by an untagged embed, and reached here at a third prefix this struct
// adds. Nothing about that composition fails loudly if it stops working: a
// prefix that stops composing hands the operator the library's default and no
// error, in a deployment that looks configured. So both are spelled out end to
// end rather than assumed from the shorter ones.
func TestConfig_TheSixLateSubsystemsParseAtTheirOwnPrefix(t *testing.T) {
	t.Parallel()

	cfg := &Config{Name: "example"}
	must.NoError(t, env.ParseWithOptions(cfg, env.Options{Environment: map[string]string{
		"ENTITLEMENTS_CHECKER_CACHE_PREFIX": "plans:",
		"INBOUND_WEBHOOKS_PROVIDER":         "github",
		"INBOUND_WEBHOOKS_TOPIC":            "inbound",
		"INBOUND_WEBHOOKS_SECRET":           "hunter2",
		"LINKS_TOKEN_BYTES":                 "48",
		"MEDIA_REGISTRY_TABLE_PREFIX":       "media",
		// Declared on oauth2servercfg.Config and webauthn.Config respectively,
		// and reached through two levels of embedding and nesting.
		"OAUTH2_SERVER_ISSUER":     "https://example.com",
		"WEBAUTHN_RP_ID":           "example.com",
		"WEBAUTHN_RP_DISPLAY_NAME": "Example",
		"WEBAUTHN_RP_ORIGINS":      "https://example.com",
	}}))

	must.NoError(t, cfg.ValidateWithContext(t.Context()))

	test.Eq(t, []string{
		"Entitlements",
		"InboundWebhooks",
		"Links",
		"MediaRegistry",
		"OAuth2Server",
		"WebAuthn",
	}, present(t, cfg))

	must.NotNil(t, cfg.Entitlements)
	test.EqOp(t, "plans:", cfg.Entitlements.Checker.CachePrefix)

	must.NotNil(t, cfg.InboundWebhooks)
	test.EqOp(t, "github", cfg.InboundWebhooks.Provider)
	test.EqOp(t, "inbound", cfg.InboundWebhooks.Topic)

	must.NotNil(t, cfg.Links)
	test.EqOp(t, 48, cfg.Links.TokenBytes)

	must.NotNil(t, cfg.MediaRegistry)
	test.EqOp(t, "media", cfg.MediaRegistry.TablePrefix)

	must.NotNil(t, cfg.OAuth2Server)
	test.EqOp(t, "https://example.com", cfg.OAuth2Server.Issuer)

	must.NotNil(t, cfg.WebAuthn)
	test.EqOp(t, "example.com", cfg.WebAuthn.RelyingParty.RPID)
	test.SliceLen(t, 1, cfg.WebAuthn.RelyingParty.RPOrigins)
}

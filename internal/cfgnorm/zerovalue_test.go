package cfgnorm_test

import (
	"context"
	"testing"

	analyticscfg "github.com/primandproper/platform-go/v13/analytics/config"
	auditcfg "github.com/primandproper/platform-go/v13/audit/config"
	tokenscfg "github.com/primandproper/platform-go/v13/authentication/tokens/config"
	authorizationcfg "github.com/primandproper/platform-go/v13/authorization/config"
	cachecfg "github.com/primandproper/platform-go/v13/cache/config"
	capitalismcfg "github.com/primandproper/platform-go/v13/capitalism/config"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	partitionedcfg "github.com/primandproper/platform-go/v13/circuitbreaking/partitioned/config"
	encryptioncfg "github.com/primandproper/platform-go/v13/cryptography/encryption/config"
	shreddingcfg "github.com/primandproper/platform-go/v13/cryptography/shredding/config"
	databasecfg "github.com/primandproper/platform-go/v13/database/config"
	dataprivacycfg "github.com/primandproper/platform-go/v13/dataprivacy/config"
	distributedlockcfg "github.com/primandproper/platform-go/v13/distributedlock/config"
	emailcfg "github.com/primandproper/platform-go/v13/email/config"
	embeddingscfg "github.com/primandproper/platform-go/v13/embeddings/config"
	entitlementscfg "github.com/primandproper/platform-go/v13/entitlements/config"
	eventstreamcfg "github.com/primandproper/platform-go/v13/eventstream/config"
	featureflagscfg "github.com/primandproper/platform-go/v13/featureflags/config"
	idempotencycfg "github.com/primandproper/platform-go/v13/idempotency/config"
	jobscfg "github.com/primandproper/platform-go/v13/jobs/config"
	linkscfg "github.com/primandproper/platform-go/v13/links/config"
	llmcfg "github.com/primandproper/platform-go/v13/llm/config"
	messagequeuecfg "github.com/primandproper/platform-go/v13/messagequeue/config"
	meteringcfg "github.com/primandproper/platform-go/v13/metering/config"
	asyncnotifcfg "github.com/primandproper/platform-go/v13/notifications/async/config"
	mobilecfg "github.com/primandproper/platform-go/v13/notifications/mobile/config"
	"github.com/primandproper/platform-go/v13/observability"
	loggingcfg "github.com/primandproper/platform-go/v13/observability/logging/config"
	metricscfg "github.com/primandproper/platform-go/v13/observability/metrics/config"
	profilingcfg "github.com/primandproper/platform-go/v13/observability/profiling/config"
	tracingcfg "github.com/primandproper/platform-go/v13/observability/tracing/config"
	operationscfg "github.com/primandproper/platform-go/v13/operations/config"
	outboxcfg "github.com/primandproper/platform-go/v13/outbox/config"
	ratelimitingcfg "github.com/primandproper/platform-go/v13/ratelimiting/config"
	retentioncfg "github.com/primandproper/platform-go/v13/retention/config"
	routingcfg "github.com/primandproper/platform-go/v13/routing/config"
	sagacfg "github.com/primandproper/platform-go/v13/saga/config"
	textsearchcfg "github.com/primandproper/platform-go/v13/search/text/config"
	vectorsearchcfg "github.com/primandproper/platform-go/v13/search/vector/config"
	secretscfg "github.com/primandproper/platform-go/v13/secrets/config"
	sessionscfg "github.com/primandproper/platform-go/v13/sessions/config"
	timerscfg "github.com/primandproper/platform-go/v13/timers/config"
	webhookscfg "github.com/primandproper/platform-go/v13/webhooks/config"
	inboundcfg "github.com/primandproper/platform-go/v13/webhooks/inbound/config"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// zeroValued is the shape every config subpackage's root config satisfies:
// validation, and — where its defaults are not expressible as `envDefault:`
// tags — an EnsureDefaults to apply first.
type zeroValued interface {
	ValidateWithContext(context.Context) error
}

// defaulter is the optional half. A config without one has no defaults beyond
// what its struct tags carry.
type defaulter interface{ EnsureDefaults() }

// TestZeroValueConfigIsDecisive asserts, for every config subpackage, that a
// hand-built zero Config either defaults into validity or names the field it
// needs.
//
// The two outcomes are both fine and the third is not: a zero config that
// validates clean and is then refused by its own constructor. That was the
// state of most of this layer — a provider field with no Required rule, or a
// leaf provider's rules made unreachable — and the failure it produced was a
// deployment that passed every check the operator could run and died at boot,
// or worse, ran.
//
// So each case declares which of the two answers it expects, and the invalid
// ones name a field the message must mention. Adding a config subpackage means
// adding a line here and deciding, once, which answer is right for it.
//
// The configs are hand-built rather than env-parsed, which is the case the
// `env:",init"` sweeps above cannot cover: a caller assembling a Config in Go
// never goes through env.Parse, so anything a struct tag would have supplied is
// absent, and the only defaults that run are the ones EnsureDefaults applies.
func TestZeroValueConfigIsDecisive(T *testing.T) {
	T.Parallel()

	cases := []struct {
		cfg zeroValued
		// name is the config subpackage.
		name string
		// needs is a fragment of the message a zero config must report. Empty
		// means the zero config is expected to default into validity instead.
		needs string
		// why records the reason a zero config is a working one, for the cases
		// where it is. Unread by the test; it is the thing worth stating.
		why string
	}{
		{name: "analytics", cfg: &analyticscfg.Config{}, needs: "provider"},
		{name: "audit", cfg: &auditcfg.Config{}, needs: "dialect"},
		{name: "authentication/tokens", cfg: &tokenscfg.Config{}, needs: "provider"},
		{name: "authorization", cfg: &authorizationcfg.Config{}, why: "the static resolver needs no infrastructure and grants nothing"},
		{name: "cache", cfg: &cachecfg.Config{}, needs: "provider"},
		{name: "capitalism", cfg: &capitalismcfg.Config{}, needs: "provider"},
		{name: "circuitbreaking", cfg: &circuitbreakingcfg.Config{}, why: "every threshold has a default"},
		{name: "circuitbreaking/partitioned", cfg: &partitionedcfg.Config{}, why: "it is the base breaker's defaults, per key"},
		{name: "cryptography/encryption", cfg: &encryptioncfg.Config{}, needs: "provider"},
		{name: "cryptography/shredding", cfg: &shreddingcfg.Config{}, why: "shredding defaults to the key store it is handed"},
		{name: "database", cfg: &databasecfg.Config{}, needs: "hostname"},
		{name: "dataprivacy", cfg: &dataprivacycfg.Config{}, needs: "dialect"},
		{name: "distributedlock", cfg: &distributedlockcfg.Config{}, needs: "provider"},
		{name: "email", cfg: &emailcfg.Config{}, needs: "provider"},
		{name: "embeddings", cfg: &embeddingscfg.Config{}, why: "embeddings are optional, and an unset provider is the noop embedder"},
		{name: "entitlements", cfg: &entitlementscfg.Config{}, why: "the checker defaults to entitling nothing"},
		{name: "eventstream", cfg: &eventstreamcfg.Config{}, needs: "provider"},
		{name: "featureflags", cfg: &featureflagscfg.Config{}, needs: "provider"},
		{name: "idempotency", cfg: &idempotencycfg.Config{}, needs: "provider"},
		{name: "jobs/pool", cfg: &jobscfg.PoolConfig{}, needs: "topic"},
		{name: "jobs/scheduler", cfg: &jobscfg.SchedulerConfig{}, needs: "provider"},
		{name: "links", cfg: &linkscfg.Config{}, needs: "provider"},
		{name: "llm", cfg: &llmcfg.Config{}, needs: "provider"},
		{name: "messagequeue", cfg: &messagequeuecfg.Config{}, needs: "provider"},
		{name: "metering", cfg: &meteringcfg.Config{}, why: "metering counts in memory until a store is named"},
		// Unlike the other optional seams below, an unset provider is not the
		// opt-out here: notifying nobody for the life of a process has to be
		// asked for by name.
		{name: "notifications/async", cfg: &asyncnotifcfg.Config{}, needs: "provider"},
		{name: "notifications/mobile", cfg: &mobilecfg.Config{}, needs: "provider"},
		{name: "observability", cfg: &observability.Config{}, why: "every pillar's unset provider is its documented opt-out"},
		{name: "observability/logging", cfg: &loggingcfg.Config{}, why: "an unset provider logs nowhere, which is the opt-out"},
		{name: "observability/metrics", cfg: &metricscfg.Config{}, why: "an unset provider records nothing, which is the opt-out"},
		{name: "observability/profiling", cfg: &profilingcfg.Config{}, why: "an unset provider profiles nothing, which is the opt-out"},
		{name: "observability/tracing", cfg: &tracingcfg.Config{}, why: "an unset provider traces nowhere, which is the opt-out"},
		{name: "operations", cfg: &operationscfg.Config{}, why: "every interval and worker count has a default"},
		{name: "outbox", cfg: &outboxcfg.Config{}, why: "the relay's batch size and intervals all have defaults"},
		{name: "ratelimiting", cfg: &ratelimitingcfg.Config{}, needs: "provider"},
		{name: "retention", cfg: &retentioncfg.Config{}, why: "the sweeper's interval and batch size have defaults"},
		{name: "routing", cfg: &routingcfg.Config{}, needs: "provider"},
		{name: "saga", cfg: &sagacfg.Config{}, why: "the worker's poll interval and backoff have defaults"},
		{name: "search/text", cfg: &textsearchcfg.Config{}, needs: "provider"},
		{name: "search/vector", cfg: &vectorsearchcfg.Config{}, needs: "provider"},
		{name: "secrets", cfg: &secretscfg.Config{}, why: "an unset provider reads secrets from the environment"},
		{name: "sessions", cfg: &sessionscfg.Config{}, needs: "provider"},
		{name: "timers", cfg: &timerscfg.Config{}, needs: "name"},
		{name: "webhooks", cfg: &webhookscfg.Config{}, why: "the sender's worker, client and breaker all have defaults"},
		{name: "webhooks/inbound", cfg: &inboundcfg.Config{}, needs: "provider"},
	}

	for _, tc := range cases {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if d, ok := tc.cfg.(defaulter); ok {
				d.EnsureDefaults()
			}

			err := tc.cfg.ValidateWithContext(t.Context())

			if tc.needs == "" {
				test.NoError(t, err, test.Sprintf("%s: a zero config was expected to default into validity (%s)", tc.name, tc.why))

				return
			}

			must.Error(t, err, must.Sprintf("%s: a zero config validated clean, so nothing will report the %q it needs until construction", tc.name, tc.needs))
			test.StrContains(t, err.Error(), tc.needs,
				test.Sprintf("%s: a zero config was refused, but not for the field it needs", tc.name))
		})
	}
}

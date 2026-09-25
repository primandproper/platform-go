package configroster_test

import (
	"context"
	"testing"

	auditcfg "github.com/primandproper/platform-go/v14/audit/config"
	oauth2clientscfg "github.com/primandproper/platform-go/v14/authentication/oauth2clients/config"
	oauth2serverstorecfg "github.com/primandproper/platform-go/v14/authentication/oauth2serverstore/config"
	passwordresetcfg "github.com/primandproper/platform-go/v14/authentication/passwordreset/config"
	signincfg "github.com/primandproper/platform-go/v14/authentication/signin/config"
	webauthnsessionscfg "github.com/primandproper/platform-go/v14/authentication/webauthnsessions/config"
	billingcfg "github.com/primandproper/platform-go/v14/billing/config"
	commentscfg "github.com/primandproper/platform-go/v14/comments/config"
	dataprivacycfg "github.com/primandproper/platform-go/v14/dataprivacy/config"
	entitlementscfg "github.com/primandproper/platform-go/v14/entitlements/config"
	identitycfg "github.com/primandproper/platform-go/v14/identity/config"
	issuereportscfg "github.com/primandproper/platform-go/v14/issuereports/config"
	linkscfg "github.com/primandproper/platform-go/v14/links/config"
	mediaregistrycfg "github.com/primandproper/platform-go/v14/mediaregistry/config"
	meteringcfg "github.com/primandproper/platform-go/v14/metering/config"
	notificationscfg "github.com/primandproper/platform-go/v14/notifications/config"
	operationscfg "github.com/primandproper/platform-go/v14/operations/config"
	outboxcfg "github.com/primandproper/platform-go/v14/outbox/config"
	rbaccfg "github.com/primandproper/platform-go/v14/rbac/config"
	retentioncfg "github.com/primandproper/platform-go/v14/retention/config"
	sagacfg "github.com/primandproper/platform-go/v14/saga/config"
	sessionscfg "github.com/primandproper/platform-go/v14/sessions/config"
	settingscfg "github.com/primandproper/platform-go/v14/settings/config"
	shreddingcfg "github.com/primandproper/platform-go/v14/shredding/config"
	timerscfg "github.com/primandproper/platform-go/v14/timers/config"
	waitlistscfg "github.com/primandproper/platform-go/v14/waitlists/config"
	webhookscfg "github.com/primandproper/platform-go/v14/webhooks/config"
	"github.com/primandproper/platform-go/v14/workqueue"

	analyticscfg "github.com/primandproper/primitives-go/v2/analytics/config"
	tokenscfg "github.com/primandproper/primitives-go/v2/authentication/tokens/config"
	cachecfg "github.com/primandproper/primitives-go/v2/cache/config"
	capitalismcfg "github.com/primandproper/primitives-go/v2/capitalism/config"
	circuitbreakingcfg "github.com/primandproper/primitives-go/v2/circuitbreaking/config"
	partitionedcfg "github.com/primandproper/primitives-go/v2/circuitbreaking/partitioned/config"
	encryptioncfg "github.com/primandproper/primitives-go/v2/cryptography/encryption/config"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	distributedlockcfg "github.com/primandproper/primitives-go/v2/distributedlock/config"
	emailcfg "github.com/primandproper/primitives-go/v2/email/config"
	embeddingscfg "github.com/primandproper/primitives-go/v2/embeddings/config"
	eventstreamcfg "github.com/primandproper/primitives-go/v2/eventstream/config"
	featureflagscfg "github.com/primandproper/primitives-go/v2/featureflags/config"
	idempotencycfg "github.com/primandproper/primitives-go/v2/idempotency/config"
	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"
	llmcfg "github.com/primandproper/primitives-go/v2/llm/config"
	messagequeuecfg "github.com/primandproper/primitives-go/v2/messagequeue/config"
	asyncnotifcfg "github.com/primandproper/primitives-go/v2/notifications/async/config"
	mobilecfg "github.com/primandproper/primitives-go/v2/notifications/mobile/config"
	"github.com/primandproper/primitives-go/v2/observability"
	loggingcfg "github.com/primandproper/primitives-go/v2/observability/logging/config"
	metricscfg "github.com/primandproper/primitives-go/v2/observability/metrics/config"
	profilingcfg "github.com/primandproper/primitives-go/v2/observability/profiling/config"
	tracingcfg "github.com/primandproper/primitives-go/v2/observability/tracing/config"
	ratelimitingcfg "github.com/primandproper/primitives-go/v2/ratelimiting/config"
	routingcfg "github.com/primandproper/primitives-go/v2/routing/config"
	textsearchcfg "github.com/primandproper/primitives-go/v2/search/text/config"
	vectorsearchcfg "github.com/primandproper/primitives-go/v2/search/vector/config"
	secretscfg "github.com/primandproper/primitives-go/v2/secrets/config"
	inboundcfg "github.com/primandproper/primitives-go/v2/webhooks/inbound/config"

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

// zeroValueCase is one config's answer to the question
// TestZeroValueConfigIsDecisive asks, and exactly one of needs and why is set.
type zeroValueCase struct {
	cfg zeroValued
	// name is the config subpackage.
	name string
	// needs is a fragment of the message a zero config must report. Empty
	// means the zero config is expected to default into validity instead.
	needs string
	// why records the reason a zero config is a working one, for the cases
	// where it is. Unread by TestZeroValueConfigIsDecisive; it is the thing
	// worth stating.
	why string
}

// zeroValueCases is the roster both tests in this file read.
//
// It is a function rather than a var because the test below calls
// EnsureDefaults on every config in it, and a package-level slice would hand
// the second reader a set of configs the first one had already defaulted.
//
// Every config subpackage in this module is named here, which is not a promise
// this list makes about itself — TestEveryConfigPackageIsRostered walks the tree
// and fails on a package that is missing. The primitives-go entries are a
// courtesy in the other direction: that module's coverage is its own to keep,
// and nothing here can walk its tree.
func zeroValueCases() []zeroValueCase {
	return []zeroValueCase{
		{name: "analytics", cfg: &analyticscfg.Config{}, needs: "provider"},
		{name: "audit", cfg: &auditcfg.Config{}, needs: "dialect"},
		{name: "authentication/tokens", cfg: &tokenscfg.Config{}, needs: "provider"},
		{name: "authentication/oauth2clients", cfg: &oauth2clientscfg.Config{}, why: "the table prefix is the only field, and which clients exist is rows an operator or a person creates rather than environment"},
		// It is decisive for the store, which is what a zero config builds.
		// What it still cannot do is serve: NewServer refuses an empty issuer,
		// and that is checked in oauth2server.NewServer rather than here
		// because the issuer is the embedded primitive config's field and a
		// deployment wiring only the store has none to name.
		{name: "authentication/oauth2serverstore", cfg: &oauth2serverstorecfg.Config{}, why: "the provider defaults to database, and the store under it reads its tables from a prefix that defaults too"},
		// Decisive for the config and not for the service: the mailer and the
		// authenticator are the application's, and RegisterService reports a
		// missing one when it is invoked rather than here.
		{name: "authentication/passwordreset", cfg: &passwordresetcfg.Config{}, why: "the prefix, the token lifetime, the request floor and the sweep interval all default"},
		// The zero config is decisive for the same reason passwordreset's is:
		// the authenticator and the token issuer are resolved from the injector
		// when the service is invoked, not checked here.
		{name: "authentication/signin", cfg: &signincfg.Config{}, why: "rotation, recovery codes and registration are on at their defaults, the magic link door is off until its block is present, and the lifetimes default"},
		{name: "authentication/webauthnsessions", cfg: &webauthnsessionscfg.Config{}, needs: "rpID"},
		{name: "rbac", cfg: &rbaccfg.Config{}, why: "the static resolver needs no infrastructure and grants nothing"},
		{name: "billing", cfg: &billingcfg.Config{}, why: "the table prefix is the only field, and what a deployment sells is rows rather than environment"},
		{name: "cache", cfg: &cachecfg.Config{}, needs: "provider"},
		{name: "capitalism", cfg: &capitalismcfg.Config{}, needs: "provider"},
		{name: "circuitbreaking", cfg: &circuitbreakingcfg.Config{}, why: "every threshold has a default"},
		{name: "circuitbreaking/partitioned", cfg: &partitionedcfg.Config{}, why: "it is the base breaker's defaults, per key"},
		// The target catalog is not configuration and so is not checked here: a
		// store with no targets accepts no writes, which comments.NewSQLStore
		// rules is a wiring mistake rather than a config one.
		{name: "comments", cfg: &commentscfg.Config{}, why: "the table prefix is the only field and it defaults"},
		{name: "cryptography/encryption", cfg: &encryptioncfg.Config{}, needs: "provider"},
		{name: "shredding", cfg: &shreddingcfg.Config{}, why: "shredding defaults to the key store it is handed"},
		{name: "database", cfg: &databasecfg.Config{}, needs: "hostname"},
		{name: "dataprivacy", cfg: &dataprivacycfg.Config{}, needs: "dialect"},
		{name: "distributedlock", cfg: &distributedlockcfg.Config{}, needs: "provider"},
		{name: "email", cfg: &emailcfg.Config{}, needs: "provider"},
		{name: "embeddings", cfg: &embeddingscfg.Config{}, why: "embeddings are optional, and an unset provider is the noop embedder"},
		{name: "entitlements", cfg: &entitlementscfg.Config{}, why: "the checker defaults to entitling nothing"},
		{name: "eventstream", cfg: &eventstreamcfg.Config{}, needs: "provider"},
		{name: "featureflags", cfg: &featureflagscfg.Config{}, needs: "provider"},
		{name: "idempotency", cfg: &idempotencycfg.Config{}, needs: "provider"},
		{name: "identity", cfg: &identitycfg.Config{}, why: "the table prefix defaults, and both invitation lifetimes default to the pair identitygrpc already enforces against each other"},
		{name: "issuereports", cfg: &issuereportscfg.Config{}, why: "the table prefix is the only field and it defaults"},
		{name: "jobs/pool", cfg: &jobscfg.PoolConfig{}, needs: "topic"},
		{name: "jobs/scheduler", cfg: &jobscfg.SchedulerConfig{}, needs: "provider"},
		// It needed a provider until there was only one store. What a zero
		// config still cannot do is mint: NewMinter refuses a registry with no
		// actions, which is checked there rather than here because the actions
		// a caller passes as options are only visible at that point.
		{name: "links", cfg: &linkscfg.Config{}, why: "the one store reads its table from the client, and every other field has a default"},
		{name: "llm", cfg: &llmcfg.Config{}, needs: "provider"},
		{name: "mediaregistry", cfg: &mediaregistrycfg.Config{}, why: "the table prefix is the only field, and the bucket the bytes go to is not this package's to configure"},
		{name: "messagequeue", cfg: &messagequeuecfg.Config{}, needs: "provider"},
		{name: "metering", cfg: &meteringcfg.Config{}, why: "metering counts in memory until a store is named"},
		// The one config subpackage in this module with no EnsureDefaults, and
		// deliberately: its one field's default is the empty prefix, so an
		// unset field already is the default and a defaulting step would be
		// assigning "" to "".
		{name: "notifications", cfg: &notificationscfg.Config{}, why: "the table prefix is the only field and unset is its default"},
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
		{name: "settings", cfg: &settingscfg.Config{}, why: "the table prefix is the only field, and which settings exist is rows rather than environment"},
		{name: "timers", cfg: &timerscfg.Config{}, needs: "name"},
		{name: "waitlists", cfg: &waitlistscfg.Config{}, why: "the table prefix is the only field, and which waitlists exist is rows rather than environment"},
		{name: "webhooks", cfg: &webhookscfg.Config{}, why: "the sender's worker, client and breaker all have defaults"},
		{name: "webhooks/inbound", cfg: &inboundcfg.Config{}, needs: "provider"},
		// workqueuecfg declares no Config of its own — it takes this one — so
		// this is the leaf's, which is the config an operator actually sets.
		// See configPackagesTakingALeafConfig.
		{name: "workqueue", cfg: &workqueue.Config{}, needs: "name"},
	}
}

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
// adding a line to zeroValueCases and deciding, once, which answer is right for
// it — which TestEveryConfigPackageIsRostered turns from a comment into a
// failing test.
//
// The configs are hand-built rather than env-parsed, which is the case the
// `env:",init"` sweeps above cannot cover: a caller assembling a Config in Go
// never goes through env.Parse, so anything a struct tag would have supplied is
// absent, and the only defaults that run are the ones EnsureDefaults applies.
func TestZeroValueConfigIsDecisive(T *testing.T) {
	T.Parallel()

	for _, tc := range zeroValueCases() {
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

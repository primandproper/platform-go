package configroster_test

import (
	"context"
	"testing"
	"time"

	asyncably "github.com/primandproper/platform-go/v14/notifications/async/ably"
	asyncnotifcfg "github.com/primandproper/platform-go/v14/notifications/async/config"
	asyncpusher "github.com/primandproper/platform-go/v14/notifications/async/pusher"
	"github.com/primandproper/platform-go/v14/rbac"
	rbaccfg "github.com/primandproper/platform-go/v14/rbac/config"

	analyticscfg "github.com/primandproper/primitives-go/analytics/config"
	analyticsposthog "github.com/primandproper/primitives-go/analytics/posthog"
	analyticssegment "github.com/primandproper/primitives-go/analytics/segment"
	cachecfg "github.com/primandproper/primitives-go/cache/config"
	cacheredis "github.com/primandproper/primitives-go/cache/redis"
	capitalismcfg "github.com/primandproper/primitives-go/capitalism/config"
	capitalismrevenuecat "github.com/primandproper/primitives-go/capitalism/revenuecat"
	capitalismstripe "github.com/primandproper/primitives-go/capitalism/stripe"
	"github.com/primandproper/primitives-go/database/dialect"
	distributedlockcfg "github.com/primandproper/primitives-go/distributedlock/config"
	distributedlockredis "github.com/primandproper/primitives-go/distributedlock/redis"
	emailcfg "github.com/primandproper/primitives-go/email/config"
	emailmailgun "github.com/primandproper/primitives-go/email/mailgun"
	emailmailjet "github.com/primandproper/primitives-go/email/mailjet"
	emailpostmark "github.com/primandproper/primitives-go/email/postmark"
	emailresend "github.com/primandproper/primitives-go/email/resend"
	emailsendgrid "github.com/primandproper/primitives-go/email/sendgrid"
	emailses "github.com/primandproper/primitives-go/email/ses"
	embeddingscohere "github.com/primandproper/primitives-go/embeddings/cohere"
	embeddingscfg "github.com/primandproper/primitives-go/embeddings/config"
	embeddingsopenai "github.com/primandproper/primitives-go/embeddings/openai"
	eventstreamcfg "github.com/primandproper/primitives-go/eventstream/config"
	featureflagscfg "github.com/primandproper/primitives-go/featureflags/config"
	featureflagslaunchdarkly "github.com/primandproper/primitives-go/featureflags/launchdarkly"
	featureflagsposthog "github.com/primandproper/primitives-go/featureflags/posthog"
	llmanthropic "github.com/primandproper/primitives-go/llm/anthropic"
	llmcfg "github.com/primandproper/primitives-go/llm/config"
	llmopenai "github.com/primandproper/primitives-go/llm/openai"
	messagequeuecfg "github.com/primandproper/primitives-go/messagequeue/config"
	"github.com/primandproper/primitives-go/notifications/mobile/apns"
	mobilecfg "github.com/primandproper/primitives-go/notifications/mobile/config"
	loggingcfg "github.com/primandproper/primitives-go/observability/logging/config"
	loggingotelgrpc "github.com/primandproper/primitives-go/observability/logging/otelgrpc"
	metricscfg "github.com/primandproper/primitives-go/observability/metrics/config"
	metricsotelgrpc "github.com/primandproper/primitives-go/observability/metrics/otelgrpc"
	profilingcfg "github.com/primandproper/primitives-go/observability/profiling/config"
	profilingpprof "github.com/primandproper/primitives-go/observability/profiling/pprof"
	profilingpyroscope "github.com/primandproper/primitives-go/observability/profiling/pyroscope"
	tracingcloudtrace "github.com/primandproper/primitives-go/observability/tracing/cloudtrace"
	tracingcfg "github.com/primandproper/primitives-go/observability/tracing/config"
	tracingoteltrace "github.com/primandproper/primitives-go/observability/tracing/oteltrace"
	routingchi "github.com/primandproper/primitives-go/routing/backends/chi"
	routinggin "github.com/primandproper/primitives-go/routing/backends/gin"
	routinghttprouter "github.com/primandproper/primitives-go/routing/backends/httprouter"
	routingstdlib "github.com/primandproper/primitives-go/routing/backends/stdlib"
	routingcfg "github.com/primandproper/primitives-go/routing/config"
	textsearchalgolia "github.com/primandproper/primitives-go/search/text/algolia"
	textsearchcfg "github.com/primandproper/primitives-go/search/text/config"
	textsearchelasticsearch "github.com/primandproper/primitives-go/search/text/elasticsearch"
	vectorsearchcfg "github.com/primandproper/primitives-go/search/vector/config"
	vectorsearchpgvector "github.com/primandproper/primitives-go/search/vector/pgvector"
	vectorsearchqdrant "github.com/primandproper/primitives-go/search/vector/qdrant"
	secretscfg "github.com/primandproper/primitives-go/secrets/config"
	secretsgcp "github.com/primandproper/primitives-go/secrets/gcp"
	secretskubernetes "github.com/primandproper/primitives-go/secrets/kubernetes"
	secretsssm "github.com/primandproper/primitives-go/secrets/ssm"
	"github.com/primandproper/primitives-go/server/http"
	"github.com/primandproper/primitives-go/uploads/objectstorage"

	"github.com/caarlos0/env/v11"
	"github.com/shoenig/test/must"
)

// validatable is any config that validates itself.
type validatable interface {
	ValidateWithContext(context.Context) error
}

// sweepPrefix isolates these sweeps from the ambient environment. Nothing is
// ever set under it, so each config below holds exactly what its case declared
// plus whatever `env:",init"` allocated — which is the thing under test. Without
// a prefix a stray ADDRESSES or REGION in the developer's shell would decide the
// result.
const sweepPrefix = "PLATFORMGOCFGSWEEPTEST_"

// parseEnvironment puts cfg through the environment parsing a real deployment
// does, which is what allocates every pointer sub-config carrying `env:",init"`.
func parseEnvironment(t *testing.T, cfg any) {
	t.Helper()

	must.NoError(t, env.ParseWithOptions(cfg, env.Options{Prefix: sweepPrefix}))
}

// TestSelectedProviderMustBeConfigured asserts that naming a provider and
// configuring nothing is refused, for every config whose provider sub-configs
// carry `env:",init"`.
//
// The tag makes env parsing allocate every provider's sub-config, so a
// `validation.Required` on the pointer stops meaning "the operator supplied
// one": a non-nil pointer to a zero struct satisfies Required. Each case here
// runs env parsing first, which is what a real deployment does, so a rule that
// only holds before parsing does not count as holding.
//
// Anything that validates clean here boots with empty credentials.
//
// Four configs are deliberately absent, because for them a zero sub-config is a
// real configuration rather than an absent one, so this invariant does not apply
// as stated:
//
//   - eventstream's websocket config has no required field at all, so an empty
//     one is a fully defaulted one.
//   - notifications/mobile's FCM config documents an empty CredentialsPath as
//     "use Application Default Credentials", so an empty one is how ADC is
//     requested. Its "at least one of APNs or FCM" rule keys on nil-ness and no
//     longer holds after env parsing; restoring it needs a decision about what an
//     empty FCM block means, not a mechanical fix.
//   - embeddings' ollama config and distributedlock's postgres config have no
//     required field either — ollama defaults to the local daemon, and the
//     postgres locker to the database it is handed. Both were listed here and
//     passed until the sibling sub-configs stopped being enforced, which is to
//     say they were never testing their own provider: the error they asserted on
//     came from openai's and redis' credentials, on a config that had named
//     neither.
func TestSelectedProviderMustBeConfigured(T *testing.T) {
	T.Parallel()

	cases := []struct {
		cfg      validatable
		name     string
		provider string
	}{
		{name: "analytics/segment", provider: analyticscfg.ProviderSegment, cfg: &analyticscfg.Config{Provider: analyticscfg.ProviderSegment}},
		{name: "analytics/posthog", provider: analyticscfg.ProviderPostHog, cfg: &analyticscfg.Config{Provider: analyticscfg.ProviderPostHog}},
		{name: "rbac", provider: rbaccfg.ProviderDatabase, cfg: &rbaccfg.Config{Provider: rbaccfg.ProviderDatabase}},
		{name: "cache/redis", provider: cachecfg.ProviderRedis, cfg: &cachecfg.Config{Provider: cachecfg.ProviderRedis}},
		{name: "capitalism/stripe", provider: capitalismcfg.StripeProvider, cfg: &capitalismcfg.Config{Provider: capitalismcfg.StripeProvider}},
		{name: "capitalism/revenuecat", provider: capitalismcfg.RevenueCatProvider, cfg: &capitalismcfg.Config{Provider: capitalismcfg.RevenueCatProvider}},
		{name: "distributedlock/redis", provider: distributedlockcfg.RedisProvider, cfg: &distributedlockcfg.Config{Provider: distributedlockcfg.RedisProvider}},
		{name: "email/sendgrid", provider: emailcfg.ProviderSendgrid, cfg: &emailcfg.Config{Provider: emailcfg.ProviderSendgrid}},
		{name: "email/mailgun", provider: emailcfg.ProviderMailgun, cfg: &emailcfg.Config{Provider: emailcfg.ProviderMailgun}},
		{name: "email/mailjet", provider: emailcfg.ProviderMailjet, cfg: &emailcfg.Config{Provider: emailcfg.ProviderMailjet}},
		{name: "email/resend", provider: emailcfg.ProviderResend, cfg: &emailcfg.Config{Provider: emailcfg.ProviderResend}},
		{name: "email/postmark", provider: emailcfg.ProviderPostmark, cfg: &emailcfg.Config{Provider: emailcfg.ProviderPostmark}},
		{name: "email/ses", provider: emailcfg.ProviderSES, cfg: &emailcfg.Config{Provider: emailcfg.ProviderSES}},
		{name: "embeddings/openai", provider: embeddingscfg.ProviderOpenAI, cfg: &embeddingscfg.Config{Provider: embeddingscfg.ProviderOpenAI}},
		{name: "embeddings/cohere", provider: embeddingscfg.ProviderCohere, cfg: &embeddingscfg.Config{Provider: embeddingscfg.ProviderCohere}},
		{name: "featureflags/launchdarkly", provider: featureflagscfg.ProviderLaunchDarkly, cfg: &featureflagscfg.Config{Provider: featureflagscfg.ProviderLaunchDarkly}},
		{name: "featureflags/posthog", provider: featureflagscfg.ProviderPostHog, cfg: &featureflagscfg.Config{Provider: featureflagscfg.ProviderPostHog}},
		{name: "routing/chi", provider: routingcfg.ProviderChi, cfg: &routingcfg.Config{Provider: routingcfg.ProviderChi}},
		{name: "routing/stdlib", provider: routingcfg.ProviderStdlib, cfg: &routingcfg.Config{Provider: routingcfg.ProviderStdlib}},
		{name: "routing/httprouter", provider: routingcfg.ProviderHTTPRouter, cfg: &routingcfg.Config{Provider: routingcfg.ProviderHTTPRouter}},
		{name: "routing/gin", provider: routingcfg.ProviderGin, cfg: &routingcfg.Config{Provider: routingcfg.ProviderGin}},
		{name: "messagequeue/redis", provider: string(messagequeuecfg.ProviderRedis), cfg: &messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderRedis}},
		{name: "messagequeue/kafka", provider: string(messagequeuecfg.ProviderKafka), cfg: &messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderKafka}},
		{name: "messagequeue/pubsub", provider: string(messagequeuecfg.ProviderPubSub), cfg: &messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderPubSub}},
		{name: "llm/openai", provider: llmcfg.ProviderOpenAI, cfg: &llmcfg.Config{Provider: llmcfg.ProviderOpenAI}},
		{name: "llm/anthropic", provider: llmcfg.ProviderAnthropic, cfg: &llmcfg.Config{Provider: llmcfg.ProviderAnthropic}},
		{name: "notifications-async/pusher", provider: asyncnotifcfg.ProviderPusher, cfg: &asyncnotifcfg.Config{Provider: asyncnotifcfg.ProviderPusher}},
		{name: "notifications-async/ably", provider: asyncnotifcfg.ProviderAbly, cfg: &asyncnotifcfg.Config{Provider: asyncnotifcfg.ProviderAbly}},
		// sse and websocket configure no credentials, but they do have to
		// declare a topology: a self-hosted provider that names nothing is the
		// silently-single-replica config this sweep should refuse.
		{name: "notifications-async/sse", provider: asyncnotifcfg.ProviderSSE, cfg: &asyncnotifcfg.Config{Provider: asyncnotifcfg.ProviderSSE}},
		{name: "notifications-async/websocket", provider: asyncnotifcfg.ProviderWebSocket, cfg: &asyncnotifcfg.Config{Provider: asyncnotifcfg.ProviderWebSocket}},
		{name: "logging/otelslog", provider: loggingcfg.ProviderOtelSlog, cfg: &loggingcfg.Config{Provider: loggingcfg.ProviderOtelSlog, ServiceName: "svc"}},
		{name: "metrics/otel", provider: metricscfg.ProviderOtel, cfg: &metricscfg.Config{Provider: metricscfg.ProviderOtel, ServiceName: "svc", Enabled: true}},
		{name: "profiling/pyroscope", provider: profilingcfg.ProviderPyroscope, cfg: &profilingcfg.Config{Provider: profilingcfg.ProviderPyroscope, ServiceName: "svc"}},
		{name: "tracing/otel", provider: tracingcfg.ProviderOtel, cfg: &tracingcfg.Config{Provider: tracingcfg.ProviderOtel, ServiceName: "svc"}},
		{name: "tracing/cloudtrace", provider: tracingcfg.ProviderCloudTrace, cfg: &tracingcfg.Config{Provider: tracingcfg.ProviderCloudTrace, ServiceName: "svc"}},
		{name: "search-text/algolia", provider: textsearchcfg.AlgoliaProvider, cfg: &textsearchcfg.Config{Provider: textsearchcfg.AlgoliaProvider}},
		{name: "search-text/elasticsearch", provider: textsearchcfg.ElasticsearchProvider, cfg: &textsearchcfg.Config{Provider: textsearchcfg.ElasticsearchProvider}},
		{name: "search-vector/pgvector", provider: vectorsearchcfg.PGvectorProvider, cfg: &vectorsearchcfg.Config{Provider: vectorsearchcfg.PGvectorProvider}},
		{name: "search-vector/qdrant", provider: vectorsearchcfg.QdrantProvider, cfg: &vectorsearchcfg.Config{Provider: vectorsearchcfg.QdrantProvider}},
		{name: "secrets/gcp", provider: secretscfg.ProviderGCP, cfg: &secretscfg.Config{Provider: secretscfg.ProviderGCP}},
		{name: "secrets/ssm", provider: secretscfg.ProviderSSM, cfg: &secretscfg.Config{Provider: secretscfg.ProviderSSM}},
		{name: "secrets/kubernetes", provider: secretscfg.ProviderKubernetes, cfg: &secretscfg.Config{Provider: secretscfg.ProviderKubernetes}},
		{name: "uploads/filesystem", provider: objectstorage.FilesystemProvider, cfg: &objectstorage.Config{Provider: objectstorage.FilesystemProvider, BucketName: "b"}},
		{name: "uploads/r2", provider: objectstorage.R2Provider, cfg: &objectstorage.Config{Provider: objectstorage.R2Provider, BucketName: "b"}},
		{name: "uploads/backblaze", provider: objectstorage.BackblazeB2Provider, cfg: &objectstorage.Config{Provider: objectstorage.BackblazeB2Provider, BucketName: "b"}},
	}

	for _, tc := range cases {
		T.Run(tc.name+" is refused when nothing is configured", func(t *testing.T) {
			t.Parallel()

			parseEnvironment(t, tc.cfg)
			must.Error(t, tc.cfg.ValidateWithContext(t.Context()),
				must.Sprintf("%s: naming provider %q and configuring nothing validated clean", tc.name, tc.provider))
		})
	}
}

// TestUnselectedProvidersAreNotEnforced is the other half of the invariant: a
// config that names one provider and configures it validates cleanly, whatever
// its siblings would have required had they been chosen.
//
// It exists because the two halves fail in opposite directions and a fix for
// either one alone looks correct. `env:",init"` allocates every pointer
// sub-config, and ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run — regardless of the validation.When guard on the field.
// A seam that guards a sub-config with When alone therefore enforces every
// unselected provider's Required rules, and rejects configurations that are
// completely valid: cache with the memory provider, logging with slog, email
// with any provider at all.
//
// Every seam is listed, including the ones with no pointer sub-config to get
// wrong, so the sweep is a statement about the whole surface rather than about
// the packages that happened to be broken when it was written.
func TestUnselectedProvidersAreNotEnforced(T *testing.T) {
	T.Parallel()

	cases := []struct {
		cfg  validatable
		name string
	}{
		// analytics
		{
			name: "analytics/noop",
			cfg:  &analyticscfg.Config{Provider: analyticscfg.ProviderNoop},
		},
		{
			name: "analytics/segment",
			cfg: &analyticscfg.Config{
				Provider: analyticscfg.ProviderSegment,
				Segment:  &analyticssegment.Config{APIToken: "token"}},
		},
		{
			name: "analytics/posthog",
			cfg: &analyticscfg.Config{
				Provider: analyticscfg.ProviderPostHog,
				Posthog:  &analyticsposthog.Config{APIKey: "key"}},
		},

		// authorization
		{
			name: "authorization/unset",
			cfg:  &rbaccfg.Config{},
		},
		{
			name: "authorization/static",
			cfg:  &rbaccfg.Config{Provider: rbaccfg.ProviderStatic},
		},
		{
			name: "rbac",
			cfg: &rbaccfg.Config{
				Provider: rbaccfg.ProviderDatabase,
				Database: &rbac.Config{Dialect: dialect.Postgres},
			},
		},

		// cache
		{
			name: "cache/memory",
			cfg:  &cachecfg.Config{Provider: cachecfg.ProviderMemory},
		},
		{
			name: "cache/redis",
			cfg: &cachecfg.Config{
				Provider: cachecfg.ProviderRedis,
				Redis:    &cacheredis.Config{Addresses: []string{"localhost:6379"}},
			},
		},

		// capitalism
		{
			name: "capitalism/noop",
			cfg:  &capitalismcfg.Config{Provider: capitalismcfg.NoopProvider},
		},
		{
			name: "capitalism/stripe",
			cfg: &capitalismcfg.Config{
				Provider: capitalismcfg.StripeProvider,
				Stripe:   &capitalismstripe.Config{APIKey: "key", WebhookSecret: "secret"},
			},
		},
		{
			name: "capitalism/revenuecat",
			cfg: &capitalismcfg.Config{
				Provider:   capitalismcfg.RevenueCatProvider,
				RevenueCat: &capitalismrevenuecat.Config{WebhookSecret: "secret"},
			},
		},

		// distributedlock
		{
			name: "distributedlock/noop",
			cfg:  &distributedlockcfg.Config{Provider: distributedlockcfg.NoopProvider},
		},
		{
			name: "distributedlock/memory",
			cfg:  &distributedlockcfg.Config{Provider: distributedlockcfg.MemoryProvider},
		},
		{
			name: "distributedlock/postgres",
			cfg:  &distributedlockcfg.Config{Provider: distributedlockcfg.PostgresProvider},
		},
		{
			name: "distributedlock/redis",
			cfg: &distributedlockcfg.Config{
				Provider: distributedlockcfg.RedisProvider,
				Redis:    &distributedlockredis.Config{Addresses: []string{"localhost:6379"}},
			},
		},

		// email
		{
			name: "email/noop",
			cfg:  &emailcfg.Config{Provider: emailcfg.ProviderNoop},
		},
		{
			name: "email/sendgrid",
			cfg: &emailcfg.Config{
				Provider: emailcfg.ProviderSendgrid,
				Sendgrid: &emailsendgrid.Config{APIToken: "token"},
			},
		},
		{
			name: "email/mailgun",
			cfg: &emailcfg.Config{
				Provider: emailcfg.ProviderMailgun,
				Mailgun:  &emailmailgun.Config{Domain: "example.com", PrivateAPIKey: "key"},
			},
		},
		{
			name: "email/mailjet",
			cfg: &emailcfg.Config{
				Provider: emailcfg.ProviderMailjet,
				Mailjet:  &emailmailjet.Config{APIKey: "key", SecretKey: "secret"},
			},
		},
		{
			name: "email/resend",
			cfg: &emailcfg.Config{
				Provider: emailcfg.ProviderResend,
				Resend:   &emailresend.Config{APIToken: "token"},
			},
		},
		{
			name: "email/postmark",
			cfg: &emailcfg.Config{
				Provider: emailcfg.ProviderPostmark,
				Postmark: &emailpostmark.Config{ServerToken: "token"},
			},
		},
		{
			name: "email/ses",
			cfg: &emailcfg.Config{
				Provider: emailcfg.ProviderSES,
				SES:      &emailses.Config{Region: "us-east-1"},
			},
		},

		// embeddings
		{
			name: "embeddings/unset",
			cfg:  &embeddingscfg.Config{},
		},
		{
			name: "embeddings/noop",
			cfg:  &embeddingscfg.Config{Provider: embeddingscfg.ProviderNoop},
		},
		{
			name: "embeddings/ollama",
			cfg:  &embeddingscfg.Config{Provider: embeddingscfg.ProviderOllama},
		},
		{
			name: "embeddings/openai",
			cfg: &embeddingscfg.Config{
				Provider: embeddingscfg.ProviderOpenAI,
				OpenAI:   &embeddingsopenai.Config{APIKey: "key"},
			},
		},
		{
			name: "embeddings/cohere",
			cfg: &embeddingscfg.Config{
				Provider: embeddingscfg.ProviderCohere,
				Cohere:   &embeddingscohere.Config{APIKey: "key"},
			},
		},

		// eventstream
		{
			name: "eventstream/sse",
			cfg:  &eventstreamcfg.Config{Provider: eventstreamcfg.ProviderSSE},
		},
		{
			name: "eventstream/websocket",
			cfg:  &eventstreamcfg.Config{Provider: eventstreamcfg.ProviderWebSocket},
		},

		// featureflags
		{
			name: "featureflags/noop",
			cfg:  &featureflagscfg.Config{Provider: featureflagscfg.ProviderNoop},
		},
		{
			name: "featureflags/launchdarkly",
			cfg: &featureflagscfg.Config{
				Provider:     featureflagscfg.ProviderLaunchDarkly,
				LaunchDarkly: &featureflagslaunchdarkly.Config{SDKKey: "key"},
			},
		},
		{
			name: "featureflags/posthog",
			cfg: &featureflagscfg.Config{
				Provider: featureflagscfg.ProviderPostHog,
				PostHog:  &featureflagsposthog.Config{ProjectAPIKey: "key", PersonalAPIKey: "personal"},
			},
		},

		// llm
		{
			name: "llm/noop",
			cfg:  &llmcfg.Config{Provider: llmcfg.ProviderNoop},
		},
		{
			name: "llm/openai",
			cfg: &llmcfg.Config{
				Provider: llmcfg.ProviderOpenAI,
				OpenAI:   &llmopenai.Config{APIKey: "key"},
			},
		},
		{
			name: "llm/anthropic",
			cfg: &llmcfg.Config{
				Provider:  llmcfg.ProviderAnthropic,
				Anthropic: &llmanthropic.Config{APIKey: "key"},
			},
		},

		// notifications/async
		//
		// There is no unset case here, unlike its siblings: async notifications
		// have no default provider. Sending nothing is selected by naming noop,
		// so an unset provider is a validation failure rather than a config that
		// validates and then quietly notifies nobody.
		{
			name: "notifications-async/noop",
			cfg:  &asyncnotifcfg.Config{Provider: asyncnotifcfg.ProviderNoop},
		},
		// The self-hosted providers carry a required Topology, so a fully
		// configured example of one names it — the same way the pusher and ably
		// cases below carry their credentials.
		{
			name: "notifications-async/sse",
			cfg: &asyncnotifcfg.Config{
				Provider: asyncnotifcfg.ProviderSSE,
				Topology: asyncnotifcfg.TopologySingleReplica,
			},
		},
		{
			name: "notifications-async/websocket",
			cfg: &asyncnotifcfg.Config{
				Provider: asyncnotifcfg.ProviderWebSocket,
				Topology: asyncnotifcfg.TopologySingleReplica,
			},
		},
		{
			name: "notifications-async/pusher",
			cfg: &asyncnotifcfg.Config{
				Provider: asyncnotifcfg.ProviderPusher,
				Pusher: &asyncpusher.Config{
					AppID:   "id",
					Key:     "key",
					Secret:  "secret",
					Cluster: "cluster",
				},
			},
		},
		{
			name: "notifications-async/ably",
			cfg: &asyncnotifcfg.Config{
				Provider: asyncnotifcfg.ProviderAbly,
				Ably:     &asyncably.Config{APIKey: "key"},
			},
		},

		// notifications/mobile
		{
			name: "notifications-mobile/noop",
			cfg:  &mobilecfg.Config{Provider: mobilecfg.ProviderNoop},
		},
		{
			name: "notifications-mobile/fcm",
			cfg:  &mobilecfg.Config{Provider: mobilecfg.ProviderFCM},
		},
		{
			name: "notifications-mobile/apns",
			cfg: &mobilecfg.Config{
				Provider: mobilecfg.ProviderAPNs,
				APNs: &apns.Config{
					AuthKeyPath: "/etc/apns.p8",
					KeyID:       "keyID",
					TeamID:      "teamID",
					BundleID:    "com.example.app",
				},
			},
		},

		// observability/logging
		{
			name: "logging/unset",
			cfg:  &loggingcfg.Config{ServiceName: "svc"},
		},
		{
			name: "logging/noop",
			cfg:  &loggingcfg.Config{ServiceName: "svc", Provider: loggingcfg.ProviderNoop},
		},
		{
			name: "logging/slog",
			cfg:  &loggingcfg.Config{ServiceName: "svc", Provider: loggingcfg.ProviderSlog},
		},
		{
			name: "logging/zap",
			cfg:  &loggingcfg.Config{ServiceName: "svc", Provider: loggingcfg.ProviderZap},
		},
		{
			name: "logging/zerolog",
			cfg:  &loggingcfg.Config{ServiceName: "svc", Provider: loggingcfg.ProviderZerolog},
		},
		{
			name: "logging/otelslog",
			cfg: &loggingcfg.Config{
				ServiceName: "svc",
				Provider:    loggingcfg.ProviderOtelSlog,
				OtelSlog:    &loggingotelgrpc.Config{CollectorEndpoint: "localhost:4317"},
			},
		},

		// observability/metrics
		{
			name: "metrics/disabled",
			cfg:  &metricscfg.Config{ServiceName: "svc"},
		},
		{
			name: "metrics/noop",
			cfg:  &metricscfg.Config{ServiceName: "svc", Enabled: true, Provider: metricscfg.ProviderNoop},
		},
		{
			name: "metrics/otel",
			cfg: &metricscfg.Config{
				ServiceName: "svc",
				Enabled:     true,
				Provider:    metricscfg.ProviderOtel,
				Otel: &metricsotelgrpc.Config{
					CollectorEndpoint:  "localhost:4317",
					CollectionInterval: time.Minute,
				},
			},
		},

		// observability/profiling
		{
			name: "profiling/unset",
			cfg:  &profilingcfg.Config{ServiceName: "svc"},
		},
		{
			name: "profiling/noop",
			cfg:  &profilingcfg.Config{ServiceName: "svc", Provider: profilingcfg.ProviderNoop},
		},
		{
			name: "profiling/pprof",
			cfg: &profilingcfg.Config{
				ServiceName: "svc",
				Provider:    profilingcfg.ProviderPprof,
				Pprof:       &profilingpprof.Config{Port: 6060},
			},
		},
		{
			name: "profiling/pyroscope",
			cfg: &profilingcfg.Config{
				ServiceName: "svc",
				Provider:    profilingcfg.ProviderPyroscope,
				Pyroscope: &profilingpyroscope.Config{
					ServerAddress: "http://localhost:4040",
					UploadRate:    15 * time.Second,
				},
			},
		},

		// observability/tracing
		{
			name: "tracing/unset",
			cfg:  &tracingcfg.Config{},
		},
		{
			name: "tracing/noop",
			cfg:  &tracingcfg.Config{Provider: tracingcfg.ProviderNoop},
		},
		{
			name: "tracing/otel",
			cfg: &tracingcfg.Config{
				ServiceName: "svc",
				Provider:    tracingcfg.ProviderOtel,
				Otel:        &tracingoteltrace.Config{CollectorEndpoint: "localhost:4317"},
			},
		},
		{
			name: "tracing/cloudtrace",
			cfg: &tracingcfg.Config{
				ServiceName: "svc",
				Provider:    tracingcfg.ProviderCloudTrace,
				CloudTrace:  &tracingcloudtrace.Config{ProjectID: "project"},
			},
		},

		// routing
		{
			name: "routing/chi",
			cfg:  &routingcfg.Config{Provider: routingcfg.ProviderChi, Chi: &routingchi.Config{ServiceName: "svc"}},
		},
		{
			name: "routing/stdlib",
			cfg:  &routingcfg.Config{Provider: routingcfg.ProviderStdlib, Stdlib: &routingstdlib.Config{ServiceName: "svc"}},
		},
		{
			name: "routing/httprouter",
			cfg:  &routingcfg.Config{Provider: routingcfg.ProviderHTTPRouter, HTTPRouter: &routinghttprouter.Config{ServiceName: "svc"}},
		},
		{
			name: "routing/gin",
			cfg:  &routingcfg.Config{Provider: routingcfg.ProviderGin, Gin: &routinggin.Config{ServiceName: "svc"}},
		},

		// search/text
		{
			name: "search-text/noop",
			cfg:  &textsearchcfg.Config{Provider: textsearchcfg.ProviderNoop},
		},
		{
			name: "search-text/algolia",
			cfg: &textsearchcfg.Config{
				Provider: textsearchcfg.AlgoliaProvider,
				Algolia:  &textsearchalgolia.Config{AppID: "id", APIKey: "key"},
			},
		},
		{
			name: "search-text/elasticsearch",
			cfg: &textsearchcfg.Config{
				Provider:      textsearchcfg.ElasticsearchProvider,
				Elasticsearch: &textsearchelasticsearch.Config{Address: "http://localhost:9200"},
			},
		},

		// search/vector
		{
			name: "search-vector/noop",
			cfg:  &vectorsearchcfg.Config{Provider: vectorsearchcfg.ProviderNoop},
		},
		{
			name: "search-vector/pgvector",
			cfg: &vectorsearchcfg.Config{
				Provider: vectorsearchcfg.PGvectorProvider,
				Pgvector: &vectorsearchpgvector.Config{Dimension: 1536},
			},
		},
		{
			name: "search-vector/qdrant",
			cfg: &vectorsearchcfg.Config{
				Provider: vectorsearchcfg.QdrantProvider,
				Qdrant:   &vectorsearchqdrant.Config{BaseURL: "http://localhost:6333", Dimension: 1536},
			},
		},

		// secrets
		{
			name: "secrets/env",
			cfg:  &secretscfg.Config{Provider: secretscfg.ProviderEnv},
		},
		{
			name: "secrets/noop",
			cfg:  &secretscfg.Config{Provider: secretscfg.ProviderNoop},
		},
		{
			name: "secrets/gcp",
			cfg: &secretscfg.Config{
				Provider: secretscfg.ProviderGCP,
				GCP:      &secretsgcp.Config{ProjectID: "project"},
			},
		},
		{
			name: "secrets/ssm",
			cfg: &secretscfg.Config{
				Provider: secretscfg.ProviderSSM,
				SSM:      &secretsssm.Config{Region: "us-east-1"},
			},
		},
		{
			name: "secrets/kubernetes",
			cfg: &secretscfg.Config{
				Provider:   secretscfg.ProviderKubernetes,
				Kubernetes: &secretskubernetes.Config{Namespace: "default"},
			},
		},

		// server/http
		{
			name: "server-http/defaults",
			cfg:  &http.Config{},
		},

		// uploads/objectstorage
		{
			name: "uploads/memory",
			cfg:  &objectstorage.Config{BucketName: "bucket", Provider: objectstorage.MemoryProvider},
		},
		{
			name: "uploads/s3",
			cfg:  &objectstorage.Config{BucketName: "bucket", Provider: objectstorage.S3Provider},
		},
		{
			name: "uploads/gcp",
			cfg:  &objectstorage.Config{BucketName: "bucket", Provider: objectstorage.GCPCloudStorageProvider},
		},
		{
			name: "uploads/filesystem",
			cfg: &objectstorage.Config{
				BucketName:       "bucket",
				Provider:         objectstorage.FilesystemProvider,
				FilesystemConfig: &objectstorage.FilesystemConfig{RootDirectory: "/tmp/uploads"},
			},
		},
		{
			name: "uploads/r2",
			cfg: &objectstorage.Config{
				BucketName: "bucket",
				Provider:   objectstorage.R2Provider,
				R2Config: &objectstorage.R2Config{
					AccountID:       "account",
					AccessKeyID:     "access",
					SecretAccessKey: "secret",
				},
			},
		},
		{
			name: "uploads/backblaze",
			cfg: &objectstorage.Config{
				BucketName: "bucket",
				Provider:   objectstorage.BackblazeB2Provider,
				BackblazeB2Config: &objectstorage.BackblazeB2Config{
					ApplicationKeyID: "keyID",
					ApplicationKey:   "key",
					Region:           "us-west-000",
				},
			},
		},
	}

	for _, tc := range cases {
		T.Run(tc.name+" validates with its siblings unset", func(t *testing.T) {
			t.Parallel()

			parseEnvironment(t, tc.cfg)
			must.NoError(t, tc.cfg.ValidateWithContext(t.Context()),
				must.Sprintf("%s: a config naming its own provider was rejected", tc.name))
		})
	}
}

// TestUnconfiguredOptionalsStayOff covers the other direction: a config that
// names no provider, or an optional feature nobody filled in, must not be
// switched on by env parsing having allocated its sub-config.
func TestUnconfiguredOptionalsStayOff(T *testing.T) {
	T.Parallel()

	T.Run("an unfilled apple-app-site-association does not serve the document", func(t *testing.T) {
		t.Parallel()

		cfg := &http.Config{}
		parseEnvironment(t, cfg)

		must.NotNil(t, cfg.AppleAppSiteAssociation)
		must.False(t, cfg.AppleAppSiteAssociation.Enabled())
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("logging stays off when no provider is named", func(t *testing.T) {
		t.Parallel()

		// This one used to fail for every parsed config: ",init" allocated the
		// otelslog block, and validating it demanded an endpoint URL nobody had
		// asked for, so no logging config could be validated after env parsing.
		cfg := &loggingcfg.Config{ServiceName: "svc"}
		must.NoError(t, env.Parse(cfg))
		must.NoError(t, cfg.ValidateWithContext(context.Background()))
	})

	T.Run("profiling stays off when no provider is named", func(t *testing.T) {
		t.Parallel()

		cfg := &profilingcfg.Config{ServiceName: "svc"}
		parseEnvironment(t, cfg)
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

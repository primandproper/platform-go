package service

import (
	analyticscfg "github.com/primandproper/platform-go/v10/analytics/config"
	auditcfg "github.com/primandproper/platform-go/v10/audit/config"
	tokenscfg "github.com/primandproper/platform-go/v10/authentication/tokens/config"
	authorizationcfg "github.com/primandproper/platform-go/v10/authorization/config"
	capitalismcfg "github.com/primandproper/platform-go/v10/capitalism/config"
	circuitbreakingcfg "github.com/primandproper/platform-go/v10/circuitbreaking/config"
	partitionedcfg "github.com/primandproper/platform-go/v10/circuitbreaking/partitioned/config"
	cookiescfg "github.com/primandproper/platform-go/v10/cookies/config"
	encryptioncfg "github.com/primandproper/platform-go/v10/cryptography/encryption/config"
	databasecfg "github.com/primandproper/platform-go/v10/database/config"
	dataprivacycfg "github.com/primandproper/platform-go/v10/dataprivacy/config"
	distributedlockcfg "github.com/primandproper/platform-go/v10/distributedlock/config"
	emailcfg "github.com/primandproper/platform-go/v10/email/config"
	embeddingscfg "github.com/primandproper/platform-go/v10/embeddings/config"
	"github.com/primandproper/platform-go/v10/encoding"
	eventstreamcfg "github.com/primandproper/platform-go/v10/eventstream/config"
	featureflagscfg "github.com/primandproper/platform-go/v10/featureflags/config"
	"github.com/primandproper/platform-go/v10/httpclient"
	jobscfg "github.com/primandproper/platform-go/v10/jobs/config"
	llmcfg "github.com/primandproper/platform-go/v10/llm/config"
	messagequeuecfg "github.com/primandproper/platform-go/v10/messagequeue/config"
	meteringcfg "github.com/primandproper/platform-go/v10/metering/config"
	asyncnotifcfg "github.com/primandproper/platform-go/v10/notifications/async/config"
	mobilenotifcfg "github.com/primandproper/platform-go/v10/notifications/mobile/config"
	"github.com/primandproper/platform-go/v10/observability"
	loggingcfg "github.com/primandproper/platform-go/v10/observability/logging/config"
	metricscfg "github.com/primandproper/platform-go/v10/observability/metrics/config"
	profilingcfg "github.com/primandproper/platform-go/v10/observability/profiling/config"
	tracingcfg "github.com/primandproper/platform-go/v10/observability/tracing/config"
	outboxcfg "github.com/primandproper/platform-go/v10/outbox/config"
	ratelimitingcfg "github.com/primandproper/platform-go/v10/ratelimiting/config"
	retrycfg "github.com/primandproper/platform-go/v10/retry/config"
	routingcfg "github.com/primandproper/platform-go/v10/routing/config"
	sagacfg "github.com/primandproper/platform-go/v10/saga/config"
	secretscfg "github.com/primandproper/platform-go/v10/secrets/config"
	grpcserver "github.com/primandproper/platform-go/v10/server/grpc"
	httpserver "github.com/primandproper/platform-go/v10/server/http"
	uploadscfg "github.com/primandproper/platform-go/v10/uploads/config"
	"github.com/primandproper/platform-go/v10/uploads/objectstorage"
	webhookscfg "github.com/primandproper/platform-go/v10/webhooks/config"

	"github.com/samber/do/v2"
)

// Register walks cfg and registers every subsystem it names with i.
//
// A non-nil sub-config is registered with the injector alongside its package's
// Register* bridges; a nil one contributes nothing, so invoking what it would
// have built reports absence rather than handing back a default that looks
// configured. Nothing here decides anything the config does not already say.
//
// Registration is lazy — do.Provide stores a constructor and runs it on first
// invoke — so registering a subsystem costs nothing until something asks for
// it, and a subsystem whose application-supplied dependencies are missing fails
// at that invoke rather than here.
//
// The caller registers two things before anything is invoked:
//
//   - a context.Context, which nearly every constructor takes and which the
//     caller owns because it owns the service's lifetime;
//   - the application's own types — the registries, catalogs, handlers,
//     resolvers, and policies this package will never define. Each package's
//     Register* documents the ones it needs.
//
// cfg itself is registered too, so an application hook can read the
// configuration the service booted with off the same injector.
//
// Register does not validate. Call cfg.ValidateWithContext first: it is what
// releases the sub-configs `env:",init"` allocated and nobody filled in, and
// until it has run every subsystem is present.
func Register(i do.Injector, cfg *Config) {
	do.ProvideValue(i, cfg)

	registerObservability(i, cfg)
	registerInfrastructure(i, cfg)
	registerPlatformServices(i, cfg)
	registerDurableWorkflows(i, cfg)
	registerHealth(i)
	registerServers(i, cfg)
}

// registerObservability registers all four pillars unconditionally, matching
// Config.Observability being a value: a pillar naming no provider builds its
// own noop, so there is no absence to represent by leaving it out.
func registerObservability(i do.Injector, cfg *Config) {
	do.ProvideValue(i, &cfg.Observability)
	observability.RegisterO11yConfigs(i)

	loggingcfg.RegisterLogger(i)
	tracingcfg.RegisterTracerProvider(i)
	metricscfg.RegisterMetricsProvider(i)
	profilingcfg.RegisterProfilingProvider(i)
}

// registerInfrastructure registers the clients and transports the rest of a
// service is built on.
func registerInfrastructure(i do.Injector, cfg *Config) {
	if cfg.Database != nil {
		do.ProvideValue(i, cfg.Database)
		databasecfg.RegisterDatabase(i)
	}

	if cfg.MessageQueue != nil {
		do.ProvideValue(i, cfg.MessageQueue)
		messagequeuecfg.RegisterMessageQueue(i)
	}

	if cfg.HTTPClient != nil {
		do.ProvideValue(i, cfg.HTTPClient)
		httpclient.RegisterHTTPClient(i)
	}

	if cfg.Secrets != nil {
		do.ProvideValue(i, cfg.Secrets)
		secretscfg.RegisterSecretSource(i)
	}

	if cfg.Uploads != nil {
		do.ProvideValue(i, cfg.Uploads)
		uploadscfg.RegisterStorageConfig(i)
		objectstorage.RegisterUploadManager(i)
	}

	if cfg.DistributedLock != nil {
		do.ProvideValue(i, cfg.DistributedLock)
		distributedlockcfg.RegisterLocker(i)
		distributedlockcfg.RegisterScopedLocker(i)
	}

	if cfg.CircuitBreaking != nil {
		do.ProvideValue(i, cfg.CircuitBreaking)
		circuitbreakingcfg.RegisterCircuitBreaker(i)
	}

	if cfg.KeyedCircuitBreaking != nil {
		do.ProvideValue(i, cfg.KeyedCircuitBreaking)
		partitionedcfg.RegisterKeyedCircuitBreaker(i)
	}

	if cfg.RateLimiting != nil {
		do.ProvideValue(i, cfg.RateLimiting)
		ratelimitingcfg.RegisterRateLimiter(i)
	}

	if cfg.Retry != nil {
		do.ProvideValue(i, cfg.Retry)
		retrycfg.RegisterPolicy(i)
	}
}

// registerPlatformServices registers the request-path capabilities: identity,
// money, messaging, and the third-party providers behind them.
func registerPlatformServices(i do.Injector, cfg *Config) {
	if cfg.Analytics != nil {
		do.ProvideValue(i, cfg.Analytics)
		analyticscfg.RegisterEventReporter(i)
	}

	if cfg.Authorization != nil {
		do.ProvideValue(i, cfg.Authorization)
		authorizationcfg.RegisterPolicyResolver(i)
	}

	if cfg.Capitalism != nil {
		do.ProvideValue(i, cfg.Capitalism)
		capitalismcfg.RegisterPaymentManager(i)
		capitalismcfg.RegisterUsageReporter(i)
	}

	if cfg.Cookies != nil {
		do.ProvideValue(i, cfg.Cookies)
		cookiescfg.RegisterCookieManager(i)
	}

	if cfg.Email != nil {
		do.ProvideValue(i, cfg.Email)
		emailcfg.RegisterEmailer(i)
	}

	if cfg.Embeddings != nil {
		do.ProvideValue(i, cfg.Embeddings)
		embeddingscfg.RegisterEmbedder(i)
	}

	if cfg.Encryption != nil {
		do.ProvideValue(i, cfg.Encryption)
		encryptioncfg.RegisterEncryptorDecryptor(i)
	}

	if cfg.EventStream != nil {
		do.ProvideValue(i, cfg.EventStream)
		eventstreamcfg.RegisterEventStreamUpgrader(i)
		eventstreamcfg.RegisterBidirectionalEventStreamUpgrader(i)
	}

	if cfg.FeatureFlags != nil {
		do.ProvideValue(i, cfg.FeatureFlags)
		featureflagscfg.RegisterFeatureFlagManager(i)
	}

	if cfg.LLM != nil {
		do.ProvideValue(i, cfg.LLM)
		llmcfg.RegisterLLMProvider(i)
	}

	if cfg.AsyncNotifications != nil {
		do.ProvideValue(i, cfg.AsyncNotifications)
		asyncnotifcfg.RegisterAsyncNotifier(i)
	}

	// The push sender resolves its config by value, not by pointer.
	if cfg.MobileNotifications != nil {
		do.ProvideValue(i, *cfg.MobileNotifications)
		mobilenotifcfg.RegisterPushSender(i)
	}

	if cfg.Tokens != nil {
		do.ProvideValue(i, cfg.Tokens)
		tokenscfg.RegisterTokenIssuer(i)
	}
}

// registerDurableWorkflows registers the tier that outlives a request: the
// stores, workers, relays, and sweepers.
func registerDurableWorkflows(i do.Injector, cfg *Config) {
	if cfg.Audit != nil {
		do.ProvideValue(i, cfg.Audit)
		auditcfg.RegisterRecorder(i)
		auditcfg.RegisterReader(i)
		// No sweeper: pruning the audit log is a retention.Policy the
		// application appends to its policy set, so that it is scheduled and
		// coordinated by the same jobs.Scheduler as every other one. See
		// auditcfg.NewRetentionPolicy.
	}

	if cfg.DataPrivacy != nil {
		do.ProvideValue(i, cfg.DataPrivacy)
		dataprivacycfg.RegisterStore(i)
		dataprivacycfg.RegisterService(i)
		dataprivacycfg.RegisterWorker(i)
		dataprivacycfg.RegisterSweeper(i)
	}

	if cfg.JobsPool != nil {
		do.ProvideValue(i, cfg.JobsPool)
		jobscfg.RegisterPool(i)
	}

	if cfg.JobsScheduler != nil {
		do.ProvideValue(i, cfg.JobsScheduler)
		jobscfg.RegisterScheduler(i)
	}

	if cfg.Metering != nil {
		do.ProvideValue(i, cfg.Metering)
		meteringcfg.RegisterStore(i)
		meteringcfg.RegisterRecorder(i)
		meteringcfg.RegisterEnforcer(i)
		meteringcfg.RegisterFlusher(i)
	}

	if cfg.Outbox != nil {
		do.ProvideValue(i, cfg.Outbox)
		outboxcfg.RegisterWriter(i)
		outboxcfg.RegisterRelay(i)
	}

	if cfg.Saga != nil {
		do.ProvideValue(i, cfg.Saga)
		sagacfg.RegisterStore(i)
		sagacfg.RegisterWorker(i)

		// The outbox publisher is the seam between the two packages, so it is
		// registered only when both ends were configured. Without an outbox,
		// the application names its own saga.EventPublisher.
		if cfg.Outbox != nil {
			sagacfg.RegisterOutboxEventPublisher(i)
		}
	}

	if cfg.Webhooks != nil {
		do.ProvideValue(i, cfg.Webhooks)
		webhookscfg.RegisterStore(i)
		webhookscfg.RegisterDispatcher(i)
		webhookscfg.RegisterWorker(i)
	}
}

// registerServers registers ingress: the encoder/decoder, the router built on
// it, and the two servers.
func registerServers(i do.Injector, cfg *Config) {
	// The encoder/decoder resolves its config by value, not by pointer.
	if cfg.Encoding != nil {
		do.ProvideValue(i, *cfg.Encoding)
		encoding.RegisterServerEncoderDecoder(i)
	}

	if cfg.Routing != nil {
		do.ProvideValue(i, cfg.Routing)
		routingcfg.RegisterRouter(i)
	}

	// The HTTP server resolves its config by value, not by pointer, and takes
	// the service name as an argument because string is too generic a type to
	// resolve from an injector unambiguously.
	if cfg.HTTPServer != nil {
		do.ProvideValue(i, *cfg.HTTPServer)
		httpserver.RegisterHTTPServer(i, cfg.Name)
	}

	if cfg.GRPCServer != nil {
		do.ProvideValue(i, cfg.GRPCServer)
		grpcserver.RegisterGRPCServer(i)
	}
}

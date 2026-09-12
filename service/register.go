package service

import (
	auditcfg "github.com/primandproper/platform-go/v14/audit/config"
	billingcfg "github.com/primandproper/platform-go/v14/billing/config"
	commentscfg "github.com/primandproper/platform-go/v14/comments/config"
	dataprivacycfg "github.com/primandproper/platform-go/v14/dataprivacy/config"
	"github.com/primandproper/platform-go/v14/errormappers"
	identitycfg "github.com/primandproper/platform-go/v14/identity/config"
	issuereportscfg "github.com/primandproper/platform-go/v14/issuereports/config"
	meteringcfg "github.com/primandproper/platform-go/v14/metering/config"
	notificationscfg "github.com/primandproper/platform-go/v14/notifications/config"
	operationscfg "github.com/primandproper/platform-go/v14/operations/config"
	outboxcfg "github.com/primandproper/platform-go/v14/outbox/config"
	rbaccfg "github.com/primandproper/platform-go/v14/rbac/config"
	retentioncfg "github.com/primandproper/platform-go/v14/retention/config"
	sagacfg "github.com/primandproper/platform-go/v14/saga/config"
	settingscfg "github.com/primandproper/platform-go/v14/settings/config"
	shreddingcfg "github.com/primandproper/platform-go/v14/shredding/config"
	waitlistscfg "github.com/primandproper/platform-go/v14/waitlists/config"
	webhookscfg "github.com/primandproper/platform-go/v14/webhooks/config"

	analyticscfg "github.com/primandproper/primitives-go/v2/analytics/config"
	tokenscfg "github.com/primandproper/primitives-go/v2/authentication/tokens/config"
	capitalismcfg "github.com/primandproper/primitives-go/v2/capitalism/config"
	circuitbreakingcfg "github.com/primandproper/primitives-go/v2/circuitbreaking/config"
	partitionedcfg "github.com/primandproper/primitives-go/v2/circuitbreaking/partitioned/config"
	cookiescfg "github.com/primandproper/primitives-go/v2/cookies/config"
	encryptioncfg "github.com/primandproper/primitives-go/v2/cryptography/encryption/config"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	distributedlockcfg "github.com/primandproper/primitives-go/v2/distributedlock/config"
	emailcfg "github.com/primandproper/primitives-go/v2/email/config"
	embeddingscfg "github.com/primandproper/primitives-go/v2/embeddings/config"
	"github.com/primandproper/primitives-go/v2/encoding"
	eventstreamcfg "github.com/primandproper/primitives-go/v2/eventstream/config"
	featureflagscfg "github.com/primandproper/primitives-go/v2/featureflags/config"
	"github.com/primandproper/primitives-go/v2/httpclient"
	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"
	llmcfg "github.com/primandproper/primitives-go/v2/llm/config"
	messagequeuecfg "github.com/primandproper/primitives-go/v2/messagequeue/config"
	asyncnotifcfg "github.com/primandproper/primitives-go/v2/notifications/async/config"
	mobilenotifcfg "github.com/primandproper/primitives-go/v2/notifications/mobile/config"
	"github.com/primandproper/primitives-go/v2/observability"
	loggingcfg "github.com/primandproper/primitives-go/v2/observability/logging/config"
	metricscfg "github.com/primandproper/primitives-go/v2/observability/metrics/config"
	profilingcfg "github.com/primandproper/primitives-go/v2/observability/profiling/config"
	tracingcfg "github.com/primandproper/primitives-go/v2/observability/tracing/config"
	ratelimitingcfg "github.com/primandproper/primitives-go/v2/ratelimiting/config"
	retrycfg "github.com/primandproper/primitives-go/v2/retry/config"
	routingcfg "github.com/primandproper/primitives-go/v2/routing/config"
	secretscfg "github.com/primandproper/primitives-go/v2/secrets/config"
	grpcserver "github.com/primandproper/primitives-go/v2/server/grpc"
	httpserver "github.com/primandproper/primitives-go/v2/server/http"
	uploadscfg "github.com/primandproper/primitives-go/v2/uploads/config"
	"github.com/primandproper/primitives-go/v2/uploads/objectstorage"

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
	registerErrorMappers()
}

// registerErrorMappers installs the transport mappings for this module's domain
// tier, which are not do registrations and take no injector.
//
// errors/http and errors/grpc are primitives and map only primitives, so a
// domain's sentinels reach a client as a considered status because somebody
// registered that domain's mapper. errormappers.Register is that somebody for a
// service built from a Config, and the same one call a service assembled by hand
// makes: the list of which packages declare mappers lives there, in a package
// importing those domains and the two registries, so it is not written out a
// second time here where it could drift.
//
// Unconditional, including for a service whose Config names no DataPrivacy and
// no Operations. That is the resolution of a split this function used to be on
// both sides of — links and sessions registered for every service, dataprivacy
// and operations only inside their config blocks — and the argument that decided
// it is the one the unconditional half already made: an unused mapper costs one
// comparison against a sentinel the service cannot produce, and the expensive
// direction to be wrong in is an operation answering 500 because a config field
// was nil at the moment the mappers were installed.
//
// It takes no injector because the two registries are process-global — an error
// is mapped by whatever is linked into the binary, not by whichever container
// resolved the handler — so a second Register call adds a second copy of each
// mapper, which answers identically and is never reached, since the first match
// wins.
func registerErrorMappers() {
	errormappers.Register()
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
		rbaccfg.RegisterPolicyResolver(i)
	}

	if cfg.Capitalism != nil {
		do.ProvideValue(i, cfg.Capitalism)
		capitalismcfg.RegisterPaymentManager(i)
		capitalismcfg.RegisterUsageReporter(i)
	}

	// The billing store is registered here and its two seams are not. The
	// entitlements PlanSource in billing/plans takes a function saying which
	// reported statuses leave an account entitled, and billing/privacy's
	// collector takes a mapping from a person to the accounts they are billed
	// under; both are judgements a deployment makes, and no environment variable
	// can express either.
	if cfg.Billing != nil {
		do.ProvideValue(i, cfg.Billing)
		billingcfg.RegisterStore(i)
	}

	// The store only, and it resolves a comments.Targets the application
	// registers. Which kinds of thing accept comments is a declaration in Go —
	// each type optionally carrying a function that reads the application's own
	// tables to say whether one is there — and no environment variable can
	// express a function. comments/privacy's collector and eraser are the
	// service's to register too, for the reason every registry in this file is:
	// they need a mapping from a person to the tenants they belong to.
	if cfg.Comments != nil {
		do.ProvideValue(i, cfg.Comments)
		commentscfg.RegisterStore(i)
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

	// Both registrations resolve the container's database.Client, which is to
	// say the keys land in the same database as the data they protect unless
	// the application arranges otherwise. That is the one thing about
	// crypto-shredding that cannot be fixed later — see the shredding package
	// documentation — so a deployment that means it registers a
	// shredding.Store of its own instead of letting this build one.
	//
	// An encryption.KeyWrapper is required and is not built from configuration:
	// which KMS wraps the root key is Go wiring, the same way the Keyset above
	// is.
	if cfg.Shredding != nil {
		do.ProvideValue(i, cfg.Shredding)
		shreddingcfg.RegisterStore(i)
		shreddingcfg.RegisterKeys(i)
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

	if cfg.Identity != nil {
		do.ProvideValue(i, cfg.Identity)
		identitycfg.RegisterStore(i)
	}

	// The store only. issuereports/privacy's collector and eraser need a mapping
	// from a person to the tenants they belong to, which no environment variable
	// can express, so a service that wants its issue reports in its subject
	// access requests registers those two itself against this store.
	if cfg.IssueReports != nil {
		do.ProvideValue(i, cfg.IssueReports)
		issuereportscfg.RegisterStore(i)
	}

	if cfg.LLM != nil {
		do.ProvideValue(i, cfg.LLM)
		llmcfg.RegisterLLMProvider(i)
	}

	if cfg.AsyncNotifications != nil {
		do.ProvideValue(i, cfg.AsyncNotifications)
		asyncnotifcfg.RegisterAsyncNotifier(i)
	}

	// Before the push sender, because the two are the ends of one loop. The
	// store registers a notifications.Registry, RegisterPushSender resolves one
	// optionally, and a sender that finds it deletes the device row a provider
	// has told it is dead instead of addressing the same handset tomorrow. The
	// ordering is documentation rather than mechanism — do resolves lazily, so
	// the loop closes whichever of the two is registered second — but the two
	// belong next to each other for the same reason they close it.
	if cfg.Notifications != nil {
		do.ProvideValue(i, cfg.Notifications)
		notificationscfg.RegisterStore(i)
	}

	if cfg.MobileNotifications != nil {
		do.ProvideValue(i, cfg.MobileNotifications)
		mobilenotifcfg.RegisterPushSender(i)
	}

	if cfg.Settings != nil {
		do.ProvideValue(i, cfg.Settings)
		settingscfg.RegisterStore(i)
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

	// Operations is registered before DataPrivacy because DataPrivacy is built
	// on it: the fulfiller registers its kinds into the operations registry and
	// the service starts operations through the operations service. The ordering
	// here is documentation rather than mechanism — do resolves lazily — but the
	// two belong next to each other for the same reason they are validated
	// together in Config.
	//
	// All five bridges, on the same reading of presence the rest of this walk
	// makes: the config names one operations tier, not five independently
	// switchable pieces of one, so a deployment that set OPERATIONS_WATCHER_POLL
	// gets a watcher rather than a knob nothing reads. The alternative was a
	// switch here saying which of the loops to start, and there is nothing for
	// one to decide that the config does not already say — the same argument
	// this package's documentation makes about feature flags.
	//
	// What it costs a process that streams nothing is a ticker: do.Provide is
	// lazy, Service.New starts what it resolves, and a watcher nobody has
	// subscribed to reads no rows on a tick. That is what makes "inert for a
	// process that serves no streaming endpoint" true of the loop and not only
	// of the settings.
	//
	// What it costs a process that already built its own watcher is a second
	// ticker, and what the registered one is built without is a wake channel.
	// Both are consequences of the registration rather than of this walk, and
	// operationscfg.RegisterWatcher is where they are argued.
	if cfg.Operations != nil {
		do.ProvideValue(i, cfg.Operations)
		operationscfg.RegisterStore(i)
		operationscfg.RegisterQueue(i)
		operationscfg.RegisterService(i)
		operationscfg.RegisterWorker(i)
		operationscfg.RegisterWatcher(i)
	}

	if cfg.DataPrivacy != nil {
		do.ProvideValue(i, cfg.DataPrivacy)
		dataprivacycfg.RegisterStore(i)
		dataprivacycfg.RegisterFulfiller(i)
		dataprivacycfg.RegisterService(i)
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

	// The sweeper is no Runner and gets no place in Service's shutdown order,
	// because it is not a loop: it is a retention.Policy set swept by a
	// jobs.Job the application schedules, the same shape the audit log's own
	// pruning takes. What this walk registers is the sweeper; which policies it
	// enforces is the []retention.Policy the application registers, since what
	// a deployment is allowed to keep and for how long is not a platform
	// decision.
	if cfg.Retention != nil {
		do.ProvideValue(i, cfg.Retention)
		retentioncfg.RegisterSweeper(i)
	}

	if cfg.Saga != nil {
		do.ProvideValue(i, cfg.Saga)
		sagacfg.RegisterStore(i)
		sagacfg.RegisterWorker(i)

		// The outbox publisher is the seam between the two packages, so it is
		// registered only when both ends were configured. Without an outbox,
		// the application names its own saga.EventPublisher — or names none,
		// since sagacfg.RegisterWorker resolves the publisher optionally and a
		// worker without one still advances instances.
		if cfg.Outbox != nil {
			sagacfg.RegisterOutboxEventPublisher(i)
		}
	}

	// A waitlist outlives the request that joined it — somebody signs up in
	// March and is invited in June — which is what puts it in this tier rather
	// than beside the settings store above.
	if cfg.Waitlists != nil {
		do.ProvideValue(i, cfg.Waitlists)
		waitlistscfg.RegisterStore(i)
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

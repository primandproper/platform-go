/*
Package service is platform-go's composition root: one config struct describing
a whole service, and one walk that registers everything it names with a
samber/do injector.

The composition decision is presence in the config, and nothing else. Every
subsystem is a pointer sub-config, `env:",init"` allocates those pointers so a
deployment can configure a subsystem entirely from the environment, and
normalization releases the ones nothing was put into. What survives is what the
operator configured:

	non-nil sub-config  ->  registered
	nil sub-config      ->  never registered

That is "the provider is the only off switch" scaled up one level. There are no
feature flags and no builder DSL, because there is nothing for them to decide
that the config does not already say. A subsystem nobody configured is simply
absent from the injector, which internal/injection.InvokeOptional already
reports as absent — so dependents that treat absence as a noop get their noop,
and dependents that genuinely need it fail with do's own not-found error rather
than running against something that looks configured.

Two constraints hold this package's shape:

  - It defines no domain types, ever. Registries, catalogs, handlers, and
    policies are the application's, and reach the injector from the application.
  - It does not hide the injector. Register takes the caller's do.Injector and
    everything it registers stays individually invocable. This is convenience
    over do, not a wall in front of it.

Register is a pure function of the config, so what a service is made of can be
read off the config it booted with.

New and Run are the other half. Register says what a service is made of; New
builds it in the order it has to come up, and Run serves until the process is
signaled and then takes it down in the order that makes each drain mean
something — ingress first, background loops in reverse, the observability
pillars last. The convention that makes that orderable is Runner, which every
background loop in this module already satisfied before it had a name.

Health falls out of the same reading. Register wraps the infrastructure it
registered in the healthcheck adapters that have always existed for it, so a
service that configured a database and a queue has a readiness answer for both
without asking for one; the servers mount it, HTTP at /readyz and gRPC as
grpc_health_v1, from the one registry. What the platform cannot see — a domain
dependency, a cache whose type no config can name — joins through
WithHealthChecks.
*/
package service

import (
	"context"
	"time"

	analyticscfg "github.com/primandproper/platform-go/v13/analytics/config"
	auditcfg "github.com/primandproper/platform-go/v13/audit/config"
	tokenscfg "github.com/primandproper/platform-go/v13/authentication/tokens/config"
	authorizationcfg "github.com/primandproper/platform-go/v13/authorization/config"
	capitalismcfg "github.com/primandproper/platform-go/v13/capitalism/config"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	partitionedcfg "github.com/primandproper/platform-go/v13/circuitbreaking/partitioned/config"
	"github.com/primandproper/platform-go/v13/cookies"
	encryptioncfg "github.com/primandproper/platform-go/v13/cryptography/encryption/config"
	shreddingcfg "github.com/primandproper/platform-go/v13/cryptography/shredding/config"
	databasecfg "github.com/primandproper/platform-go/v13/database/config"
	dataprivacycfg "github.com/primandproper/platform-go/v13/dataprivacy/config"
	distributedlockcfg "github.com/primandproper/platform-go/v13/distributedlock/config"
	emailcfg "github.com/primandproper/platform-go/v13/email/config"
	embeddingscfg "github.com/primandproper/platform-go/v13/embeddings/config"
	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	eventstreamcfg "github.com/primandproper/platform-go/v13/eventstream/config"
	featureflagscfg "github.com/primandproper/platform-go/v13/featureflags/config"
	"github.com/primandproper/platform-go/v13/httpclient"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	jobscfg "github.com/primandproper/platform-go/v13/jobs/config"
	llmcfg "github.com/primandproper/platform-go/v13/llm/config"
	messagequeuecfg "github.com/primandproper/platform-go/v13/messagequeue/config"
	meteringcfg "github.com/primandproper/platform-go/v13/metering/config"
	asyncnotifcfg "github.com/primandproper/platform-go/v13/notifications/async/config"
	mobilenotifcfg "github.com/primandproper/platform-go/v13/notifications/mobile/config"
	"github.com/primandproper/platform-go/v13/observability"
	operationscfg "github.com/primandproper/platform-go/v13/operations/config"
	outboxcfg "github.com/primandproper/platform-go/v13/outbox/config"
	ratelimitingcfg "github.com/primandproper/platform-go/v13/ratelimiting/config"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"
	routingcfg "github.com/primandproper/platform-go/v13/routing/config"
	sagacfg "github.com/primandproper/platform-go/v13/saga/config"
	secretscfg "github.com/primandproper/platform-go/v13/secrets/config"
	grpcserver "github.com/primandproper/platform-go/v13/server/grpc"
	httpserver "github.com/primandproper/platform-go/v13/server/http"
	uploadscfg "github.com/primandproper/platform-go/v13/uploads/config"
	webhookscfg "github.com/primandproper/platform-go/v13/webhooks/config"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config describes a whole service: its name, its observability, and one
// pointer sub-config per subsystem it is made of.
//
// Every sub-config carries `env:",init"`, which is what lets a deployment
// configure a subsystem from environment variables alone — without it, env
// parsing skips a nil pointer and DATABASE_WRITE_CONNECTION_HOST is silently
// never read. The tag allocates every pointer whether or not anyone wanted the
// subsystem, so ValidateWithContext normalizes first and releases the ones
// nothing was put into. After that, non-nil means the operator configured it.
//
// The generic subsystems are deliberately absent. cache.Cache[T],
// idempotency.Manager[T], and the two search indexes are registered per
// concrete type, with an index name or a type argument no config can supply, so
// they stay explicit calls to their own Register functions on the injector this
// package does not hide. The same goes for the config-less primitives —
// clock.RegisterClock, random.RegisterGenerator — which have no presence to
// read.
//
// The health registry is the one config-less thing this package does register,
// because it is not a primitive: it is a reading of everything else that got
// registered, which is a question only the composition root can answer.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	Analytics            *analyticscfg.Config       `env:",init" envPrefix:"ANALYTICS_"              json:"analytics,omitempty"            yaml:"analytics,omitempty"`
	AsyncNotifications   *asyncnotifcfg.Config      `env:",init" envPrefix:"ASYNC_NOTIFICATIONS_"    json:"asyncNotifications,omitempty"   yaml:"asyncNotifications,omitempty"`
	Audit                *auditcfg.Config           `env:",init" envPrefix:"AUDIT_"                  json:"audit,omitempty"                yaml:"audit,omitempty"`
	Authorization        *authorizationcfg.Config   `env:",init" envPrefix:"AUTHORIZATION_"          json:"authorization,omitempty"        yaml:"authorization,omitempty"`
	Capitalism           *capitalismcfg.Config      `env:",init" envPrefix:"CAPITALISM_"             json:"capitalism,omitempty"           yaml:"capitalism,omitempty"`
	CircuitBreaking      *circuitbreakingcfg.Config `env:",init" envPrefix:"CIRCUIT_BREAKING_"       json:"circuitBreaking,omitempty"      yaml:"circuitBreaking,omitempty"`
	Cookies              *cookies.Config            `env:",init" envPrefix:"COOKIES_"                json:"cookies,omitempty"              yaml:"cookies,omitempty"`
	DataPrivacy          *dataprivacycfg.Config     `env:",init" envPrefix:"DATA_PRIVACY_"           json:"dataPrivacy,omitempty"          yaml:"dataPrivacy,omitempty"`
	Database             *databasecfg.Config        `env:",init" envPrefix:"DATABASE_"               json:"database,omitempty"             yaml:"database,omitempty"`
	DistributedLock      *distributedlockcfg.Config `env:",init" envPrefix:"DISTRIBUTED_LOCK_"       json:"distributedLock,omitempty"      yaml:"distributedLock,omitempty"`
	Email                *emailcfg.Config           `env:",init" envPrefix:"EMAIL_"                  json:"email,omitempty"                yaml:"email,omitempty"`
	Embeddings           *embeddingscfg.Config      `env:",init" envPrefix:"EMBEDDINGS_"             json:"embeddings,omitempty"           yaml:"embeddings,omitempty"`
	Encoding             *encoding.Config           `env:",init" envPrefix:"ENCODING_"               json:"encoding,omitempty"             yaml:"encoding,omitempty"`
	Encryption           *encryptioncfg.Config      `env:",init" envPrefix:"ENCRYPTION_"             json:"encryption,omitempty"           yaml:"encryption,omitempty"`
	Shredding            *shreddingcfg.Config       `env:",init" envPrefix:"SHREDDING_"              json:"shredding,omitempty"            yaml:"shredding,omitempty"`
	EventStream          *eventstreamcfg.Config     `env:",init" envPrefix:"EVENT_STREAM_"           json:"eventStream,omitempty"          yaml:"eventStream,omitempty"`
	FeatureFlags         *featureflagscfg.Config    `env:",init" envPrefix:"FEATURE_FLAGS_"          json:"featureFlags,omitempty"         yaml:"featureFlags,omitempty"`
	GRPCServer           *grpcserver.Config         `env:",init" envPrefix:"GRPC_SERVER_"            json:"grpcServer,omitempty"           yaml:"grpcServer,omitempty"`
	HTTPClient           *httpclient.Config         `env:",init" envPrefix:"HTTP_CLIENT_"            json:"httpClient,omitempty"           yaml:"httpClient,omitempty"`
	HTTPServer           *httpserver.Config         `env:",init" envPrefix:"HTTP_SERVER_"            json:"httpServer,omitempty"           yaml:"httpServer,omitempty"`
	JobsPool             *jobscfg.PoolConfig        `env:",init" envPrefix:"JOBS_POOL_"              json:"jobsPool,omitempty"             yaml:"jobsPool,omitempty"`
	JobsScheduler        *jobscfg.SchedulerConfig   `env:",init" envPrefix:"JOBS_SCHEDULER_"         json:"jobsScheduler,omitempty"        yaml:"jobsScheduler,omitempty"`
	KeyedCircuitBreaking *partitionedcfg.Config     `env:",init" envPrefix:"KEYED_CIRCUIT_BREAKING_" json:"keyedCircuitBreaking,omitempty" yaml:"keyedCircuitBreaking,omitempty"`
	LLM                  *llmcfg.Config             `env:",init" envPrefix:"LLM_"                    json:"llm,omitempty"                  yaml:"llm,omitempty"`
	MessageQueue         *messagequeuecfg.Config    `env:",init" envPrefix:"MESSAGE_QUEUE_"          json:"messageQueue,omitempty"         yaml:"messageQueue,omitempty"`
	Metering             *meteringcfg.Config        `env:",init" envPrefix:"METERING_"               json:"metering,omitempty"             yaml:"metering,omitempty"`
	MobileNotifications  *mobilenotifcfg.Config     `env:",init" envPrefix:"MOBILE_NOTIFICATIONS_"   json:"mobileNotifications,omitempty"  yaml:"mobileNotifications,omitempty"`
	Operations           *operationscfg.Config      `env:",init" envPrefix:"OPERATIONS_"             json:"operations,omitempty"           yaml:"operations,omitempty"`
	Outbox               *outboxcfg.Config          `env:",init" envPrefix:"OUTBOX_"                 json:"outbox,omitempty"               yaml:"outbox,omitempty"`
	RateLimiting         *ratelimitingcfg.Config    `env:",init" envPrefix:"RATE_LIMITING_"          json:"rateLimiting,omitempty"         yaml:"rateLimiting,omitempty"`
	Retry                *retrycfg.Config           `env:",init" envPrefix:"RETRY_"                  json:"retry,omitempty"                yaml:"retry,omitempty"`
	Routing              *routingcfg.Config         `env:",init" envPrefix:"ROUTING_"                json:"routing,omitempty"              yaml:"routing,omitempty"`
	Saga                 *sagacfg.Config            `env:",init" envPrefix:"SAGA_"                   json:"saga,omitempty"                 yaml:"saga,omitempty"`
	Secrets              *secretscfg.Config         `env:",init" envPrefix:"SECRETS_"                json:"secrets,omitempty"              yaml:"secrets,omitempty"`
	Tokens               *tokenscfg.Config          `env:",init" envPrefix:"TOKENS_"                 json:"tokens,omitempty"               yaml:"tokens,omitempty"`
	Uploads              *uploadscfg.Config         `env:",init" envPrefix:"UPLOADS_"                json:"uploads,omitempty"              yaml:"uploads,omitempty"`
	Webhooks             *webhookscfg.Config        `env:",init" envPrefix:"WEBHOOKS_"               json:"webhooks,omitempty"             yaml:"webhooks,omitempty"`

	// Name identifies the service. It is the name the HTTP server reports and
	// the default ServiceName for each observability pillar.
	Name string `env:"NAME" json:"name,omitempty" yaml:"name,omitempty"`

	// Observability is a value rather than a pointer: every service has the
	// four pillars, and a pillar naming no provider is already its own noop, so
	// there is nothing for absence to mean here that an unconfigured pillar
	// does not already mean.
	Observability observability.Config `envPrefix:"OBSERVABILITY_" json:"observability,omitzero" yaml:"observability,omitempty"`

	// ShutdownTimeout bounds the whole of Service.Shutdown: draining ingress,
	// closing every background loop, the final flushes, and releasing the
	// clients.
	//
	// One budget rather than one per phase, because what an orchestrator gives
	// a process is a single number — Kubernetes' terminationGracePeriodSeconds
	// — and per-phase timeouts that sum to more than it are timeouts nobody
	// honors. The phases therefore compete for it, which is a second reason the
	// shutdown order is what it is.
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"30s" json:"shutdownTimeout,omitzero" yaml:"shutdownTimeout,omitempty"`
}

// DefaultShutdownTimeout is the budget Service.Shutdown gets when the config
// names none. It is the same thirty seconds Kubernetes defaults
// terminationGracePeriodSeconds to, so a service that configures neither is
// bounded by its own deadline rather than by a SIGKILL.
const DefaultShutdownTimeout = 30 * time.Second

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults propagates Name to the observability pillars that have no
// ServiceName of their own, so a service names itself once instead of four
// more times. A pillar that was given its own name keeps it, and so does a
// shutdown budget that was set.
func (cfg *Config) EnsureDefaults() {
	// Applied here as well as through envDefault, because a Config assembled in
	// code never goes through env parsing, and an unset budget would otherwise
	// make every shutdown an expired deadline.
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = DefaultShutdownTimeout
	}

	if cfg.Name == "" {
		return
	}

	for _, name := range []*string{
		&cfg.Observability.Logging.ServiceName,
		&cfg.Observability.Metrics.ServiceName,
		&cfg.Observability.Tracing.ServiceName,
		&cfg.Observability.Profiling.ServiceName,
	} {
		if *name == "" {
			*name = cfg.Name
		}
	}
}

// ValidateWithContext applies defaults, normalizes, defaults again, and then
// validates whatever survives.
//
// The order is the whole method. This config's own defaults first, so a service
// that named itself once is not failed for the four pillar names it did not
// repeat. Normalization second, and it is the load-bearing step: until the
// sub-configs `env:",init"` allocated and nobody filled in have been released,
// every subsystem looks configured and the validation below is a validation of
// the entire module.
//
// The surviving sub-configs' own defaults third, and only third. Every
// constructor in this module applies a config's defaults before validating it,
// because an unset field with a documented default is not a validation failure;
// a parent that validates a sub-config it did not construct has to do the same
// or it enforces rules the constructor never would. It cannot happen any
// earlier — defaulting before normalization fills in every allocated
// sub-config, and nothing would ever look unconfigured again.
//
// The sub-config rules are then deliberately empty. ozzo dereferences a field
// pointer once and validates whatever implements ValidatableWithContext, which
// for a *T field is the sub-config itself and for a nil one is nothing — so
// listing a field is exactly the rule "if this subsystem is present, it
// validates itself". Nothing here second-guesses what a sub-config considers
// valid; a nil one is not a subsystem with missing settings, it is a subsystem
// this service is not made of.
//
// Observability is listed through a closure rather than plainly because it is a
// value field, and ozzo's dereference lands on the value, whose method set does
// not include a pointer-receiver ValidateWithContext.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	cfg.EnsureDefaults()

	if err := cfgnorm.UnconfiguredToNil(cfg); err != nil {
		return err
	}

	if err := cfgnorm.EnsureSubDefaults(cfg); err != nil {
		return err
	}

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Name, validation.Required),
		// A negative budget is an operator error worth reporting rather than
		// quietly correcting; a zero one has already been defaulted above.
		validation.Field(&cfg.ShutdownTimeout, validation.Min(time.Duration(0))),
		validation.Field(&cfg.Observability, validation.By(func(any) error {
			return cfg.Observability.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.Analytics),
		validation.Field(&cfg.AsyncNotifications),
		validation.Field(&cfg.Audit),
		validation.Field(&cfg.Authorization),
		validation.Field(&cfg.Capitalism),
		validation.Field(&cfg.CircuitBreaking),
		validation.Field(&cfg.Cookies),
		// The one cross-subsystem rule in this list, and it earns the exception
		// because the alternative is silence. dataprivacy runs its exports and
		// erasures as operations, so a service configured for privacy requests
		// and not for operations resolves a Service that accepts a subject's
		// request, records it, and has nothing anywhere that will ever fulfill
		// it — discovered thirty days later by the subject rather than at boot.
		validation.Field(&cfg.DataPrivacy, validation.By(func(any) error {
			if cfg.DataPrivacy != nil && cfg.Operations == nil {
				return platformerrors.New(
					"dataprivacy is configured but operations is not; dataprivacy requests are fulfilled " +
						"as operations, so both are needed")
			}

			return nil
		})),
		validation.Field(&cfg.Database),
		validation.Field(&cfg.DistributedLock),
		validation.Field(&cfg.Email),
		validation.Field(&cfg.Embeddings),
		validation.Field(&cfg.Encoding),
		validation.Field(&cfg.Encryption),
		validation.Field(&cfg.EventStream),
		validation.Field(&cfg.FeatureFlags),
		validation.Field(&cfg.GRPCServer),
		validation.Field(&cfg.HTTPClient),
		validation.Field(&cfg.HTTPServer),
		validation.Field(&cfg.JobsPool),
		validation.Field(&cfg.JobsScheduler),
		validation.Field(&cfg.KeyedCircuitBreaking),
		validation.Field(&cfg.LLM),
		validation.Field(&cfg.MessageQueue),
		validation.Field(&cfg.Metering),
		validation.Field(&cfg.MobileNotifications),
		validation.Field(&cfg.Operations),
		validation.Field(&cfg.Outbox),
		validation.Field(&cfg.RateLimiting),
		validation.Field(&cfg.Retry),
		validation.Field(&cfg.Routing),
		validation.Field(&cfg.Saga),
		validation.Field(&cfg.Secrets),
		validation.Field(&cfg.Shredding),
		validation.Field(&cfg.Tokens),
		validation.Field(&cfg.Uploads),
		validation.Field(&cfg.Webhooks),
	)
}

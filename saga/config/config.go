/*
Package sagacfg assembles the saga machinery from environment configuration:
the Store the runner and the worker share, and the Worker that advances
instances.

Both read one Config, so the table prefix a Runner writes to is by construction
the one the Worker claims from. The dialect is not configured here at all: it
comes from the database.Client, so it cannot disagree with the database the
statements actually run against.

The registry is not configured here, and cannot be. A step is a Go function, so
there is no way to express a definition in the environment and no way to load
one at runtime — it is passed explicitly to NewWorker and to the Runner.

Runner construction is likewise not here, for a different reason: NewRunner is
generic over the state type, and a constructor in a config package would have to
name that type. Build the store here, hand it to NewRunner[T] at the call site.
*/
package sagacfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/distributedlock"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"
	"github.com/primandproper/platform-go/v13/saga"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles a saga Store and Worker.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix names the instance table. It must match the prefix the
	// migrations were rendered with. Defaults to saga.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// EventTopic is the outbox topic lifecycle events are published to.
	// Defaults to saga.DefaultEventTopic.
	EventTopic string `env:"EVENT_TOPIC" json:"eventTopic,omitempty" yaml:"eventTopic,omitempty"`

	// Worker carries the advance loop's knobs.
	Worker saga.WorkerConfig `env:",init" envPrefix:"WORKER_" json:"worker,omitzero" yaml:"worker,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = saga.DefaultTablePrefix
	}

	if cfg.EventTopic == "" {
		cfg.EventTopic = saga.DefaultEventTopic
	}

	cfg.Worker.EnsureDefaults()
}

// ValidateWithContext validates a Config.
//
// The nested config is validated through a validation.By closure because ozzo
// dereferences a struct-value field before checking ValidatableWithContext, so
// it would otherwise be skipped.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.EventTopic, validation.Required),
		validation.Field(&cfg.Worker, validation.By(func(any) error {
			return cfg.Worker.ValidateWithContext(ctx)
		})),
	)
}

// NewStore builds the saga Store from the config.
func NewStore(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	opts ...Option,
) (saga.Store, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if cfg == nil {
		return nil, errors.New("nil saga config provided")
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating saga config")
	}

	store, err := saga.NewSQLStore(client,
		saga.WithTablePrefix(cfg.TablePrefix),
		saga.WithStoreLogger(logger),
		saga.WithStoreTracerProvider(tracerProvider),
		saga.WithStoreMetricsProvider(metricsProvider),
	)
	if err != nil {
		return nil, err
	}

	return store, nil
}

// NewWorker builds the Worker that advances instances.
//
// The locker is required and has no default — see saga.ErrNilLocker. The
// idempotency manager and the event publisher are optional; both may be nil,
// and the package documentation says what each one being absent costs.
func NewWorker(
	ctx context.Context,
	cfg *Config,
	store saga.Store,
	registry *saga.Registry,
	locker distributedlock.ScopedLocker,
	manager *idempotency.Manager[saga.StepResult],
	publisher saga.EventPublisher,
	opts ...Option,
) (*saga.Worker, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if cfg == nil {
		return nil, errors.New("nil saga config provided")
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating saga config")
	}

	return saga.NewWorker(ctx, &cfg.Worker, store, registry, locker,
		saga.WithWorkerLogger(logger),
		saga.WithWorkerTracerProvider(tracerProvider),
		saga.WithWorkerMetricsProvider(metricsProvider),
		saga.WithWorkerIdempotency(manager),
		saga.WithWorkerEventPublisher(publisher),
	)
}

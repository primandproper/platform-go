package sagacfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/outbox"
	"github.com/primandproper/platform-go/v14/saga"

	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/distributedlock"
	"github.com/primandproper/primitives-go/v2/idempotency"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers a saga.Store with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the
// injector before the Store is invoked.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (saga.Store, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewStore(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			WithPillars(pillars),
		)
	})
}

// RegisterOutboxEventPublisher registers a saga.EventPublisher backed by the
// registered *outbox.Writer, publishing to the config's EventTopic.
//
// Prerequisites: *Config and *outbox.Writer (see outboxcfg.RegisterWriter)
// must be registered in the injector before the publisher is invoked.
func RegisterOutboxEventPublisher(i do.Injector) {
	do.Provide(i, func(i do.Injector) (saga.EventPublisher, error) {
		cfg := do.MustInvoke[*Config](i)
		cfg.EnsureDefaults()

		publisher, err := saga.NewOutboxPublisher(
			do.MustInvoke[*outbox.Writer](i),
			saga.WithEventTopic(cfg.EventTopic),
		)
		if err != nil {
			return nil, err
		}

		return publisher, nil
	})
}

// RegisterWorker registers a *saga.Worker with the injector.
//
// The idempotency manager and the event publisher are both optional. Without
// the manager, a step whose instance is advanced twice runs twice; without a
// publisher, instances still advance and nothing outside the saga tables hears
// about it. That the publisher is optional is what lets a deployment configure
// sagas without an outbox — see RegisterOutboxEventPublisher, which is the
// publisher an outbox-carrying deployment registers.
//
// Prerequisites: *Config, saga.Store (see RegisterStore), *saga.Registry (the
// application's saga definitions), and distributedlock.ScopedLocker must be
// registered in the injector before the Worker is invoked.
func RegisterWorker(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*saga.Worker, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		manager, err := injection.InvokeOptional[*idempotency.Manager[saga.StepResult]](i)
		if err != nil {
			return nil, err
		}

		publisher, err := injection.InvokeOptional[saga.EventPublisher](i)
		if err != nil {
			return nil, err
		}

		opts := []Option{WithPillars(pillars)}
		if manager != nil {
			opts = append(opts, WithWorkerIdempotency(manager))
		}
		if publisher != nil {
			opts = append(opts, WithWorkerEventPublisher(publisher))
		}

		return NewWorker(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[saga.Store](i),
			do.MustInvoke[*saga.Registry](i),
			do.MustInvoke[distributedlock.ScopedLocker](i),
			opts...,
		)
	})
}

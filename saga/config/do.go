package sagacfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/distributedlock"
	"github.com/primandproper/platform-go/v13/idempotency"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/outbox"
	"github.com/primandproper/platform-go/v13/saga"

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
// Prerequisites: *Config, saga.Store (see RegisterStore), *saga.Registry (the
// application's saga definitions), distributedlock.ScopedLocker,
// *idempotency.Manager[saga.StepResult], and saga.EventPublisher (see
// RegisterOutboxEventPublisher) must be registered in the injector before the
// Worker is invoked.
func RegisterWorker(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*saga.Worker, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewWorker(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[saga.Store](i),
			do.MustInvoke[*saga.Registry](i),
			do.MustInvoke[distributedlock.ScopedLocker](i),
			do.MustInvoke[*idempotency.Manager[saga.StepResult]](i),
			do.MustInvoke[saga.EventPublisher](i),
			WithPillars(pillars),
		)
	})
}

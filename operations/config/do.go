package operationscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/operations"
	"github.com/primandproper/platform-go/v13/workqueue"

	"github.com/samber/do/v2"
)

// RegisterStore registers an operations.Store with the injector.
//
// Prerequisites: *Config and database.Client must be registered before the store
// is invoked.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (operations.Store, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewStore(
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			WithPillars(pillars),
		)
	})
}

// RegisterQueue registers the *workqueue.Queue[string] operations are dispatched
// through.
//
// It is registered separately from the service because it is shared: the service
// enqueues onto it, the worker claims from it, and both resolve the same value.
// A Queue owns a goroutine; do's shutdown runs Close.
func RegisterQueue(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*workqueue.Queue[string], error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewQueue(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			WithPillars(pillars),
		)
	})
}

// RegisterService registers an operations.Service over the registered store,
// queue, and registry.
//
// Prerequisites: everything RegisterStore and RegisterQueue need, plus both of
// those registrations and an *operations.Registry. The registry is registered
// rather than passed because it is the one dependency that is genuinely the
// application's — a container that resolves a Service has to be able to say what
// kinds of work exist.
//
// This is the registration an API process wants. It starts nothing and needs no
// shutdown of its own.
func RegisterService(i do.Injector) {
	do.Provide(i, func(i do.Injector) (operations.Service, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return newServiceOver(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[operations.Store](i),
			do.MustInvoke[*workqueue.Queue[string]](i),
			do.MustInvoke[*operations.Registry](i),
			WithPillars(pillars),
		)
	})
}

// RegisterWorker registers an *operations.Worker over the registered store,
// queue, and registry, so the worker and the service share one queue and one set
// of metrics.
//
// A Worker starts nothing on its own: Run blocks, and the injector will not call
// it. Run it from wherever you start the rest of your background work, and stop
// it by cancelling that context.
func RegisterWorker(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*operations.Worker, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewWorker(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[operations.Store](i),
			do.MustInvoke[*workqueue.Queue[string]](i),
			do.MustInvoke[*operations.Registry](i),
			WithPillars(pillars),
		)
	})
}

// RegisterWatcher registers an *operations.Watcher over the registered store.
//
// Like the Worker, its Run blocks and the injector will not call it — but unlike
// the Worker, nothing works without it: a Watcher whose Run is not started
// delivers each subscriber its first snapshot and then nothing. Start it beside
// the rest of your background work.
func RegisterWatcher(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*operations.Watcher, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewWatcher(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[operations.Store](i),
			WithPillars(pillars),
		)
	})
}

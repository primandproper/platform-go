package operationscfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v14/operations"
	"github.com/primandproper/platform-go/v14/workqueue"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers an operations.Store with the injector.
//
// Prerequisites: context.Context, *Config and database.Client must be registered
// before the store is invoked. The context is the one NewStore validates the
// configuration under, and is the same key every other registration here
// resolves.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (operations.Store, error) {
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

// QueueKey is the injector key the operations queue is registered under, and the
// one RegisterService and RegisterWorker resolve it by.
//
// The queue is named rather than left to its type because *workqueue.Queue[string]
// is not a key this package gets to keep. A queue is generic over the key its
// work is addressed by, string is the obvious one, and do infers a registration's
// key from the provider's return type — so a consumer draining its own
// string-keyed work through workqueuecfg.RegisterQueue[string] registers exactly
// the type this package would have registered, and do panics on the second of
// the two. The container is not the place to discover that two unrelated
// components both schedule work by name, and a consumer should not have to wrap
// their queue in a type of their own to say so.
//
// Naming this one leaves the inferred key where the generic registration puts
// it, so the two register beside each other.
const QueueKey = "operations.queue"

// RegisterQueue registers the *workqueue.Queue[string] operations are dispatched
// through, under QueueKey.
//
// It is registered separately from the service because it is shared: the service
// enqueues onto it, the worker claims from it, and both resolve the same value.
//
// A Queue owns a goroutine and has to be Closed, and the injector will not do
// it: do recognizes a Shutdown method, and this module's background components
// spell that Close. Resolve it with InvokeQueue and close it from the same place
// you shut the rest of them down — after ingress is gone, so a request still in
// flight can finish enqueueing.
//
// A service built from a service.Config is already covered: Service.Shutdown
// closes it as a final flush, after the loops it shares a table with have
// stopped. A container assembled by hand owes the call itself, and a
// composition root that may or may not have configured operations asks
// QueueRegistered first.
func RegisterQueue(i do.Injector) {
	do.ProvideNamed(i, QueueKey, func(i do.Injector) (*workqueue.Queue[string], error) {
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

// InvokeQueue resolves the queue RegisterQueue registered.
//
// It exists so that QueueKey is spelled once. A consumer has to reach the queue
// for the same reason RegisterQueue is separate from RegisterService — somebody
// has to Close it, and nothing in the container will — and a key they have to
// spell themselves is a key that can be misspelled into a "service not found"
// at the one moment a process is trying to shut down.
func InvokeQueue(i do.Injector) (*workqueue.Queue[string], error) {
	return do.InvokeNamed[*workqueue.Queue[string]](i, QueueKey)
}

// QueueRegistered reports whether a queue is registered under QueueKey.
//
// It is the half of injection.InvokeOptional that a named registration cannot
// get from that function, which resolves strictly by do.NameOf, and it is asked
// for the reason that function gives: a composition root shutting a whole
// service down has to tell "nobody configured operations" from "the queue
// somebody configured failed to build", and the error alone cannot say which.
// do reports a provider that itself invoked something unregistered with
// do.ErrServiceNotFound too, so the sentinel says only that something along the
// way was missing. Registration presence can say the rest.
//
// Pair it with InvokeQueue: a queue that is registered and still fails to build
// is an error, not an absence, because a component somebody configured wrongly
// should not be silently swapped for one nobody asked for.
func QueueRegistered(i do.Injector) bool {
	return slices.ContainsFunc(i.ListProvidedServices(), func(d do.ServiceDescription) bool {
		return d.Service == QueueKey
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
			do.MustInvoke[database.Client](i),
			do.MustInvoke[operations.Store](i),
			do.MustInvokeNamed[*workqueue.Queue[string]](i, QueueKey),
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
			do.MustInvokeNamed[*workqueue.Queue[string]](i, QueueKey),
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
// the rest of your background work, which for a service composed by
// service.Register is already done: service.New resolves the Watcher into its
// runner list and Run starts it.
//
// # The registered watcher polls
//
// It is built without WithWatcherWakeup, as the registered queue is built
// without WithQueueWakeup, and for the same reason: a wake channel is a value
// the caller owns and do.Provide has nowhere to take one from. The two loops
// want different signals — one fires when work is enqueued, the other when an
// operation row moves — so a bare channel resolved from the injector would be
// one key answering two questions.
//
// WatcherConfig.Poll is therefore the whole of the latency here. The wake is a
// Postgres LISTEN/NOTIFY optimisation on top rather than the thing that makes
// the watch path work — see WithWatcherWakeup for what it does and does not
// change — and a consumer who wants it builds the Watcher with NewWatcher,
// pairs it with WithStoreNotifyChannel on the writing side, and joins it
// through service.WithRunners instead of through this registration.
//
// # A hand-built watcher is now a second one
//
// Before service.Register called this, a consumer who wanted the watch path
// built one themselves. That still works and is no longer the only one: the
// registration is lazy, but service.New resolves it, so a process that starts
// its own Watcher and also composes itself from a service.Config with an
// Operations section runs two loops polling one table. No subscriber is lost —
// a subscription reaches only the Watcher whose Watch returned it — but the
// second ticker does nothing the first is not already doing. Pass the
// hand-built one through service.WithRunners and let this registration be the
// only one, or keep it and compose the rest by hand.
func RegisterWatcher(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*operations.Watcher, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewWatcher(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			do.MustInvoke[operations.Store](i),
			WithPillars(pillars),
		)
	})
}

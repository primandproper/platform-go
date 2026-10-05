package webhookscfg

import (
	"context"

	"github.com/primandproper/platform-go/v15/outbox"
	"github.com/primandproper/platform-go/v15/recording"
	recordingcfg "github.com/primandproper/platform-go/v15/recording/config"
	"github.com/primandproper/platform-go/v15/webhooks"
	"github.com/primandproper/platform-go/v15/webhooks/recordinghooks"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers a webhooks.Store with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the
// injector before the Store is invoked.
//
// webhooks.Hooks is resolved through recordingcfg.InvokeHooks: one the
// application registered, then a recordinghooks.RecordingHooks when a
// *recording.Recorder is registered, then none. The Recorder's own Emitter
// dispatches through a store of its own rather than this one, which is what
// lets this store's hooks write through it; see NewEmitter.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (webhooks.Store, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		opts := []Option{WithPillars(pillars)}

		hooks, err := recordingcfg.InvokeHooks(i, func(r *recording.Recorder) (webhooks.Hooks, error) {
			return recordinghooks.NewRecordingHooks(r)
		})
		if err != nil {
			return nil, err
		}

		if hooks != nil {
			opts = append(opts, WithStoreOptions(webhooks.WithHooks(hooks)))
		}

		return NewStore(ctx, cfg, client, opts...)
	})
}

// RegisterDispatcher registers a webhooks.Dispatcher with the injector.
//
// Prerequisites: *Config, database.Client, webhooks.Store (see RegisterStore),
// and webhooks.Catalog must be registered in the injector before the Dispatcher
// is invoked. The Catalog is the application's declaration of which event types
// exist, so it has no environment-driven construction here. The client is the
// one NewDispatcher takes a reader off; see there for the single read it serves.
func RegisterDispatcher(i do.Injector) {
	do.Provide(i, func(i do.Injector) (webhooks.Dispatcher, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		store, err := do.Invoke[webhooks.Store](i)
		if err != nil {
			return nil, err
		}

		catalog, err := do.Invoke[webhooks.Catalog](i)
		if err != nil {
			return nil, err
		}

		return NewDispatcher(ctx, cfg, client, store, catalog, WithPillars(pillars))
	})
}

// RegisterEmitter registers a *webhooks.Emitter with the injector.
//
// Prerequisites: *Config, database.Client, an *outbox.Writer
// (outboxcfg.RegisterWriter) and webhooks.Catalog must be registered in the
// injector before the Emitter is invoked. The Emitter dispatches through a
// store and a dispatcher it builds for itself, not the ones RegisterStore and
// RegisterDispatcher provide; NewEmitter says why.
func RegisterEmitter(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*webhooks.Emitter, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		writer, err := do.Invoke[*outbox.Writer](i)
		if err != nil {
			return nil, err
		}

		catalog, err := do.Invoke[webhooks.Catalog](i)
		if err != nil {
			return nil, err
		}

		return NewEmitter(ctx, cfg, client, writer, catalog, WithPillars(pillars))
	})
}

// RegisterWorker registers a *webhooks.Worker with the injector.
//
// Prerequisites: *Config and webhooks.Store (see RegisterStore) must be
// registered in the injector before the Worker is invoked.
func RegisterWorker(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*webhooks.Worker, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		store, err := do.Invoke[webhooks.Store](i)
		if err != nil {
			return nil, err
		}

		return NewWorker(ctx, cfg, store, WithPillars(pillars))
	})
}

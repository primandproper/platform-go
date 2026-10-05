package billingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/recording"
	recordingcfg "github.com/primandproper/platform-go/v14/recording/config"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers a billing.Store with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the injector
// before the Store is invoked.
//
// billing.Hooks is resolved through recordingcfg.InvokeHooks: one the
// application registered, then a billing.RecordingHooks when a
// *recording.Recorder is registered, then none, which leaves the store's
// NoopHooks default.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (billing.Store, error) {
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

		hooks, err := recordingcfg.InvokeHooks(i, func(r *recording.Recorder) (billing.Hooks, error) {
			return billing.NewRecordingHooks(r)
		})
		if err != nil {
			return nil, err
		}

		if hooks != nil {
			opts = append(opts, WithStoreOptions(billing.WithHooks(hooks)))
		}

		return NewStore(ctx, cfg, client, opts...)
	})
}

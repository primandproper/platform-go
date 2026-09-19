package outboxcfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/outbox"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterWriter registers an *outbox.Writer with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the
// injector before the Writer is invoked.
func RegisterWriter(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*outbox.Writer, error) {
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

		return NewWriter(ctx, cfg, client, WithPillars(pillars))
	})
}

// RegisterRelay registers an *outbox.Relay with the injector. The Relay builds
// its own publisher provider from the config's Queue section.
//
// Prerequisites: *Config and database.Client must be registered in the
// injector before the Relay is invoked.
func RegisterRelay(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*outbox.Relay, error) {
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

		return NewRelay(ctx, cfg, client, WithPillars(pillars))
	})
}

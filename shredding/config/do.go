package shreddingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/shredding"

	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/cryptography/encryption"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/messagequeue"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers a shredding.Store with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the injector
// before the Store is invoked.
//
// The database.Client it resolves is whichever one the container holds, which is
// the point at which a deployment quietly decides that its keys share a backup
// schedule with the data they protect. A container that wants them separate
// registers this against its own injector, or provides the Store directly.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (shredding.Store, error) {
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

		return NewStore(ctx, cfg, client, WithPillars(pillars))
	})
}

// RegisterKeys registers a shredding.Keys with the injector.
//
// A registered messagequeue.PublisherProvider is used to announce shreds to the
// rest of the fleet; its absence means cached keys expire on the TTL instead,
// which is the guarantee either way. The subscribing half is not registered
// here — a consumer needs somewhere to report its errors and something to keep
// it running, neither of which belongs in a provider function. Build it with
// NewInvalidationConsumer wherever this service's other consumers are started,
// and check shredding_invalidations_received afterwards: a container that
// registers the publisher and never builds the consumer is the one
// configuration that is worse than having neither.
//
// Prerequisites: *Config, shredding.Store (see RegisterStore), and an
// encryption.KeyWrapper must be registered in the injector before Keys is
// invoked.
func RegisterKeys(i do.Injector) {
	do.Provide(i, func(i do.Injector) (shredding.Keys, error) {
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

		opts := []Option{WithPillars(pillars)}

		provider, err := injection.InvokeOptional[messagequeue.PublisherProvider](i)
		if err != nil {
			return nil, err
		}

		if provider != nil {
			broadcaster, bErr := NewBroadcaster(ctx, cfg, provider)
			if bErr != nil {
				return nil, bErr
			}

			opts = append(opts, WithKeysOptions(shredding.WithBroadcaster(broadcaster)))
		}

		store, err := do.Invoke[shredding.Store](i)
		if err != nil {
			return nil, err
		}

		keyWrapper, err := do.Invoke[encryption.KeyWrapper](i)
		if err != nil {
			return nil, err
		}

		return NewKeys(ctx, cfg, store, keyWrapper, opts...)
	})
}

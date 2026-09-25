package oauth2clientscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"

	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers an oauth2clients.Store with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the injector
// before the Store is invoked.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (oauth2clients.Store, error) {
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

// RegisterService registers an *oauth2clients.Service with the injector.
//
// Prerequisites: *Config, database.Client and oauth2clients.Store (see
// RegisterStore) must be registered before the Service is invoked.
//
// oauth2clients.Hooks is resolved if something registered one and defaulted to
// oauth2clients.NoopHooks otherwise, which is the same reading the constructor
// takes. Absence is the only thing the lookup absorbs: a Hooks that is
// registered but fails to build is returned rather than replaced by the noop,
// since a Service that quietly ran without it would commit every registration
// with none of the companions the consumer registered hooks to get. The
// distinction is the one observability.InvokePillars draws, through the same
// injection.InvokeOptional.
func RegisterService(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*oauth2clients.Service, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		opts := []Option{WithPillars(pillars)}

		hooks, err := injection.InvokeOptional[oauth2clients.Hooks](i)
		if err != nil {
			return nil, platformerrors.Wrap(err, "invoking oauth2clients hooks")
		}

		if hooks != nil {
			opts = append(opts, WithHooks(hooks))
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

		store, err := do.Invoke[oauth2clients.Store](i)
		if err != nil {
			return nil, err
		}

		return NewService(ctx, cfg, client, store, opts...)
	})
}

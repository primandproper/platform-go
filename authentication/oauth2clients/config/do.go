package oauth2clientscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"

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
// Prerequisites: *Config, database.Client, oauth2clients.Store (see
// RegisterStore) and oauth2clients.Hooks must be registered before the Service
// is invoked.
//
// oauth2clients.Hooks is required too. A container whose registrations owe no
// companions registers oauth2clients.NoopHooks{} by name:
//
//	do.ProvideValue[oauth2clients.Hooks](i, oauth2clients.NoopHooks{})
//
// A container that registers none fails when the Service is invoked, with an
// error naming the type it wanted.
func RegisterService(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*oauth2clients.Service, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		hooks, err := do.Invoke[oauth2clients.Hooks](i)
		if err != nil {
			return nil, platformerrors.Wrapf(err,
				"resolving %s: the application registers what commits alongside each registration, NoopHooks{} if nothing",
				do.NameOf[oauth2clients.Hooks]())
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

		return NewService(ctx, cfg, client, store, hooks, WithPillars(pillars))
	})
}

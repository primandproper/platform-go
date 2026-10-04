package identitycfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers an identity.Store with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the injector
// before the Store is invoked.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (identity.Store, error) {
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

// RegisterService registers an *identity.Service with the injector.
//
// Prerequisites: *Config, database.Client, identity.Store (see RegisterStore)
// and identity.Hooks must be registered before the Service is invoked.
//
// identity.Hooks has no default. An application with nothing to commit beside
// an identity write registers identity.NoopHooks{} by name:
//
//	do.ProvideValue[identity.Hooks](i, identity.NoopHooks{})
//
// A container that registers none fails when the Service is invoked, with an
// error naming the type it wanted.
//
// An identity.InvitationMailer is resolved if something registered one, and
// absence is left alone. Registering one is what moves an invitation's token
// out of Hooks.AfterInvite and into the mailer, as identity.WithInvitationMailer
// describes; registering none leaves the hook holding it.
func RegisterService(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*identity.Service, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		opts := []Option{WithPillars(pillars)}

		hooks, err := do.Invoke[identity.Hooks](i)
		if err != nil {
			return nil, platformerrors.Wrapf(err,
				"resolving %s: the application registers what commits alongside each identity write, or identity.NoopHooks{}",
				do.NameOf[identity.Hooks]())
		}

		mailer, err := injection.InvokeOptional[identity.InvitationMailer](i)
		if err != nil {
			return nil, platformerrors.Wrap(err, "invoking identity invitation mailer")
		}

		if mailer != nil {
			opts = append(opts, WithServiceOptions(identity.WithInvitationMailer(mailer)))
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

		store, err := do.Invoke[identity.Store](i)
		if err != nil {
			return nil, err
		}

		return NewService(ctx, cfg, client, store, hooks, opts...)
	})
}

// RegisterServer registers an *identitygrpc.Server with the injector.
//
// Prerequisites: *Config, database.Client, identity.Store (see RegisterStore),
// *identity.Service (see RegisterService) and a callers.PrincipalExtractor must
// be registered before the Server is invoked.
//
// The extractor is a MustInvoke where Hooks above is not, and the asymmetry is
// the point: a server with no hooks writes nothing extra, and a server with no
// way to resolve a caller answers every read with the zero scope. The first is a
// configuration and the second is a hole, so the container refuses to build one.
// Register it with do.ProvideValue, keyed on the named type:
//
//	do.ProvideValue[callers.PrincipalExtractor](i, principalFromContext)
//
// Registering the server does not declare its authorization requirements or
// install its error mappings. See NewServer for what a mount still owes.
func RegisterServer(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*identitygrpc.Server, error) {
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

		service, err := do.Invoke[*identity.Service](i)
		if err != nil {
			return nil, err
		}

		store, err := do.Invoke[identity.Store](i)
		if err != nil {
			return nil, err
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		principalExtractor, err := do.Invoke[callers.PrincipalExtractor](i)
		if err != nil {
			return nil, err
		}

		return NewServer(ctx, cfg, service, store, client, principalExtractor, WithPillars(pillars))
	})
}

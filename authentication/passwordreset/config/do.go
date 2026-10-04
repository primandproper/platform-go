package passwordresetcfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers a passwordreset.Store with the injector.
//
// Prerequisites: *Config, database.Client, passwordreset.Hooks and a
// context.Context must be registered in the injector before the Store is
// invoked. The context bounds the sweeper's life, when the config starts one.
//
// The hooks are required. A container whose token writes owe no companions
// registers passwordreset.NoopHooks{} by name:
//
//	do.ProvideValue[passwordreset.Hooks](i, passwordreset.NoopHooks{})
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (passwordreset.Store, error) {
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

		hooks, err := do.Invoke[passwordreset.Hooks](i)
		if err != nil {
			return nil, platformerrors.Wrapf(err,
				"resolving %s: the application registers what runs inside each token write, NoopHooks{} if nothing",
				do.NameOf[passwordreset.Hooks]())
		}

		return NewStore(ctx, cfg, client, hooks, WithPillars(pillars))
	})
}

// RegisterService registers a *passwordreset.Service with the injector.
//
// Prerequisites: *Config, database.Client, passwordreset.Store (see
// RegisterStore) and identity.Store must be registered before the Service is
// invoked, and so must the two the application supplies:
//
//	do.ProvideValue[passwordreset.Mailer](i, mailer)
//	do.ProvideValue[authentication.Authenticator](i, authenticator)
//
// Both are required and neither has a default; the package documentation says
// why the authenticator in particular must not. A container missing either
// fails when the Service is invoked — at boot, for a service built through
// service.New — with an error naming the one it wanted.
//
// A passwordreset.PasswordPolicy is used if the application registered one. It
// is an alias of signin.PasswordPolicy, so it is the same key signincfg
// resolves: one registration governs every door that writes a password, and
// this one cannot be the door a policy forgot. Only absence is absorbed; one
// that is registered and fails to build is returned.
func RegisterService(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*passwordreset.Service, error) {
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

		store, err := do.Invoke[passwordreset.Store](i)
		if err != nil {
			return nil, err
		}

		directory, err := do.Invoke[identity.Store](i)
		if err != nil {
			return nil, platformerrors.Wrapf(err,
				"resolving %s as the password reset directory", do.NameOf[identity.Store]())
		}

		authenticator, err := do.Invoke[authentication.Authenticator](i)
		if err != nil {
			return nil, platformerrors.Wrapf(err,
				"resolving %s: the application registers the one sign-in hashes with",
				do.NameOf[authentication.Authenticator]())
		}

		mailer, err := do.Invoke[passwordreset.Mailer](i)
		if err != nil {
			return nil, platformerrors.Wrapf(err,
				"resolving %s: the application registers what delivers a reset link",
				do.NameOf[passwordreset.Mailer]())
		}

		policy, err := injection.InvokeOptional[passwordreset.PasswordPolicy](i)
		if err != nil {
			return nil, platformerrors.Wrap(err, "invoking password reset password policy")
		}

		return NewService(ctx, cfg, client, store, directory, authenticator, mailer,
			WithPillars(pillars),
			WithServiceOptions(passwordreset.WithPasswordPolicy(policy)),
		)
	})
}

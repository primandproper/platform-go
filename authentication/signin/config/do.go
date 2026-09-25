package signincfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/authentication/tokens"
	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterService registers a *signin.Service with the injector.
//
// Prerequisites: *Config, a context.Context, database.Client, identity.Store
// and tokens.Issuer must be registered before the Service is invoked, and so
// must the authenticator the application supplies:
//
//	do.ProvideValue[authentication.Authenticator](i, authenticator)
//
// It is required and has no default. passwordresetcfg resolves the same key,
// so a container holding both blocks hashes a reset's password with the engine
// sign-in verifies it with. The context bounds the sweepers' lives.
//
// Two more are required when their block is present. A Registration block
// needs the *identity.Service identitycfg.RegisterService provides. That
// service is not registered here, because an application that already calls
// RegisterService would then hold two providers under one key. A MagicLinks
// block needs a signin.MagicLinkMailer. A missing one fails when the Service is
// invoked, and for a service built through service.New that means at boot, with
// an error naming what was wanted.
//
// signin.Hooks, signin.PasswordPolicy and signin.ClaimsBuilder are used if the
// application registered them, and the service's own defaults apply otherwise.
// Only absence is absorbed, as identitycfg absorbs it for identity.Hooks. One
// that is registered and fails to build is returned.
func RegisterService(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*signin.Service, error) {
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

		// Validated here as well as in NewService, because which of the
		// conditional dependencies below are wanted is read off the blocks that
		// survive validation.
		if err = cfg.ValidateWithContext(ctx); err != nil {
			return nil, platformerrors.Wrap(err, "validating sign-in config")
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		directory, err := do.Invoke[identity.Store](i)
		if err != nil {
			return nil, platformerrors.Wrapf(err,
				"resolving %s as the sign-in directory", do.NameOf[identity.Store]())
		}

		authenticator, err := do.Invoke[authentication.Authenticator](i)
		if err != nil {
			return nil, platformerrors.Wrapf(err,
				"resolving %s: the application registers the one every password is hashed with",
				do.NameOf[authentication.Authenticator]())
		}

		issuer, err := do.Invoke[tokens.Issuer](i)
		if err != nil {
			return nil, platformerrors.Wrapf(err,
				"resolving %s: the tokens block builds what a sign-in's token is minted with",
				do.NameOf[tokens.Issuer]())
		}

		opts := []Option{WithPillars(pillars)}

		if cfg.Registration != nil {
			registrar, registrarErr := do.Invoke[*identity.Service](i)
			if registrarErr != nil {
				return nil, platformerrors.Wrapf(registrarErr,
					"resolving %s: the registration block registers people through it",
					do.NameOf[*identity.Service]())
			}

			opts = append(opts, WithRegistrar(registrar))
		}

		if cfg.MagicLinks != nil {
			mailer, mailerErr := do.Invoke[signin.MagicLinkMailer](i)
			if mailerErr != nil {
				return nil, platformerrors.Wrapf(mailerErr,
					"resolving %s: the application registers what delivers a sign-in link",
					do.NameOf[signin.MagicLinkMailer]())
			}

			opts = append(opts, WithMagicLinkMailer(mailer))
		}

		serviceOpts, err := optionalServiceOptions(i)
		if err != nil {
			return nil, err
		}

		opts = append(opts, WithServiceOptions(serviceOpts...))

		return NewService(ctx, cfg, client, directory, authenticator, issuer, opts...)
	})
}

// optionalServiceOptions resolves the three things an application may register
// and need not, and turns each one it registered into the option that attaches
// it.
func optionalServiceOptions(i do.Injector) ([]signin.ServiceOption, error) {
	var opts []signin.ServiceOption

	hooks, err := injection.InvokeOptional[signin.Hooks](i)
	if err != nil {
		return nil, platformerrors.Wrap(err, "invoking sign-in hooks")
	}

	if hooks != nil {
		opts = append(opts, signin.WithHooks(hooks))
	}

	policy, err := injection.InvokeOptional[signin.PasswordPolicy](i)
	if err != nil {
		return nil, platformerrors.Wrap(err, "invoking sign-in password policy")
	}

	if policy != nil {
		opts = append(opts, signin.WithPasswordPolicy(policy))
	}

	claims, err := injection.InvokeOptional[signin.ClaimsBuilder](i)
	if err != nil {
		return nil, platformerrors.Wrap(err, "invoking sign-in claims builder")
	}

	if claims != nil {
		opts = append(opts, signin.WithClaimsBuilder(claims))
	}

	return opts, nil
}

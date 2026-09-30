package passkeyscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/passkeys"

	"github.com/primandproper/primitives-go/v2/authentication/webauthn"
	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers a passkeys.Store with the injector.
//
// Prerequisites: *Config, database.Client and a context.Context must be
// registered before the Store is invoked.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (passkeys.Store, error) {
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

// RegisterService registers a *passkeys.Service with the injector.
//
// Prerequisites: *Config, database.Client, a context.Context, passkeys.Store
// (see RegisterStore) and the *webauthn.RelyingParty the WebAuthn block
// registers, and the two the application supplies:
//
//	do.ProvideValue[passkeys.UserResolver](i, resolve)
//	do.ProvideValue[passkeys.EnrollmentGate](i, gate) // or passkeys.AdmitEveryEnrollment
//
// Both are required and neither has a default; a container missing either fails
// when the Service is invoked — at boot, for a service built through
// service.New — with an error naming the one it wanted. A registered
// passkeys.UsernameResolver, passkeys.AlternativeSignIn or passkeys.Hooks is
// attached, and an absent one is left off.
func RegisterService(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*passkeys.Service, error) {
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

		store, err := do.Invoke[passkeys.Store](i)
		if err != nil {
			return nil, err
		}

		rp, err := do.Invoke[*webauthn.RelyingParty](i)
		if err != nil {
			return nil, platformerrors.Wrapf(err,
				"resolving %s: the WebAuthn block registers the relying party passkeys verify against",
				do.NameOf[*webauthn.RelyingParty]())
		}

		resolve, err := do.Invoke[passkeys.UserResolver](i)
		if err != nil {
			return nil, platformerrors.Wrapf(err,
				"resolving %s: the application registers which of its users a webauthn handle names",
				do.NameOf[passkeys.UserResolver]())
		}

		gate, err := do.Invoke[passkeys.EnrollmentGate](i)
		if err != nil {
			return nil, platformerrors.Wrapf(err,
				"resolving %s: the application registers what a passkey enrollment must pass, or passkeys.AdmitEveryEnrollment",
				do.NameOf[passkeys.EnrollmentGate]())
		}

		serviceOpts, err := optionalServiceOptions(i)
		if err != nil {
			return nil, err
		}

		return NewService(ctx, cfg, client, store, rp, resolve, gate,
			WithPillars(pillars), WithServiceOptions(serviceOpts...))
	})
}

// optionalServiceOptions resolves what an application may register and need
// not, and turns each one it registered into the option that attaches it.
func optionalServiceOptions(i do.Injector) ([]passkeys.ServiceOption, error) {
	var opts []passkeys.ServiceOption

	usernames, err := injection.InvokeOptional[passkeys.UsernameResolver](i)
	if err != nil {
		return nil, platformerrors.Wrap(err, "invoking passkey username resolver")
	}

	if usernames != nil {
		opts = append(opts, passkeys.WithUsernameResolver(usernames))
	}

	alternative, err := injection.InvokeOptional[passkeys.AlternativeSignIn](i)
	if err != nil {
		return nil, platformerrors.Wrap(err, "invoking passkey alternative sign-in check")
	}

	if alternative != nil {
		opts = append(opts, passkeys.WithAlternativeSignIn(alternative))
	}

	hooks, err := injection.InvokeOptional[passkeys.Hooks](i)
	if err != nil {
		return nil, platformerrors.Wrap(err, "invoking passkey hooks")
	}

	if hooks != nil {
		opts = append(opts, passkeys.WithHooks(hooks))
	}

	return opts, nil
}

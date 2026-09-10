package identitycfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	"github.com/primandproper/primitives-go/config/injection"
	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability"

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

		return NewStore(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			WithPillars(pillars),
		)
	})
}

// RegisterService registers an *identity.Service with the injector.
//
// Prerequisites: *Config, database.Client and identity.Store (see RegisterStore)
// must be registered before the Service is invoked.
//
// identity.Hooks is resolved if something registered one and defaulted to
// identity.NoopHooks otherwise, which is the same reading the constructor takes:
// an application with nothing to commit beside an identity write registers
// nothing. Absence is the only thing the lookup absorbs, and it is decided by
// whether a Hooks is registered, not by the error the invocation returns. A
// Hooks that is registered but fails to build is returned — including one whose
// own provider asked the container for something nobody registered, which do
// reports with the very sentinel a missing Hooks would carry. The distinction
// is the one observability.InvokePillars draws, through the same
// injection.InvokeOptional: "nobody registered one" is a configuration, "the
// one registered could not be built" is a failure, and a Service that quietly
// ran the noop in its place would commit every identity write with none of the
// companions the consumer registered hooks to get.
func RegisterService(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*identity.Service, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		opts := []Option{WithPillars(pillars)}

		hooks, err := injection.InvokeOptional[identity.Hooks](i)
		if err != nil {
			return nil, platformerrors.Wrap(err, "invoking identity hooks")
		}

		if hooks != nil {
			opts = append(opts, WithHooks(hooks))
		}

		return NewService(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			do.MustInvoke[identity.Store](i),
			opts...,
		)
	})
}

// RegisterServer registers an *identitygrpc.Server with the injector.
//
// Prerequisites: *Config, database.Client, identity.Store (see RegisterStore),
// *identity.Service (see RegisterService) and an
// identitygrpc.PrincipalExtractor must be registered before the Server is
// invoked.
//
// The extractor is a MustInvoke where Hooks above is not, and the asymmetry is
// the point: a server with no hooks writes nothing extra, and a server with no
// way to resolve a caller answers every read with the zero scope. The first is a
// configuration and the second is a hole, so the container refuses to build one.
// Register it with do.ProvideValue, keyed on the named type:
//
//	do.ProvideValue[identitygrpc.PrincipalExtractor](i, principalFromContext)
//
// Registering the server does not declare its authorization requirements or
// install its error mappings. See NewServer for what a mount still owes.
func RegisterServer(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*identitygrpc.Server, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewServer(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[*identity.Service](i),
			do.MustInvoke[identity.Store](i),
			do.MustInvoke[database.Client](i),
			do.MustInvoke[identitygrpc.PrincipalExtractor](i),
			WithPillars(pillars),
		)
	})
}

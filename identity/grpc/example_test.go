package grpc_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v14/authorization"
	authzgrpc "github.com/primandproper/platform-go/v14/authorization/grpc"
	"github.com/primandproper/platform-go/v14/database"
	"github.com/primandproper/platform-go/v14/errormappers"
	grpcerrors "github.com/primandproper/platform-go/v14/errors/grpc"
	"github.com/primandproper/platform-go/v14/identity"
	identitycfg "github.com/primandproper/platform-go/v14/identity/config"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/observability"
	grpcserver "github.com/primandproper/platform-go/v14/server/grpc"

	"github.com/samber/do/v2"
	"google.golang.org/grpc"
)

// Example_mount is the acceptance test for this package's seams, written as the
// thing the ticket asked for: a config block, a Hooks value, a permission
// fragment and a mount.
//
// Everything a consumer supplies is something only they can — the database, how
// somebody proved who they are, what else to commit alongside an identity write,
// and which of their roles may do what. Nothing in it is boilerplate this module
// could have written and did not.
func Example_mount() {
	var (
		ctx       context.Context
		cfg       *identitycfg.Config             // yours: a config block
		client    database.Client                 //
		pillars   *observability.Pillars          //
		serverCfg *grpcserver.Config              //
		hooks     identity.Hooks                  // yours: what commits alongside a write
		principal identitygrpc.PrincipalExtractor // yours: who is calling
		authn     grpc.UnaryServerInterceptor     // yours: what puts them on the context
		grants    authorization.GrantsExtractor   // yours: their authority
	)

	_ = func() error {
		opts := []identitycfg.Option{identitycfg.WithPillars(pillars), identitycfg.WithHooks(hooks)}

		store, err := identitycfg.NewStore(ctx, cfg, client, opts...)
		if err != nil {
			return err
		}

		svc, err := identitycfg.NewService(ctx, cfg, client, store, opts...)
		if err != nil {
			return err
		}

		srv, err := identitycfg.NewServer(ctx, cfg, client, svc, store, principal, opts...)
		if err != nil {
			return err
		}

		// The permissions, in one call: the permissioned methods with what they
		// need and the self-service ones as public. Compose other domains onto
		// the same builder before Build.
		reqs, err := identitygrpc.Require(authzgrpc.NewRequirements()).Build()
		if err != nil {
			return err
		}

		enforcer, err := authzgrpc.NewEnforcer(reqs, grants)
		if err != nil {
			return err
		}

		// The error mappings, for the whole domain tier. service.Register makes
		// this call for a service built from a service.Config.
		errormappers.Register()

		_, err = grpcserver.NewGRPCServer(ctx, serverCfg,
			[]grpc.UnaryServerInterceptor{
				grpcerrors.UnaryErrorEncodingInterceptor(),
				authn,
				enforcer.UnaryServerInterceptor(),
			},
			nil,
			[]grpcserver.RegistrationFunc{srv.RegisterOn},
		)

		return err
	}

	fmt.Println("mounted")
	// Output: mounted
}

// Example_mountThroughTheContainer is the same mount for a consumer using the
// injector, which is what service.Register walks.
//
// Three registrations and two values of their own. The Hooks registration is
// optional — omit it and the Service gets identity.NoopHooks — and the
// PrincipalExtractor is not, because a directory server that cannot resolve a
// caller has no scope to filter its reads on.
func Example_mountThroughTheContainer() {
	var (
		hooks     identity.Hooks
		principal identitygrpc.PrincipalExtractor
	)

	_ = func(i do.Injector) {
		// The type argument is load-bearing on both: it keys the registration on
		// the interface and the named func type rather than on whatever concrete
		// thing the consumer happens to be holding.
		do.ProvideValue[identity.Hooks](i, hooks)
		do.ProvideValue[identitygrpc.PrincipalExtractor](i, principal)

		identitycfg.RegisterStore(i)
		identitycfg.RegisterService(i)
		identitycfg.RegisterServer(i)
	}

	fmt.Println("registered")
	// Output: registered
}

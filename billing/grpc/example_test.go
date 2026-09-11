package grpc_test

import (
	"context"
	"fmt"

	billingcfg "github.com/primandproper/platform-go/v14/billing/config"
	billinggrpc "github.com/primandproper/platform-go/v14/billing/grpc"
	"github.com/primandproper/platform-go/v14/errormappers"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
	"github.com/primandproper/primitives-go/v2/database"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/observability"
	grpcserver "github.com/primandproper/primitives-go/v2/server/grpc"

	"google.golang.org/grpc"
)

// Example_mount is what turning this domain on looks like, and it is written as
// the acceptance test for the package's seams rather than as a tour.
//
// Everything a consumer supplies is something only they can: the database, how
// somebody proved who they are, which accounts a caller has standing in, and
// which of their roles may do what. Nothing in it is boilerplate this module
// could have written and did not — and there is deliberately no place in it to
// say what a subscription status means, because that reading belongs in
// billing/plans.
func Example_mount() {
	var (
		ctx         context.Context
		cfg         *billingcfg.Config                 // yours: a config block
		client      database.Client                    //
		pillars     *observability.Pillars             //
		serverCfg   *grpcserver.Config                 //
		principals  identitygrpc.PrincipalExtractor    // yours: who is calling
		authn       grpc.UnaryServerInterceptor        // yours: what puts them on the context
		grants      authorization.GrantsExtractor      // yours: their authority
		memberships *identitygrpc.MembershipAuthorizer // yours: which accounts are theirs
	)

	_ = func() error {
		store, err := billingcfg.NewStore(ctx, cfg, client, billingcfg.WithPillars(pillars))
		if err != nil {
			return err
		}

		// The account authorizer is positional because every default is wrong
		// here. A consumer already running the directory hands over its
		// MembershipAuthorizer, which satisfies the seam as it stands.
		srv, err := billinggrpc.NewServer(store, client, principals, memberships,
			billinggrpc.WithPillars(pillars))
		if err != nil {
			return err
		}

		// Fail-closed: a method declared nowhere is denied, so the fragment is
		// composed rather than approximated.
		reqs, err := billinggrpc.Require(authzgrpc.NewRequirements()).Build()
		if err != nil {
			return err
		}

		enforcer, err := authzgrpc.NewEnforcer(reqs, grants)
		if err != nil {
			return err
		}

		// Not optional and not made by this package: without it every refusal
		// this service returns arrives as codes.Unknown.
		errormappers.Register()

		_, err = grpcserver.NewGRPCServer(ctx, serverCfg,
			[]grpc.UnaryServerInterceptor{
				authn,
				enforcer.UnaryServerInterceptor(),
				grpcerrors.UnaryErrorEncodingInterceptor(),
			},
			nil,
			[]grpcserver.RegistrationFunc{srv.RegisterOn},
		)

		return err
	}

	fmt.Println("mounted")
	// Output: mounted
}

package grpc_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v14/errormappers"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	issuereportscfg "github.com/primandproper/platform-go/v14/issuereports/config"
	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"

	"github.com/primandproper/primitives-go/authorization"
	authzgrpc "github.com/primandproper/primitives-go/authorization/grpc"
	"github.com/primandproper/primitives-go/database"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	"github.com/primandproper/primitives-go/observability"
	grpcserver "github.com/primandproper/primitives-go/server/grpc"

	"google.golang.org/grpc"
)

// Example_mount is what turning this domain on looks like, and it is written as
// the acceptance test for the package's seams rather than as a tour.
//
// Everything a consumer supplies is something only they can: the database, how
// somebody proved who they are, whose reports a caller may name, and which of
// their roles may do what. Nothing in it is boilerplate this module could have
// written and did not — and there is deliberately no place in it to say what a
// report is about or what categories a product sorts them into, because that
// vocabulary is the consumer's and stays a string.
func Example_mount() {
	var (
		ctx        context.Context
		cfg        *issuereportscfg.Config         // yours: a config block
		client     database.Client                 //
		pillars    *observability.Pillars          //
		serverCfg  *grpcserver.Config              //
		principals identitygrpc.PrincipalExtractor // yours: who is calling
		authn      grpc.UnaryServerInterceptor     // yours: what puts them on the context
		grants     authorization.GrantsExtractor   // yours: their authority
	)

	_ = func() error {
		store, err := issuereportscfg.NewStore(ctx, cfg, client, issuereportscfg.WithPillars(pillars))
		if err != nil {
			return err
		}

		// The report authorizer is positional because every default is wrong
		// here: this package owns the column that says whose a report is and
		// cannot see the grant that says who triages. ReporterAuthorizer is the
		// narrow half — a deployment with a triage console composes it with
		// whatever says this caller is a triager.
		srv, err := issuereportsgrpc.NewServer(store, client, principals,
			issuereportsgrpc.ReporterAuthorizer{}, issuereportsgrpc.WithPillars(pillars))
		if err != nil {
			return err
		}

		// Fail-closed: a method declared nowhere is denied, so the fragment is
		// composed rather than approximated.
		reqs, err := issuereportsgrpc.Require(authzgrpc.NewRequirements()).Build()
		if err != nil {
			return err
		}

		enforcer, err := authzgrpc.NewEnforcer(reqs, grants)
		if err != nil {
			return err
		}

		// Not optional and not made by this package: without it a report
		// somebody else already resolved arrives as codes.Unknown rather than as
		// the conflict a console re-reads on.
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

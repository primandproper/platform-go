package grpc_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v14/errormappers"
	waitlistscfg "github.com/primandproper/platform-go/v14/waitlists/config"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
	"github.com/primandproper/primitives-go/v2/database"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/observability"
	grpcserver "github.com/primandproper/primitives-go/v2/server/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"google.golang.org/grpc"
)

// Example_mount is the acceptance test for this package's seams: a store, a
// permission fragment, a mount, and the three things only a consumer can supply.
//
// The third of those is the one this surface has and the ones before it did not.
// A signup page is reached by somebody who has not signed in, so the tenant an
// anonymous request is against comes off the connection — and the standing to
// withdraw a signup, which is public and names a row, comes off whatever the
// consumer accepts as proof that the person asking is the person who signed up.
func Example_mount() {
	var (
		ctx        context.Context
		cfg        *waitlistscfg.Config             // yours: a config block
		client     database.Client                  //
		pillars    *observability.Pillars           //
		serverCfg  *grpcserver.Config               //
		principals waitlistsgrpc.PrincipalExtractor // yours: who is calling
		authn      grpc.UnaryServerInterceptor      // yours: what puts them on the context
		grants     authorization.GrantsExtractor    // yours: their authority
		scopes     waitlistsgrpc.ScopeResolver      // yours: whose catalog an anonymous visitor sees
		withdrawal waitlistsgrpc.SignupAuthorizer   // yours: who may take somebody off a list
	)

	_ = func() error {
		store, err := waitlistscfg.NewStore(ctx, cfg, client, waitlistscfg.WithPillars(pillars))
		if err != nil {
			return err
		}

		srv, err := waitlistsgrpc.NewServer(store, client, principals, withdrawal,
			waitlistsgrpc.WithScopeResolver(scopes),
			waitlistsgrpc.WithPillars(pillars),
		)
		if err != nil {
			return err
		}

		// The permissions, in one call: the administrative fourteen with what
		// they need, and the signup page's three as public. Compose other
		// domains onto the same builder before Build.
		reqs, err := waitlistsgrpc.Require(authzgrpc.NewRequirements()).Build()
		if err != nil {
			return err
		}

		enforcer, err := authzgrpc.NewEnforcer(reqs, grants)
		if err != nil {
			return err
		}

		// The error mappings, for the whole domain tier. Without this call every
		// refusal on the signup page arrives as codes.Unknown. service.Register
		// makes it for a service built from a service.Config.
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

// ExampleSignupAuthorizerFunc is the seam a consumer answers most often, written
// the way it is usually answered: an unsubscribe URL carries a single-use token,
// the consumer's own interceptor redeems it and puts what it named on the
// context, and this compares that against the signup the request names.
//
// platform-go/links is what mints and redeems such a token. Nothing here imports
// it, because how a person proves they are themselves is the consumer's.
func ExampleSignupAuthorizerFunc() {
	// yours: what the interceptor that redeemed the link put on the context.
	var redeemedSignupID func(context.Context) (string, bool)

	withdrawal := waitlistsgrpc.SignupAuthorizerFunc(
		func(ctx context.Context, caller waitlistsgrpc.Principal, _ tenancy.Scope, _, signupID string) error {
			// A signed-in caller is one answer, where the deployment's
			// unsubscribe page sits behind a sign-in.
			if caller != nil && caller.UserID() != "" {
				return nil
			}

			redeemed, ok := redeemedSignupID(ctx)
			if !ok || redeemed != signupID {
				return waitlistsgrpc.ErrTargetNotPermitted
			}

			return nil
		})

	var _ waitlistsgrpc.SignupAuthorizer = withdrawal

	fmt.Println("answered")
	// Output: answered
}

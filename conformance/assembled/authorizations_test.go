package assembled_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/primandproper/platform-go/v15/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v15/authentication/oauth2clients/authserver"
	"github.com/primandproper/platform-go/v15/authentication/oauth2serverstore"
	"github.com/primandproper/platform-go/v15/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v15/authentication/signin/grpc"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/samber/do/v2"
)

// authorizationIssuer is the authorization server's identity. Nothing resolves
// it: the suite compares the endpoints the discovery document names by their
// paths, which is what a deployment behind a proxy needs it to do.
const authorizationIssuer = "https://conformance.example"

// registerAuthorizationServer builds the authorization server the way
// oauth2clients/authserver's documentation says a deployment whose clients come
// from the registry builds it, and registers it where service mounts it from.
//
// By hand rather than through Config.OAuth2Server, because that block
// registers the protocol store under oauth2server.Store and builds the server
// over it, and a registry deployment's server reads a decorator over that store
// instead. Same key, so a block and a decorator cannot both be registered — the
// harness builds oauth2clients by hand for the same reason.
//
// A person signs in at /authorize the way a person signs in everywhere else in
// this harness: with a token the sign-in service minted, read off the request
// by the extractor's middleware. That is the resolver, wrapped in the registry's
// guard so a registration admits only a person in the registry that issued it.
// The form behind it is the registry's authenticator over sign-in, which no
// assertion reaches but which the server requires.
func registerAuthorizationServer(i do.Injector, prefix string, extractor *signingrpc.PrincipalExtractor) {
	do.Provide(i, func(i do.Injector) (*oauth2server.Server, error) {
		db := do.MustInvoke[database.Client](i)
		registry := do.MustInvoke[oauth2clients.Store](i)

		protocol, err := oauth2serverstore.NewStore(&oauth2serverstore.Config{TablePrefix: prefix}, db)
		if err != nil {
			return nil, err
		}

		store, err := authserver.NewStore(protocol, registry, db)
		if err != nil {
			return nil, err
		}

		form, err := authserver.NewAuthenticator(do.MustInvoke[*signin.Service](i), registry, db)
		if err != nil {
			return nil, err
		}

		signedIn, err := authserver.NewGuardedResolver(authserver.ScopedSubjectResolverFunc(
			func(ctx context.Context, _ *http.Request) (*oauth2server.Subject, tenancy.Scope, error) {
				principal, ok := extractor.Extract(ctx)
				if !ok {
					return nil, tenancy.Scope{}, nil
				}

				return &oauth2server.Subject{ID: principal.UserID()}, principal.Scope(), nil
			}), registry, db)
		if err != nil {
			return nil, err
		}

		return oauth2server.NewServer(authorizationIssuer, store, form,
			oauth2server.WithSubjectResolver(signedIn),
			oauth2server.WithDynamicRegistration(false))
	})
}

// approvals is Actions.Authorized: the person, signed in with the token the
// harness minted for them, POSTs the authorization request with no body, and
// the resolver above answers for them.
type approvals struct {
	tokens sync.Map
}

// remember records the token a subject was minted, which is what that person
// approves a request with.
func (a *approvals) remember(userID, token string) {
	a.tokens.Store(userID, token)
}

func (a *approvals) authorize(ctx context.Context, _ tenancy.Scope, userID, authorizeURL string) (string, error) {
	token, ok := a.tokens.Load(userID)
	if !ok {
		return "", fmt.Errorf("no subject %q was minted in this run", userID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authorizeURL, http.NoBody)
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+token.(string))

	// The redirect names the client's host, which does not resolve; it is read,
	// not followed.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	res, err := client.Do(req)
	if err != nil {
		return "", err
	}

	if closeErr := res.Body.Close(); closeErr != nil {
		return "", closeErr
	}

	if res.StatusCode != http.StatusFound {
		return "", fmt.Errorf("the authorization request answered %d rather than a redirect", res.StatusCode)
	}

	return res.Header.Get("Location"), nil
}

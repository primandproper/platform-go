package grpc_test

import (
	"context"
	"maps"
	"net"
	"net/http"
	"testing"
	"time"

	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	signinclient "github.com/primandproper/platform-go/v14/authentication/signin/grpc/client"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// The resource the suite's verifier guards, and a sibling an access token can
// be minted for instead.
const (
	thisResource    = "https://api.example.com"
	siblingResource = "https://mcp.example.com"
)

// accessTokenStore is the authorization server's token lookup, held as a map
// from bearer to the record behind it. A bearer it does not hold is
// oauth2server.ErrNotFound, as Server.Authenticate answers one.
type accessTokenStore map[string]*oauth2server.AccessToken

func (s accessTokenStore) Authenticate(_ context.Context, bearer string) (*oauth2server.AccessToken, error) {
	token, ok := s[bearer]
	if !ok {
		return nil, oauth2server.ErrNotFound
	}

	return token, nil
}

// brokenTokenStore is a token store that cannot be read.
type brokenTokenStore struct{}

func (brokenTokenStore) Authenticate(context.Context, string) (*oauth2server.AccessToken, error) {
	return nil, platformerrors.New("the token store's database is down")
}

// accessToken is a live token for subject, minted for audience with scopes.
func accessToken(subject string, audience []string, scopes ...string) *oauth2server.AccessToken {
	now := time.Now()

	return &oauth2server.AccessToken{
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Hour),
		ClientID:  "a-third-party-client",
		Subject:   oauth2server.Subject{ID: subject},
		Scopes:    scopes,
		Audience:  audience,
	}
}

// verifierOver builds the verifier a resource server at thisResource holds.
func verifierOver(t *testing.T, tokens oauth2server.TokenAuthenticator) *oauth2server.Verifier {
	t.Helper()

	md, err := oauth2server.NewResourceMetadata(thisResource, []string{"https://auth.example.com"})
	must.NoError(t, err)

	v, err := oauth2server.NewVerifier(md, tokens)
	must.NoError(t, err)

	return v
}

// inTestScope reads every access token's subject in the suite's directory.
func inTestScope(context.Context, *oauth2server.AccessToken) (tenancy.Scope, error) {
	return testScope, nil
}

func TestPrincipalExtractor_WithAccessTokens(T *testing.T) {
	T.Parallel()

	h := newExtractorHarness(T)

	tokens := accessTokenStore{
		"member-token":     accessToken(h.member.User.ID, []string{thisResource}, "recipes:read"),
		"operator-token":   accessToken(h.admin.User.ID, []string{thisResource}, "recipes:read"),
		"sibling-token":    accessToken(h.member.User.ID, []string{siblingResource}, "recipes:read"),
		"no-audience":      accessToken(h.member.User.ID, nil, "recipes:read"),
		"read-only-token":  accessToken(h.member.User.ID, []string{thisResource}),
		"nobody-token":     accessToken("no-such-user", []string{thisResource}, "recipes:read"),
		"other-dir-token":  accessToken(h.staff.User.ID, []string{thisResource}, "recipes:read"),
		"global-dir-token": accessToken(h.member.User.ID, []string{thisResource}, "recipes:read"),
	}

	verifier := verifierOver(T, tokens)

	extractor := func(t *testing.T, opts ...signingrpc.ExtractorOption) *signingrpc.PrincipalExtractor {
		t.Helper()

		return h.extractor(t, append([]signingrpc.ExtractorOption{
			signingrpc.WithAccessTokens(verifier, "recipes:read"),
			signingrpc.WithAccessTokenScope(inTestScope),
		}, opts...)...)
	}

	T.Run("an access token names its subject, in their default account", func(t *testing.T) {
		t.Parallel()

		saw := serveThrough(t, extractor(t), "Bearer member-token")
		test.EqOp(t, http.StatusNoContent, saw.code)
		must.NotNil(t, saw.principal)

		caller, ok := saw.principal.(*signingrpc.AccessTokenCaller)
		must.True(t, ok)
		test.EqOp(t, h.member.User.ID, caller.UserID())
		test.Eq(t, testScope, caller.Scope())
		test.EqOp(t, h.member.Account.ID, caller.ActiveAccountID())
		test.EqOp(t, "a-third-party-client", caller.Token().ClientID)
		test.EqOp(t, h.member.User.ID, caller.Identity().User.ID)
	})

	T.Run("an access token's caller claims no operator and no sign-in", func(t *testing.T) {
		t.Parallel()

		saw := serveThrough(t, extractor(t), "Bearer member-token")
		must.NotNil(t, saw.principal)

		_, delegated := saw.principal.(callers.Delegated)
		test.False(t, delegated)

		_, family := saw.principal.(signingrpc.FamilyIdentifier)
		test.False(t, family)
	})

	T.Run("an access token carries none of its subject's service roles", func(t *testing.T) {
		t.Parallel()

		saw := serveThrough(t, extractor(t, signingrpc.WithGrants(rolePolicy)), "Bearer operator-token")
		must.NotNil(t, saw.principal)

		caller, ok := saw.principal.(*signingrpc.AccessTokenCaller)
		must.True(t, ok)
		test.SliceEmpty(t, caller.Identity().ServiceRoles())

		must.True(t, saw.granted)
		test.True(t, saw.grants.Has(memberGrant))
		test.False(t, saw.grants.Has(operatorGrant))
	})

	T.Run("the grants come from the deployment's resolver, handed the access token's caller", func(t *testing.T) {
		t.Parallel()

		var handed callers.Principal

		e := extractor(t, signingrpc.WithGrants(func(_ context.Context, p callers.Principal) (authorization.Grants, error) {
			handed = p

			return authorization.NewGrants(authorization.NewPermissionSet(memberGrant)), nil
		}))

		saw := serveThrough(t, e, "Bearer member-token")
		must.True(t, saw.granted)
		test.True(t, saw.grants.Has(memberGrant))

		_, ok := handed.(*signingrpc.AccessTokenCaller)
		test.True(t, ok)
	})

	T.Run("a sign-in token still names its caller", func(t *testing.T) {
		t.Parallel()

		saw := serveThrough(t, extractor(t), "Bearer "+h.issue(t, h.member, false).Token)
		must.NotNil(t, saw.principal)

		_, ok := saw.principal.(*signingrpc.Caller)
		test.True(t, ok)
	})

	T.Run("the subject is read in tenancy.Global unless the deployment says otherwise", func(t *testing.T) {
		t.Parallel()

		// The suite's member lives in testScope, so the global directory has
		// never heard of them.
		saw := serveThrough(t, h.extractor(t, signingrpc.WithAccessTokens(verifier)), "Bearer global-dir-token")
		test.EqOp(t, http.StatusNoContent, saw.code)
		test.Nil(t, saw.principal)
	})

	T.Run("the directory the deployment names is the one the subject is read in", func(t *testing.T) {
		t.Parallel()

		var askedAbout string

		e := h.extractor(t,
			signingrpc.WithAccessTokens(verifier),
			signingrpc.WithAccessTokenScope(func(_ context.Context, token *oauth2server.AccessToken) (tenancy.Scope, error) {
				askedAbout = token.Subject.ID

				return staffScope, nil
			}),
		)

		saw := serveThrough(t, e, "Bearer other-dir-token")
		must.NotNil(t, saw.principal)
		test.EqOp(t, h.staff.User.ID, saw.principal.UserID())
		test.Eq(t, staffScope, saw.principal.Scope())
		test.EqOp(t, h.staff.User.ID, askedAbout)
	})

	T.Run("a scope decision that fails is an outage, and one refusing the token is not", func(t *testing.T) {
		t.Parallel()

		failing := h.extractor(t,
			signingrpc.WithAccessTokens(verifier),
			signingrpc.WithAccessTokenScope(func(context.Context, *oauth2server.AccessToken) (tenancy.Scope, error) {
				return tenancy.Global(), platformerrors.New("the claims service is down")
			}),
		)
		test.EqOp(t, http.StatusServiceUnavailable, serveThrough(t, failing, "Bearer member-token").code)

		refusing := h.extractor(t,
			signingrpc.WithAccessTokens(verifier),
			signingrpc.WithAccessTokenScope(func(context.Context, *oauth2server.AccessToken) (tenancy.Scope, error) {
				return tenancy.Global(), platformerrors.Wrap(signingrpc.ErrUnauthenticated, "no directory claim")
			}),
		)
		saw := serveThrough(t, refusing, "Bearer member-token")
		test.EqOp(t, http.StatusNoContent, saw.code)
		test.Nil(t, saw.principal)
	})

	// The token the authorization server does not hold is the one refusal that
	// says nothing about whose credential it was.
	T.Run("a bearer the authorization server does not hold goes to the fallback", func(t *testing.T) {
		t.Parallel()

		e := extractor(t, signingrpc.WithFallback(func(context.Context) (callers.Principal, bool) {
			return &testPrincipal{userID: "legacy", scope: tenancy.Global()}, true
		}))

		saw := serveThrough(t, e, "Bearer an-expired-or-unknown-token")
		must.NotNil(t, saw.principal)
		test.EqOp(t, "legacy", saw.principal.UserID())
	})

	T.Run("no refusal of an access token the server holds goes to the fallback", func(t *testing.T) {
		t.Parallel()

		fresh := newExtractorHarness(t)
		banned := accessToken(fresh.member.User.ID, []string{thisResource}, "recipes:read")

		must.NoError(t, fresh.db.WithTransaction(t.Context(), func(tx database.Tx) error {
			return fresh.store.UpdateUserAccountStatus(t.Context(), tx, testScope, fresh.member.User.ID, identity.StatusBanned, "")
		}))

		freshTokens := accessTokenStore{"banned-token": banned}
		maps.Copy(freshTokens, tokens)

		consulted := false
		e := fresh.extractor(t,
			signingrpc.WithAccessTokens(verifierOver(t, freshTokens), "recipes:read"),
			signingrpc.WithAccessTokenScope(inTestScope),
			signingrpc.WithFallback(func(context.Context) (callers.Principal, bool) {
				consulted = true

				return &testPrincipal{userID: "legacy"}, true
			}),
		)

		for _, bearer := range []string{"sibling-token", "no-audience", "read-only-token", "nobody-token", "banned-token"} {
			saw := serveThrough(t, e, "Bearer "+bearer)
			test.Nil(t, saw.principal, test.Sprintf("%s named somebody", bearer))
		}

		test.False(t, consulted)
	})

	T.Run("HTTPMiddleware answers each refusal with its own status", func(t *testing.T) {
		t.Parallel()

		e := extractor(t)

		// A token for somewhere else, or for nobody, proceeds as nobody, as
		// any credential naming nobody does.
		for _, bearer := range []string{"sibling-token", "no-audience", "nobody-token"} {
			saw := serveThrough(t, e, "Bearer "+bearer)
			test.EqOp(t, http.StatusNoContent, saw.code, test.Sprintf("%s", bearer))
			test.Nil(t, saw.principal)
		}

		// A good token without the scope is not "not signed in".
		test.EqOp(t, http.StatusForbidden, serveThrough(t, e, "Bearer read-only-token").code)

		broken := h.extractor(t, signingrpc.WithAccessTokens(verifierOver(t, brokenTokenStore{})))
		test.EqOp(t, http.StatusServiceUnavailable, serveThrough(t, broken, "Bearer member-token").code)

		down, err := signingrpc.NewPrincipalExtractor(h.signer, h.db, failingDirectory{},
			signingrpc.WithAccessTokens(verifier), signingrpc.WithAccessTokenScope(inTestScope))
		must.NoError(t, err)
		test.EqOp(t, http.StatusServiceUnavailable, serveThrough(t, down, "Bearer member-token").code)
	})

	T.Run("a nil verifier and a nil scope decision are ignored", func(t *testing.T) {
		t.Parallel()

		saw := serveThrough(t, h.extractor(t, signingrpc.WithAccessTokens(nil)), "Bearer member-token")
		test.Nil(t, saw.principal)

		saw = serveThrough(t, h.extractor(t,
			signingrpc.WithAccessTokens(verifier),
			signingrpc.WithAccessTokenScope(inTestScope),
			signingrpc.WithAccessTokenScope(nil),
		), "Bearer member-token")
		must.NotNil(t, saw.principal)
	})
}

// TestPrincipalExtractor_WithAccessTokens_interceptor is an access token on a
// real connection, through the interceptor in front of the sign-in surface.
func TestPrincipalExtractor_WithAccessTokens_interceptor(T *testing.T) {
	T.Parallel()

	h := newExtractorHarness(T)

	tokens := accessTokenStore{
		"member-token":    accessToken(h.member.User.ID, []string{thisResource}, "recipes:read"),
		"sibling-token":   accessToken(h.member.User.ID, []string{siblingResource}, "recipes:read"),
		"read-only-token": accessToken(h.member.User.ID, []string{thisResource}),
	}

	dial := func(t *testing.T, e *signingrpc.PrincipalExtractor) *signinclient.Client {
		t.Helper()

		reqs, err := signingrpc.RequireAuthentication(signingrpc.NewAuthenticationRequirements()).Build()
		must.NoError(t, err)

		srv, err := signingrpc.NewServer(h.svc, e.Extract,
			signingrpc.WithScopeResolver(func(context.Context) (tenancy.Scope, error) { return testScope, nil }))
		must.NoError(t, err)

		grpcServer := grpc.NewServer(grpc.ChainUnaryInterceptor(
			grpcerrors.UnaryErrorEncodingInterceptor(),
			e.UnaryServerInterceptor(reqs),
		))
		srv.RegisterOn(grpcServer)

		listener := bufconn.Listen(1 << 20)

		go func() { _ = grpcServer.Serve(listener) }()

		t.Cleanup(grpcServer.Stop)

		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return listener.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			signinclient.DefaultInterceptors(),
		)
		must.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		return signinclient.Wrap(conn)
	}

	bearer := func(ctx context.Context, token string) context.Context {
		return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}

	client := dial(T, h.extractor(T,
		signingrpc.WithAccessTokens(verifierOver(T, tokens), "recipes:read"),
		signingrpc.WithAccessTokenScope(inTestScope),
	))

	T.Run("an access token reaches a method that requires a caller", func(t *testing.T) {
		t.Parallel()

		self, err := client.GetSelf(bearer(t.Context(), "member-token"), &signinpb.GetSelfRequest{})
		must.NoError(t, err)
		test.EqOp(t, h.member.User.ID, self.GetUser().GetId())
	})

	T.Run("a token minted for a sibling resource is unauthenticated", func(t *testing.T) {
		t.Parallel()

		_, err := client.GetSelf(bearer(t.Context(), "sibling-token"), &signinpb.GetSelfRequest{})
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})

	T.Run("a token lacking a required scope is permission denied, even where a caller is optional", func(t *testing.T) {
		t.Parallel()

		_, err := client.GetSelf(bearer(t.Context(), "read-only-token"), &signinpb.GetSelfRequest{})
		test.EqOp(t, codes.PermissionDenied, status.Code(err))

		_, err = client.GetAuthStatus(bearer(t.Context(), "read-only-token"), &signinpb.GetAuthStatusRequest{})
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})

	T.Run("a token store outage is unavailable, not unauthenticated", func(t *testing.T) {
		t.Parallel()

		broken := dial(t, h.extractor(t, signingrpc.WithAccessTokens(verifierOver(t, brokenTokenStore{}))))

		_, err := broken.GetSelf(bearer(t.Context(), "member-token"), &signinpb.GetSelfRequest{})
		test.EqOp(t, codes.Unavailable, status.Code(err))
	})

	T.Run("a banned subject's access token is refused as the person", func(t *testing.T) {
		t.Parallel()

		fresh := newExtractorHarness(t)

		must.NoError(t, fresh.db.WithTransaction(t.Context(), func(tx database.Tx) error {
			return fresh.store.UpdateUserAccountStatus(t.Context(), tx, testScope, fresh.member.User.ID, identity.StatusBanned, "")
		}))

		e, err := signingrpc.NewPrincipalExtractor(fresh.signer, fresh.db, fresh.store,
			signingrpc.WithAccessTokens(verifierOver(t, accessTokenStore{
				"banned-token": accessToken(fresh.member.User.ID, []string{thisResource}),
			})),
			signingrpc.WithAccessTokenScope(inTestScope),
		)
		must.NoError(t, err)

		banned := dial(t, e)

		_, err = banned.GetSelf(bearer(t.Context(), "banned-token"), &signinpb.GetSelfRequest{})
		test.EqOp(t, codes.PermissionDenied, status.Code(err))

		_, err = banned.GetAuthStatus(bearer(t.Context(), "banned-token"), &signinpb.GetAuthStatusRequest{})
		test.EqOp(t, codes.PermissionDenied, status.Code(err))

		test.EqOp(t, http.StatusForbidden, serveThrough(t, e, "Bearer banned-token").code)
	})
}

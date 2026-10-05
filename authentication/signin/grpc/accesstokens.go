package grpc

import (
	"context"
	"errors"
	"slices"

	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// AccessTokenScope decides which directory an OAuth2 access token's subject is
// read in. See WithAccessTokenScope.
type AccessTokenScope func(ctx context.Context, token *oauth2server.AccessToken) (tenancy.Scope, error)

// AccessTokenCaller is who an OAuth2 access token turned back into: the
// principal a PrincipalExtractor built WithAccessTokens puts on a request whose
// bearer is a token oauth2server minted for this resource.
//
// It satisfies callers.Principal, so every surface in this module reads it
// unchanged, and it is handed to the deployment's GrantsResolver as a *Caller
// is. It deliberately satisfies neither callers.Delegated nor FamilyIdentifier:
// an access token never carries an operator acting through it, and its login
// is the authorization server's refresh-token family rather than a sign-in, so
// a principal answering either with an empty value would be claiming to have
// looked.
type AccessTokenCaller struct {
	principal *identity.Principal
	token     *oauth2server.AccessToken
	scope     tenancy.Scope
}

var _ callers.Principal = (*AccessTokenCaller)(nil)

// UserID is the token's subject.
func (c *AccessTokenCaller) UserID() string { return c.principal.User.ID }

// Scope is the directory the subject was read in, which WithAccessTokenScope
// decided.
func (c *AccessTokenCaller) Scope() tenancy.Scope { return c.scope }

// ActiveAccountID is the subject's default account, as the directory resolved
// it on this request. See WithAccessTokens for why it is not one the token
// names.
func (c *AccessTokenCaller) ActiveAccountID() string { return c.principal.ActiveAccountID }

// Identity is the directory's answer for this request: the user, redacted,
// their memberships and the active account, with the service roles an
// ordinary-door sign-in keeps — see WithAccessTokens.
func (c *AccessTokenCaller) Identity() *identity.Principal { return c.principal }

// Token is the access token the request carried, as the verifier read it back:
// the client it was issued to and the scopes it was granted, which is what a
// method deciding per-call scopes reads. It is the extractor's copy and is not
// to be modified.
func (c *AccessTokenCaller) Token() *oauth2server.AccessToken { return c.token }

// WithAccessTokens makes the extractor accept OAuth2 access tokens as well as
// the tokens signin minted: a bearer that is not a sign-in token is handed to
// verifier, and one it accepts names the user who is its subject. A nil
// verifier is ignored, leaving access tokens unaccepted.
//
// requiredScopes are the scopes every access token must hold to name anybody
// here. Naming none accepts any token minted for this resource, which is what
// a deployment whose per-method permissions are its GrantsResolver's wants.
//
// # What it decides
//
// Everything Verifier.Verify does — the token is live, its RFC 8707 audience
// names the verifier's resource, it holds requiredScopes — and then that the
// directory still admits its subject. The resource is the one the verifier's
// own metadata publishes, so it is never a second spelling of that string, and
// a token minted with no audience is refused as the verifier refuses it.
//
// The answers are the extractor's for a sign-in token, with one more. A token
// the authorization server does not hold — expired, revoked, or not an access
// token at all — is not this option's, and goes to WithFallback. Any other
// refusal does not: a token minted for a different resource, or for a subject
// the directory refuses, is still an access token, and a fallback that could
// answer for it would be a second opinion on the refusal. A banned subject is
// codes.PermissionDenied, as a banned signer-in is. And a live token for this
// resource lacking one of requiredScopes is codes.PermissionDenied (403 over
// HTTPMiddleware) whatever the method requires: it is a good credential that
// is not allowed to do this, which a client fixes by asking for the scope
// rather than by signing in again.
//
// # Whose account
//
// The token's subject acts in their default account, as the directory
// resolves it on each request, and not in one the token names. An access token
// is opaque and outlives the account switch a user makes from a session, and
// nothing can re-mint it when they do; pinning it to an account would mean a
// switch silently not applying until the next authorization. The directory the
// subject is read in is WithAccessTokenScope's.
//
// # What it carries
//
// Service roles, as an ordinary-door sign-in does: none, unless
// WithOrdinaryServiceRoles keeps some. A third-party client acting for an
// operator has skipped the administrative door entirely, and is the last
// credential operator permissions belong on.
func WithAccessTokens(verifier *oauth2server.Verifier, requiredScopes ...string) ExtractorOption {
	return func(e *PrincipalExtractor) {
		if verifier != nil {
			e.accessTokens = verifier
			e.accessTokenScopes = slices.Clone(requiredScopes)
		}
	}
}

// WithAccessTokenScope names which directory an access token's subject is read
// in. Absent reads every subject in tenancy.Global, which is the directory of a
// deployment with one; a deployment with several says how a token names its
// own — typically from a claim the authorization server's subject
// authenticator recorded in its Subject.Claims. A nil function is ignored,
// leaving the default.
//
// An error it returns wrapping ErrUnauthenticated refuses the token; any other
// is reported as an outage, as a directory that could not be read is.
func WithAccessTokenScope(scopeOf AccessTokenScope) ExtractorOption {
	return func(e *PrincipalExtractor) {
		if scopeOf != nil {
			e.accessTokenScope = scopeOf
		}
	}
}

// globalAccessTokenScope is the default AccessTokenScope.
func globalAccessTokenScope(context.Context, *oauth2server.AccessToken) (tenancy.Scope, error) {
	return tenancy.Global(), nil
}

// authenticateAccessToken resolves one bearer as an OAuth2 access token.
//
// An error wrapping oauth2server.ErrNotFound is a token the authorization
// server does not hold, and the only refusal that says nothing about whether
// the bearer was an access token; an error wrapping
// oauth2server.ErrInsufficientScope is a good token without the scope; one
// wrapping ErrUnauthenticated is any other refusal; and any other error is a
// store or directory that could not answer.
func (e *PrincipalExtractor) authenticateAccessToken(ctx context.Context, bearer string) (*AccessTokenCaller, error) {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	token, err := e.accessTokens.Verify(ctx, bearer, e.accessTokenScopes...)
	switch {
	case err == nil:
	case errors.Is(err, oauth2server.ErrNotFound), errors.Is(err, oauth2server.ErrInsufficientScope):
		return nil, err
	case errors.Is(err, oauth2server.ErrTokenAudienceMismatch), errors.Is(err, oauth2server.ErrNoBearerToken):
		return nil, platformerrors.Join(ErrUnauthenticated, err)
	default:
		return nil, op.Error(err, "verifying an access token")
	}

	userID := token.Subject.ID
	if userID == "" {
		return nil, op.Error(platformerrors.Wrap(ErrUnauthenticated, "an access token with no subject"), "reading an access token")
	}

	scope, err := e.accessTokenScope(ctx, token)
	if err != nil {
		return nil, op.Error(err, "deciding an access token's directory")
	}

	op.Set(scopeKey, scope.String())
	op.Set(userIDKey, userID)

	principal, err := e.directory.GetPrincipal(ctx, e.client.Reader(), scope, userID, "")
	if err != nil {
		if refusesTheCaller(err) {
			return nil, op.Error(platformerrors.Join(ErrUnauthenticated, err), "resolving an access token's caller")
		}

		return nil, op.Error(err, "resolving an access token's caller")
	}

	if principal == nil || principal.User == nil {
		return nil, op.Error(platformerrors.Wrap(ErrUnauthenticated, "the directory answered with nobody"), "resolving an access token's caller")
	}

	return &AccessTokenCaller{
		principal: e.withOrdinaryServiceRoles(ctx, principal),
		token:     token,
		scope:     scope,
	}, nil
}

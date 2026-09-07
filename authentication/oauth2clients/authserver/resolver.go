package authserver

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2server"
	"github.com/primandproper/platform-go/v14/database"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
)

var _ oauth2server.SubjectResolver = (*GuardedResolver)(nil)

// GuardedResolver wraps a consumer's own oauth2server.SubjectResolver so that
// the subject it reports is one the named registration admits.
//
// # Why this exists, and why it is not optional
//
// A SubjectResolver is how a request that already carries proof of who somebody
// is — a session cookie, a bearer token a first-party client holds — reaches an
// authorization code without meeting a login form. oauth2server consults it
// *before* the form and short-circuits on a non-nil subject.
//
// That is exactly the shape of a bypass. A deployment that wires
// [NewAuthenticator] for the form and its own resolver for signed-in users has
// put oauth2clients.Client.Admits on one of the two paths to a code, and the
// other path is the one its own first-party application takes. Wrapping the
// resolver is what closes it, and it is why this type is in the same package as
// the authenticator rather than left to a consumer to remember.
//
// # Why a refusal is (nil, nil) and not an error
//
// This is forced by oauth2server, and getting it backwards produces a worse
// failure than not checking at all.
//
// Server.resolveSubject turns *any* resolver error into a server_error and a
// redirect, with no form rendered — deliberately, because a caller who
// presented a credential has nothing to type. So a resolver that reported the
// mismatch as an error would replace an actionable page with an opaque failure
// at the client's redirect URI.
//
// Answering (nil, nil) means "not one of mine", which is the contract's own
// wording for a credential this resolver does not recognize. The request falls
// through to the login form, the person signs in, and [Authenticator] makes the
// same check and gives them the written answer. The guarantee is identical and
// the person gets a page instead of a redirect.
//
// A broken registry is still an error. That is not a refused credential, and
// there is no form that fixes it.
type GuardedResolver struct {
	inner    oauth2server.SubjectResolver
	registry oauth2clients.Store
	client   database.Client
	scopes   ScopeResolver
}

// NewGuardedResolver wraps a resolver with the registration check.
//
// The inner resolver is the consumer's: this package has no opinion about what
// a session looks like, which is why there is no token parser here and no
// interface for one. What it adds is the one comparison the authorization
// server cannot make for itself.
func NewGuardedResolver(
	inner oauth2server.SubjectResolver,
	registry oauth2clients.Store,
	client database.Client,
	opts ...ResolverOption,
) (*GuardedResolver, error) {
	if inner == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil subject resolver")
	}

	if registry == nil {
		return nil, oauth2clients.ErrNilStore
	}

	if client == nil {
		return nil, oauth2clients.ErrNilDatabaseClient
	}

	r := &GuardedResolver{
		inner:    inner,
		registry: registry,
		client:   client,
		scopes:   GlobalScope,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	return r, nil
}

// ResolveSubject implements oauth2server.SubjectResolver.
func (r *GuardedResolver) ResolveSubject(
	ctx context.Context,
	req *http.Request,
) (*oauth2server.Subject, error) {
	subject, err := r.inner.ResolveSubject(ctx, req)
	if err != nil || subject == nil {
		// The inner resolver's answer, unchanged. It declined, or it broke, and
		// this wrapper has nothing to add to either.
		return subject, err
	}

	scope, err := r.scopes(ctx, req)
	if err != nil {
		return nil, platformerrors.Wrap(err, "resolving the scope of an authorization request")
	}

	clientID := req.FormValue("client_id")
	if clientID == "" {
		// No client named. The authorization server has already refused the
		// request; duplicating that refusal here would be a second place
		// deciding what a malformed request is.
		return subject, nil
	}

	registered, err := r.registry.ResolveClientID(ctx, r.client.Reader(), clientID)
	if err != nil {
		if platformerrors.Is(err, oauth2clients.ErrClientNotFound) {
			// The server's own lookup has already answered for an unknown
			// client. Declining here rather than erroring keeps this wrapper
			// from being the thing that decides what an unknown client_id means.
			return nil, nil //nolint:nilnil // "not one of mine" is the contract's own answer; see the type documentation.
		}

		return nil, platformerrors.Wrapf(err, "resolving oauth2 client %q", clientID)
	}

	if err = registered.Admits(scope, subject.ID); err != nil {
		// Decline rather than refuse. See the type documentation: an error here
		// becomes a server_error redirect, and declining sends the person to the
		// form where the authenticator makes the same check and can say why.
		return nil, nil //nolint:nilnil,nilerr // Declining is the contract's answer to a refused registration; see the type documentation.
	}

	return subject, nil
}

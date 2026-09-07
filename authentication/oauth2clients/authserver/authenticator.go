package authserver

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2server"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/database"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	"github.com/primandproper/platform-go/v14/tenancy"
)

// ClaimAccountID is the claim an issued subject carries the signed-in account
// under.
//
// It is exported because it is a contract between two halves a consumer wires
// separately: this seam writes it, and whatever the resource server hands a
// tool or a handler reads it back. Spelled in two files it could disagree, and
// the symptom would be a token that authorizes somebody for no account.
const ClaimAccountID = "account_id"

// DefaultMismatchMessage is what the login form says when the person signed in
// but this client is not one their organization — or they — may use.
//
// It is written for a person, not for a log, and it says what to do next
// without naming the registry, the owner, or any other client. Replace it with
// [WithMismatchMessage] where a deployment can say something more useful, such
// as who to ask.
const DefaultMismatchMessage = "This application is not available for your account. Ask whoever administers it for access."

var _ oauth2server.SubjectAuthenticator = (*Authenticator)(nil)

// Authenticator is oauth2server.SubjectAuthenticator over
// authentication/signin: it runs the login form step of /authorize.
//
// It does two things, in this order, and the order is the interesting part.
// First it signs the person in, through the same service and the same refusals
// every other door in the deployment uses. Then it checks the registration the
// request named against the person who just proved a password — which is
// oauth2clients.Client.Admits, and is the one place that comparison can be made.
//
// # Why the client check is second
//
// Because doing it first would answer an anonymous caller. "This client is not
// registered for your organization" is a sentence that, sent before a password,
// tells whoever asked which registrations exist and which registry they are in
// — an enumeration oracle over every tenant, reachable by anybody who can
// construct a URL. After a proven password it discloses nothing the person did
// not already have, and it is the only answer that helps them.
//
// It costs one sign-in that will not issue a code. That is the right trade: the
// consumer's Hooks.AfterSignIn records a sign-in that genuinely happened, and
// the alternative is a public endpoint that reports on the registry.
type Authenticator struct {
	signIn   *signin.Service
	registry oauth2clients.Store
	client   database.Client
	scopes   ScopeResolver
	message  string

	// administrative sends the sign-in through signin's administrative door,
	// which requires a service role and a proven second factor whatever the
	// service's policy says.
	//
	// It is for an authorization server that fronts an operator tool rather
	// than a product — a remote MCP endpoint exposing administrative actions is
	// the case this exists for — where "anybody with an account may sign in
	// here" is the wrong default and there is no client-side check that fixes
	// it.
	administrative bool
}

// NewAuthenticator builds the login-form seam.
//
// The database client is taken for Reader(), which is what the registration
// lookup runs on: /authorize is not inside a transaction of the consumer's.
//
// The scope defaults to tenancy.Global(), which is the only defensible default
// and the same one authentication/signin/grpc takes: a caller signing in has not
// become a principal yet, so there is nobody to read a scope off, and it has to
// come off the connection, the host, or a path the deployment chose. See
// [WithScopeResolver].
func NewAuthenticator(
	signIn *signin.Service,
	registry oauth2clients.Store,
	client database.Client,
	opts ...AuthenticatorOption,
) (*Authenticator, error) {
	if signIn == nil {
		return nil, oauth2clients.ErrNilService
	}

	if registry == nil {
		return nil, oauth2clients.ErrNilStore
	}

	if client == nil {
		return nil, oauth2clients.ErrNilDatabaseClient
	}

	a := &Authenticator{
		signIn:   signIn,
		registry: registry,
		client:   client,
		scopes:   GlobalScope,
		message:  DefaultMismatchMessage,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(a)
		}
	}

	return a, nil
}

// AuthenticateSubject implements oauth2server.SubjectAuthenticator.
//
// Every refusal here wraps oauth2server.ErrLoginFailed, by way of
// oauth2server.NewLoginError, so the form is re-rendered with a message rather
// than the request being ended at a redirect. That is right for both kinds of
// refusal this method makes: a person who typed the wrong password is still
// here and can try again, and a person whose organization may not use this
// client is still here and needs to be told so.
//
// A broken directory or a broken registry is the exception and is returned
// bare, which fails the request. Re-rendering a form against a database that is
// down produces somebody who tries four times and then files a support ticket.
func (a *Authenticator) AuthenticateSubject(
	ctx context.Context,
	req *http.Request,
) (*oauth2server.Subject, error) {
	scope, err := a.scopes(ctx, req)
	if err != nil {
		return nil, platformerrors.Wrap(err, "resolving the scope of an authorization request")
	}

	credentials := &signin.Credentials{
		Username: req.FormValue(oauth2server.FieldUsername),
		Password: req.FormValue(oauth2server.FieldPassword),
		TOTPCode: req.FormValue(oauth2server.FieldTOTPCode),
	}

	// signin collapses four refusals into one sentinel on purpose — telling an
	// unknown handle from a wrong password tells an attacker which half of the
	// guess was right — so this hands its message straight to the form rather
	// than deciding anything of its own.
	completed, err := a.login(ctx, scope, credentials)
	if err != nil {
		if platformerrors.Is(err, signin.ErrInvalidCredentials) {
			return nil, oauth2server.NewLoginError(oauth2server.DefaultLoginFailureMessage, err)
		}

		// Every other signin sentinel is one the person can act on and whose
		// wording was written for them — a second factor is required, the
		// account is suspended, verification is incomplete. Those reach the form
		// as themselves.
		return nil, oauth2server.NewLoginError(err.Error(), err)
	}

	principal := completed.Principal

	if err = a.admits(ctx, req, scope, principal.User.ID); err != nil {
		return nil, err
	}

	return &oauth2server.Subject{
		ID:     principal.User.ID,
		Claims: map[string]string{ClaimAccountID: principal.ActiveAccountID},
	}, nil
}

// login sends the credentials through whichever of signin's two doors this
// authenticator was built for.
func (a *Authenticator) login(
	ctx context.Context,
	scope tenancy.Scope,
	credentials *signin.Credentials,
) (*signin.SignIn, error) {
	if a.administrative {
		return a.signIn.AdminLoginForToken(ctx, scope, credentials)
	}

	return a.signIn.LoginForToken(ctx, scope, credentials)
}

// admits checks the registration the request named against the person who just
// signed in.
//
// A request naming no client is left alone: the authorization server has
// already refused it, before this seam was reached, and duplicating that
// refusal here would be a second place deciding what a malformed request is.
//
// A client_id that resolves to nothing is also left alone, for the same reason
// — the server's own lookup runs through [Store.GetClient] and has already
// answered. What this adds is the one comparison neither of those can make.
func (a *Authenticator) admits(
	ctx context.Context,
	req *http.Request,
	scope tenancy.Scope,
	userID string,
) error {
	clientID := req.FormValue("client_id")
	if clientID == "" {
		return nil
	}

	registered, err := a.registry.ResolveClientID(ctx, a.client.Reader(), clientID)
	if err != nil {
		if platformerrors.Is(err, oauth2clients.ErrClientNotFound) {
			return nil
		}

		// A broken registry fails the request rather than re-rendering the
		// form. There is nothing to type that would fix it.
		return platformerrors.Wrapf(err, "resolving oauth2 client %q", clientID)
	}

	if err = registered.Admits(scope, userID); err != nil {
		// One message for both refusals, deliberately. A person who may not use
		// this client has the same thing to do next whether the reason is their
		// organization or another person's ownership, and telling them which
		// would say that this client belongs to somebody.
		return oauth2server.NewLoginError(a.message, err)
	}

	return nil
}

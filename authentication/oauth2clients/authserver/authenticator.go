package authserver

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/authentication/oauth2server"
	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/tenancy"
)

// authenticatorName scopes the authenticator's spans, logger and instruments.
const authenticatorName = "oauth2clients_authserver_authenticator"

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
// It costs one authentication that will not issue a code — a password proven
// for a request that may go no further. That is the right trade, and the
// alternative is a public endpoint that reports on the registry.
//
// It costs no token. This seam wants a subject, not a credential, so it takes
// signin.Service.Authenticate rather than its LoginForToken: a token minted
// here would be handed to nobody and would sit in whatever the consumer indexes
// tokens by until it aged out. The consumer's signin.Hooks.AfterAuthenticate
// still records that somebody genuinely signed in, which is the half of the
// trade that mattered.
type Authenticator struct {
	// What the options wrote, kept only until the observer is built from it.
	opts observabilityOptions

	// The registry lookup this seam shares with [GuardedResolver]. See [guard]:
	// the two paths to a code run the same procedure up to Admits, and a copy of
	// it that drifted would leave the other path unguarded.
	guard

	o11y   observability.Observer
	signIn *signin.Service
	scopes ScopeResolver

	instruments *metrics.OperationSet

	message string

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
//
// Observability is optional and defaults to nothing: an unconfigured
// authenticator logs to a noop logger, traces to a noop provider and counts
// through a noop metrics provider. It is worth wiring anyway, because what the
// person sees is deliberately less than what happened: a refused sign-in and a
// registration that will not admit them are one sentence on one page, and which
// of them it was lives only in what this seam records.
//
// Every [Authenticator.AuthenticateSubject] is one attempt, and an
// unresolvable scope, a refused sign-in and a refused registration are three
// failures. The sign-in's own refusals are not counted twice — signin has
// already counted them, and this seam counts the authorization request it could
// not complete.
func NewAuthenticator(
	signIn *signin.Service,
	registry oauth2clients.Store,
	client database.Client,
	opts ...AuthenticatorOption,
) (*Authenticator, error) {
	if signIn == nil {
		return nil, ErrNilSignInService
	}

	if registry == nil {
		return nil, oauth2clients.ErrNilStore
	}

	if client == nil {
		return nil, oauth2clients.ErrNilDatabaseClient
	}

	a := &Authenticator{
		registry: registry,
		client:   client,
		signIn:   signIn,
		scopes:   GlobalScope,
		message:  DefaultMismatchMessage,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(a)
		}
	}

	a.o11y = observability.NewObserver(authenticatorName, a.opts.logger, a.opts.tracerProvider)

	instruments, err := metrics.NewOperationSet(a.opts.metricsProvider, authenticatorName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating oauth2clients authserver authenticator instruments")
	}

	a.instruments = instruments

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
	ctx, op := a.o11y.Begin(ctx)
	defer op.End()

	a.instruments.Attempt(ctx)
	op.SpanOnly(adminKey, a.administrative)

	scope, err := a.scopes(ctx, req)
	if err != nil {
		a.instruments.Failed(ctx)

		return nil, op.Error(err, "resolving the scope of an authorization request")
	}

	op.Set(scopeKey, scope.String())

	credentials := &signin.Credentials{
		Username: req.FormValue(oauth2server.FieldUsername),
		Password: req.FormValue(oauth2server.FieldPassword),
		TOTPCode: req.FormValue(oauth2server.FieldTOTPCode),
	}

	// signin collapses four refusals into one sentinel on purpose — telling an
	// unknown handle from a wrong password tells an attacker which half of the
	// guess was right — so this hands its message straight to the form rather
	// than deciding anything of its own.
	principal, err := a.authenticate(ctx, scope, credentials)
	if err != nil {
		a.instruments.Failed(ctx)

		// Acknowledged rather than returned through op.Error: what goes back is
		// a *LoginError carrying a message for a page, and the sentinel
		// underneath it is what an operator reads.
		op.Acknowledge(err, "signing in the resource owner of an authorization request")

		if platformerrors.Is(err, signin.ErrInvalidCredentials) {
			return nil, oauth2server.NewLoginError(oauth2server.DefaultLoginFailureMessage, err)
		}

		// Every other signin sentinel is one the person can act on and whose
		// wording was written for them — a second factor is required, the
		// account is suspended, verification is incomplete. Those reach the form
		// as themselves.
		return nil, oauth2server.NewLoginError(err.Error(), err)
	}

	op.Set(subjectKey, principal.User.ID)

	if err = a.admits(ctx, op, req, scope, principal.User.ID); err != nil {
		a.instruments.Failed(ctx)

		return nil, err
	}

	return &oauth2server.Subject{
		ID:     principal.User.ID,
		Claims: map[string]string{ClaimAccountID: principal.ActiveAccountID},
	}, nil
}

// authenticate sends the credentials through whichever of signin's two
// token-less doors this authenticator was built for.
//
// Token-less because this seam has no use for one: what it needs is the person,
// so that the registration can be compared against them, and a credential
// minted here would be discarded on the next line.
func (a *Authenticator) authenticate(
	ctx context.Context,
	scope tenancy.Scope,
	credentials *signin.Credentials,
) (*identity.Principal, error) {
	if a.administrative {
		return a.signIn.AdminAuthenticate(ctx, scope, credentials)
	}

	return a.signIn.Authenticate(ctx, scope, credentials)
}

// admits checks the registration the request named against the person who just
// signed in.
//
// The lookup is [guard.registrationFor], which both paths to a code share; what
// is here is the half that is this seam's alone. A nil registration is the check
// not applying — the request named no client — and every error the lookup makes
// fails the request, because there is nothing to type that fixes a registry that
// is down or an authorization server resolving its clients from another table.
func (a *Authenticator) admits(
	ctx context.Context,
	op observability.Operation,
	req *http.Request,
	scope tenancy.Scope,
	userID string,
) error {
	registered, err := a.registrationFor(ctx, op, req)
	if err != nil {
		return err
	}

	if registered == nil {
		return nil
	}

	if err = registered.Admits(scope, userID); err != nil {
		// The one step the two seams do not share, and the reason they are two
		// types. A refusal here re-renders the form: the person is still present
		// and can be told something, which is what [GuardedResolver] declines in
		// order to reach.
		//
		// One message for both refusals, deliberately. A person who may not use
		// this client has the same thing to do next whether the reason is their
		// organization or another person's ownership, and telling them which
		// would say that this client belongs to somebody.
		//
		// Which of the two it was is recorded rather than rendered: the page is
		// written for the person, and the log line is what tells an operator
		// whether a team is in the wrong registry or looking at somebody else's
		// personal credential.
		op.Acknowledge(err, "refusing a subject the registration does not admit")

		return oauth2server.NewLoginError(a.message, err)
	}

	return nil
}

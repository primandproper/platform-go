package authserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/authserver"
	"github.com/primandproper/platform-go/v14/authentication/oauth2server"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	"github.com/primandproper/platform-go/v14/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The authenticator is the other of the two seams that call
// oauth2clients.Client.Admits, and it reaches the check by a different route
// than the resolver does: it signs somebody in first, and answers a refusal with
// a page rather than a decline. A fix applied to one is not a fix applied to the
// other, so each of these asserts against the seam itself, with a real
// authentication/signin service over a real identity store on real SQLite.

// newAuthenticator wires the seam over a signed-in-able directory and a registry
// that answers one lookup.
func newAuthenticator(
	t *testing.T,
	h *signInHarness,
	registry oauth2clients.Store,
	scope tenancy.Scope,
	opts ...authserver.AuthenticatorOption,
) *authserver.Authenticator {
	t.Helper()

	opts = append([]authserver.AuthenticatorOption{
		authserver.WithScopeResolver(func(context.Context, *http.Request) (tenancy.Scope, error) {
			return scope, nil
		}),
	}, opts...)

	auth, err := authserver.NewAuthenticator(h.svc, registry, h.db, opts...)
	must.NoError(t, err)

	return auth
}

// loginRequest is the POST the shipped login form makes: the authorization
// parameters in the query, the credentials in the body, and the form parsed —
// which is what oauth2server does before this seam is asked anything.
func loginRequest(tb testing.TB, clientID, username, password string) *http.Request {
	tb.Helper()

	body := url.Values{
		oauth2server.FieldUsername: {username},
		oauth2server.FieldPassword: {password},
	}

	req := httptest.NewRequestWithContext(tb.Context(), http.MethodPost,
		"/authorize?client_id="+clientID+"&response_type=code",
		strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	must.NoError(tb, req.ParseForm())

	return req
}

func TestAuthenticator(T *testing.T) {
	T.Parallel()

	T.Run("issues a subject the registration admits", func(t *testing.T) {
		t.Parallel()

		h := newSignInHarness(t, tenantA)
		auth := newAuthenticator(t, h, &fakeRegistry{client: registration(tenantA, h.user.ID)}, tenantA)

		subject, err := auth.AuthenticateSubject(t.Context(),
			loginRequest(t, "cid-1", h.user.Username, h.password))
		must.NoError(t, err)
		must.NotNil(t, subject)
		test.EqOp(t, h.user.ID, subject.ID)

		// The claim the resource server reads the signed-in account back out of.
		// It is a contract between two halves a consumer wires separately, so an
		// empty one here is a token that authorizes somebody for no account.
		test.NotEqOp(t, "", subject.Claims[authserver.ClaimAccountID])
	})

	T.Run("admits through an administered registration", func(t *testing.T) {
		t.Parallel()

		// The arrangement an operator mints: no owner, so any subject the
		// registry admits may authorize through it.
		h := newSignInHarness(t, tenantA)
		auth := newAuthenticator(t, h, &fakeRegistry{client: registration(tenantA, "")}, tenantA)

		subject, err := auth.AuthenticateSubject(t.Context(),
			loginRequest(t, "cid-1", h.user.Username, h.password))
		must.NoError(t, err)
		must.NotNil(t, subject)
		test.EqOp(t, h.user.ID, subject.ID)
	})

	T.Run("re-renders the form when the registration is in another registry", func(t *testing.T) {
		t.Parallel()

		h := newSignInHarness(t, tenantA)
		auth := newAuthenticator(t, h, &fakeRegistry{client: registration(tenantB, "")}, tenantA)

		subject, err := auth.AuthenticateSubject(t.Context(),
			loginRequest(t, "cid-1", h.user.Username, h.password))
		must.Error(t, err)
		test.Nil(t, subject)

		// A *LoginError, so the form comes back with a message rather than the
		// attempt ending at a redirect. The person is still here.
		test.ErrorIs(t, err, oauth2server.ErrLoginFailed)
		test.ErrorIs(t, err, oauth2clients.ErrClientScopeMismatch)

		var loginErr *oauth2server.LoginError
		must.True(t, platformerrors.As(err, &loginErr))
		test.EqOp(t, authserver.DefaultMismatchMessage, loginErr.Message)
	})

	T.Run("re-renders the form when the registration is another person's", func(t *testing.T) {
		t.Parallel()

		h := newSignInHarness(t, tenantA)
		auth := newAuthenticator(t, h, &fakeRegistry{client: registration(tenantA, userB)}, tenantA)

		subject, err := auth.AuthenticateSubject(t.Context(),
			loginRequest(t, "cid-1", h.user.Username, h.password))
		must.Error(t, err)
		test.Nil(t, subject)
		test.ErrorIs(t, err, oauth2server.ErrLoginFailed)
		test.ErrorIs(t, err, oauth2clients.ErrClientOwnerMismatch)
	})

	T.Run("says the same sentence for both refusals", func(t *testing.T) {
		t.Parallel()

		// One message for the two, deliberately: a person who may not use this
		// client has the same thing to do next whether the reason is their
		// organization or another person's ownership, and telling them which
		// would say that this client belongs to somebody.
		messages := make([]string, 0, 2)

		for _, registered := range []*oauth2clients.Client{
			registration(tenantB, ""),
			registration(tenantA, userB),
		} {
			h := newSignInHarness(t, tenantA)
			auth := newAuthenticator(t, h, &fakeRegistry{client: registered}, tenantA)

			_, err := auth.AuthenticateSubject(t.Context(),
				loginRequest(t, "cid-1", h.user.Username, h.password))
			must.Error(t, err)

			var loginErr *oauth2server.LoginError
			must.True(t, platformerrors.As(err, &loginErr))

			messages = append(messages, loginErr.Message)
		}

		test.EqOp(t, messages[0], messages[1])
	})

	T.Run("renders the configured message instead of the default", func(t *testing.T) {
		t.Parallel()

		const mine = "Ask the platform team for access to this application."

		h := newSignInHarness(t, tenantA)
		auth := newAuthenticator(t, h, &fakeRegistry{client: registration(tenantB, "")}, tenantA,
			authserver.WithMismatchMessage(mine))

		_, err := auth.AuthenticateSubject(t.Context(),
			loginRequest(t, "cid-1", h.user.Username, h.password))
		must.Error(t, err)

		var loginErr *oauth2server.LoginError
		must.True(t, platformerrors.As(err, &loginErr))
		test.EqOp(t, mine, loginErr.Message)
	})

	T.Run("the client check runs after the password", func(t *testing.T) {
		t.Parallel()

		// The ordering the type documentation argues for. Answering "this client
		// is not registered for your organization" before a password would tell
		// an anonymous caller which registrations exist and which registry they
		// are in — an enumeration oracle over every tenant, reachable by anybody
		// who can construct a URL. So a wrong password against a registration
		// this person may not use is the *password's* refusal, and the registry
		// is never consulted: the fake panics if it is.
		h := newSignInHarness(t, tenantA)
		auth := newAuthenticator(t, h, &unreadRegistry{}, tenantA)

		_, err := auth.AuthenticateSubject(t.Context(),
			loginRequest(t, "cid-1", h.user.Username, "not the password"))
		must.Error(t, err)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		var loginErr *oauth2server.LoginError
		must.True(t, platformerrors.As(err, &loginErr))
		test.EqOp(t, oauth2server.DefaultLoginFailureMessage, loginErr.Message)
	})

	T.Run("an unknown handle is the same answer as a wrong password", func(t *testing.T) {
		t.Parallel()

		// signin collapses the two on purpose, and this seam hands its sentinel
		// through rather than deciding anything of its own.
		h := newSignInHarness(t, tenantA)
		auth := newAuthenticator(t, h, &unreadRegistry{}, tenantA)

		_, err := auth.AuthenticateSubject(t.Context(),
			loginRequest(t, "cid-1", "nobody", h.password))
		must.Error(t, err)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		var loginErr *oauth2server.LoginError
		must.True(t, platformerrors.As(err, &loginErr))
		test.EqOp(t, oauth2server.DefaultLoginFailureMessage, loginErr.Message)
	})

	T.Run("signing in to a registry the person is not in fails", func(t *testing.T) {
		t.Parallel()

		// The property that makes the authenticator's ScopeResolver safe where
		// the resolver's would not be: the scope is handed to signin, which
		// checks the credentials within it, so a request naming another registry
		// never reaches the registration at all.
		h := newSignInHarness(t, tenantA)
		auth := newAuthenticator(t, h, &unreadRegistry{}, tenantB)

		_, err := auth.AuthenticateSubject(t.Context(),
			loginRequest(t, "cid-1", h.user.Username, h.password))
		must.Error(t, err)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
	})

	T.Run("a client this registry never issued fails the request", func(t *testing.T) {
		t.Parallel()

		// Not a LoginError: there is nothing to type that fixes a deployment
		// whose authorization server resolves its clients somewhere else. See
		// authserver.ErrClientNotRegistered.
		h := newSignInHarness(t, tenantA)
		auth := newAuthenticator(t, h, &fakeRegistry{err: oauth2clients.ErrClientNotFound}, tenantA)

		_, err := auth.AuthenticateSubject(t.Context(),
			loginRequest(t, "cid-1", h.user.Username, h.password))
		must.Error(t, err)
		test.ErrorIs(t, err, authserver.ErrClientNotRegistered)
		test.False(t, platformerrors.Is(err, oauth2server.ErrLoginFailed))
	})

	T.Run("a broken registry fails the request rather than the form", func(t *testing.T) {
		t.Parallel()

		broken := platformerrors.New("the registry is unreachable")

		h := newSignInHarness(t, tenantA)
		auth := newAuthenticator(t, h, &fakeRegistry{err: broken}, tenantA)

		_, err := auth.AuthenticateSubject(t.Context(),
			loginRequest(t, "cid-1", h.user.Username, h.password))
		must.Error(t, err)
		test.ErrorIs(t, err, broken)

		// Re-rendering a form against a database that is down produces somebody
		// who tries four times and then files a support ticket.
		test.False(t, platformerrors.Is(err, oauth2server.ErrLoginFailed))
	})

	T.Run("a broken scope resolver fails the request", func(t *testing.T) {
		t.Parallel()

		broken := platformerrors.New("the gateway sent no tenant")

		h := newSignInHarness(t, tenantA)
		auth := newAuthenticator(t, h, &unreadRegistry{}, tenantA,
			authserver.WithScopeResolver(func(context.Context, *http.Request) (tenancy.Scope, error) {
				return tenancy.Scope{}, broken
			}))

		_, err := auth.AuthenticateSubject(t.Context(),
			loginRequest(t, "cid-1", h.user.Username, h.password))
		must.Error(t, err)
		test.ErrorIs(t, err, broken)
	})

	T.Run("a request naming no client is left to the authorization server", func(t *testing.T) {
		t.Parallel()

		// The server has already refused it, before this seam was reached, and
		// duplicating that refusal here would be a second place deciding what a
		// malformed request is. The registry is never consulted.
		h := newSignInHarness(t, tenantA)
		auth := newAuthenticator(t, h, &unreadRegistry{}, tenantA)

		subject, err := auth.AuthenticateSubject(t.Context(),
			loginRequest(t, "", h.user.Username, h.password))
		must.NoError(t, err)
		must.NotNil(t, subject)
		test.EqOp(t, h.user.ID, subject.ID)
	})

	T.Run("the administrative door refuses a person holding no service role", func(t *testing.T) {
		t.Parallel()

		// A service that named no administrative roles has no administrative
		// door, so this is the refusal a deployment gets for wiring the option
		// without the roles — and it is not the registration's refusal.
		h := newSignInHarness(t, tenantA)
		auth := newAuthenticator(t, h, &unreadRegistry{}, tenantA,
			authserver.WithAdministrativeLogin())

		_, err := auth.AuthenticateSubject(t.Context(),
			loginRequest(t, "cid-1", h.user.Username, h.password))
		must.Error(t, err)
		test.False(t, platformerrors.Is(err, oauth2clients.ErrClientScopeMismatch))
	})

	T.Run("refuses what it cannot be built from", func(t *testing.T) {
		t.Parallel()

		h := newSignInHarness(t, tenantA)

		_, err := authserver.NewAuthenticator(nil, &fakeRegistry{}, h.db)
		test.ErrorIs(t, err, oauth2clients.ErrNilService)

		_, err = authserver.NewAuthenticator(h.svc, nil, h.db)
		test.ErrorIs(t, err, oauth2clients.ErrNilStore)

		_, err = authserver.NewAuthenticator(h.svc, &fakeRegistry{}, nil)
		test.ErrorIs(t, err, oauth2clients.ErrNilDatabaseClient)
	})
}

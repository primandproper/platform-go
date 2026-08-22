package oauth2server_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/authentication/oauth2server/memory"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewServer(T *testing.T) {
	T.Parallel()

	authenticator := &passwordAuthenticator{}

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		srv, err := oauth2server.NewServer(testIssuer, memory.NewStore(), authenticator)
		must.NoError(t, err)
		test.EqOp(t, testIssuer, srv.Issuer())
	})

	T.Run("rejects a nil store", func(t *testing.T) {
		t.Parallel()

		srv, err := oauth2server.NewServer(testIssuer, nil, authenticator)
		test.ErrorIs(t, err, oauth2server.ErrNilStore)
		test.Nil(t, srv)
	})

	T.Run("rejects a nil authenticator", func(t *testing.T) {
		t.Parallel()

		// There is no default. One would be a server that issues authorization
		// codes to whoever asks.
		srv, err := oauth2server.NewServer(testIssuer, memory.NewStore(), nil)
		test.ErrorIs(t, err, oauth2server.ErrNilAuthenticator)
		test.Nil(t, srv)
	})

	T.Run("rejects an issuer that is not one", func(t *testing.T) {
		t.Parallel()

		for name, issuer := range map[string]string{
			"empty":       "",
			"no scheme":   "auth.example",
			"plain http":  "http://auth.example",
			"with query":  "https://auth.example?tenant=acme",
			"with a hash": "https://auth.example#top",
			"no host":     "https://",
		} {
			srv, err := oauth2server.NewServer(issuer, memory.NewStore(), authenticator)
			test.Error(t, err, test.Sprintf("issuer %q (%s)", issuer, name))
			test.Nil(t, srv, test.Sprintf("issuer %q (%s)", issuer, name))
		}
	})

	T.Run("accepts http only on a loopback host", func(t *testing.T) {
		t.Parallel()

		// A development server on 127.0.0.1 is not reachable by anyone who is
		// not already on the machine; anything else on http is an authorization
		// server handing bearer tokens to the network.
		srv, err := oauth2server.NewServer("http://localhost:8080", memory.NewStore(), authenticator)
		must.NoError(t, err)
		test.EqOp(t, "http://localhost:8080", srv.Issuer())
	})

	T.Run("normalizes a trailing slash away", func(t *testing.T) {
		t.Parallel()

		// The issuer is concatenated with the endpoint paths to build the
		// discovery document, so a trailing slash renders "https://x//token".
		srv, err := oauth2server.NewServer("https://auth.example/", memory.NewStore(), authenticator)
		must.NoError(t, err)
		test.EqOp(t, "https://auth.example", srv.Issuer())
		test.EqOp(t, "https://auth.example/token", srv.Metadata().TokenEndpoint)
	})
}

// The discovery document is what a client believes, so what it advertises has
// to be what the endpoints do.
func TestServer_Metadata(T *testing.T) {
	T.Parallel()

	T.Run("advertises exactly what this server implements", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithScopes("read", "write"))
		doc := h.server.Metadata()

		test.EqOp(t, testIssuer, doc.Issuer)
		test.EqOp(t, testIssuer+oauth2server.PathAuthorize, doc.AuthorizationEndpoint)
		test.EqOp(t, testIssuer+oauth2server.PathToken, doc.TokenEndpoint)
		test.EqOp(t, testIssuer+oauth2server.PathRegister, doc.RegistrationEndpoint)
		test.EqOp(t, testIssuer+oauth2server.PathRevoke, doc.RevocationEndpoint)

		test.Eq(t, []string{oauth2server.ResponseTypeCode}, doc.ResponseTypesSupported)
		test.Eq(t, []string{oauth2server.GrantTypeAuthorizationCode, oauth2server.GrantTypeRefreshToken},
			doc.GrantTypesSupported)

		// S256 and nothing else, which is also how a client discovers that PKCE
		// is not optional here.
		test.Eq(t, []string{oauth2server.CodeChallengeMethodS256}, doc.CodeChallengeMethodsSupported)
		test.Eq(t, []string{"read", "write"}, doc.ScopesSupported)
		test.True(t, doc.AuthorizationResponseIssParameterSupported)
	})

	T.Run("omits the registration endpoint when registration is not served", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithDynamicRegistration(false))
		doc := h.server.Metadata()

		test.EqOp(t, "", doc.RegistrationEndpoint)

		// Everything else still named, so what was dropped is the one endpoint
		// this server stopped serving rather than the document.
		test.EqOp(t, testIssuer+oauth2server.PathAuthorize, doc.AuthorizationEndpoint)
		test.EqOp(t, testIssuer+oauth2server.PathRevoke, doc.RevocationEndpoint)
	})

	T.Run("leaves the registration key out of the document rather than blank", func(t *testing.T) {
		t.Parallel()

		served := readBody(t, newHarness(t).get(oauth2server.PathAuthorizationServerMetadata))
		test.StrContains(t, served, `"registration_endpoint":"`+testIssuer+oauth2server.PathRegister+`"`)

		withheld := readBody(t,
			newHarness(t, oauth2server.WithDynamicRegistration(false)).get(oauth2server.PathAuthorizationServerMetadata))

		// Absent, not empty. A client that resolved "" against the issuer would
		// get this server's root and send its registration there.
		test.StrNotContains(t, withheld, "registration_endpoint")
	})

	T.Run("is served at the well-known path", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res := h.get(oauth2server.PathAuthorizationServerMetadata)

		test.EqOp(t, http.StatusOK, res.StatusCode)
		test.StrContains(t, res.Header.Get("Content-Type"), "application/json")

		// Cacheable, unlike every other response here: it carries no credential
		// and changes only on redeploy.
		test.StrContains(t, res.Header.Get("Cache-Control"), "max-age")
	})
}

// The whole flow, once, end to end — the thing every other case in this file is
// a deviation from.
func TestServer_Flow(T *testing.T) {
	T.Parallel()

	T.Run("register, authorize, redeem, use, refresh", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithResources(testResource))
		reg := h.registerConfidential()

		tokens := h.exchange(reg)
		test.EqOp(t, oauth2server.TokenTypeBearer, tokens.TokenType)
		test.NotEq(t, "", tokens.AccessToken)
		test.NotEq(t, "", tokens.RefreshToken)
		test.EqOp(t, "read", tokens.Scope)
		test.EqOp(t, int64(oauth2server.DefaultAccessTokenTTL.Seconds()), tokens.ExpiresIn)

		access, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		must.NoError(t, err)
		test.EqOp(t, testSubject().ID, access.Subject.ID)

		// The application-shaped half of the identity, carried through the code
		// and into the token without this package reading it.
		test.EqOp(t, "acct_9", access.Subject.Claims["account_id"])

		// RFC 8707. Without it a token minted for this resource server can be
		// replayed at another one that trusts the same issuer.
		test.Eq(t, []string{testResource}, access.Audience)

		refreshed := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})

		must.EqOp(t, http.StatusOK, refreshed.status)
		test.NotEq(t, tokens.AccessToken, refreshed.AccessToken)

		// Rotation: the refresh token is single-use, so the response carries a
		// different one.
		test.NotEq(t, tokens.RefreshToken, refreshed.RefreshToken)
	})

	T.Run("the redirect carries the state and the issuer", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		res := h.authorize(authorizeParams(reg.ClientID), login())
		must.EqOp(t, http.StatusFound, res.StatusCode)

		location, err := url.Parse(res.Header.Get("Location"))
		must.NoError(t, err)

		test.EqOp(t, "opaque-state", location.Query().Get("state"))

		// RFC 9207: a client configured with more than one authorization server
		// cannot otherwise tell which one answered.
		test.EqOp(t, testIssuer, location.Query().Get("iss"))
	})

	T.Run("a public client redeems with PKCE and no secret", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		reg := h.register(map[string]any{
			"redirect_uris":              []string{testRedirectURI},
			"token_endpoint_auth_method": oauth2server.AuthMethodNone,
		})

		must.EqOp(t, http.StatusCreated, reg.status)

		// No secret is minted for a public client, so there is nothing to ship
		// in a binary and nothing to leak.
		test.EqOp(t, "", reg.ClientSecret)

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		out := h.token(reg.ClientID, "", url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {testVerifier},
		})

		test.EqOp(t, http.StatusOK, out.status)
		test.NotEq(t, "", out.AccessToken)
	})
}

// The login form is the one response a human reads.
func TestServer_Login(T *testing.T) {
	T.Parallel()

	T.Run("a GET renders the form", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		res := h.get(oauth2server.PathAuthorize + "?" + authorizeParams(reg.ClientID).Encode())

		test.EqOp(t, http.StatusOK, res.StatusCode)
		test.StrContains(t, res.Header.Get("Content-Type"), "text/html")

		// A login page cached by a shared proxy is a login page served to the
		// next person, and its URL carries the whole authorization request.
		test.EqOp(t, "no-store", res.Header.Get("Cache-Control"))
	})

	T.Run("a wrong password re-renders the form rather than ending the attempt", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		res := h.authorize(authorizeParams(reg.ClientID), url.Values{
			oauth2server.FieldUsername: {testUsername},
			oauth2server.FieldPassword: {"wrong"},
		})

		// The human is still here and can try again, so the answer is the form
		// — not a redirect that sends them back to the client empty-handed.
		test.EqOp(t, http.StatusUnauthorized, res.StatusCode)
		test.StrContains(t, res.Header.Get("Content-Type"), "text/html")
	})

	T.Run("a broken identity store fails the request instead of looping the form", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		// Not an ErrLoginFailed. Re-rendering the form against a database that
		// is down produces a user who tries four times and files a ticket.
		h.authenticator.err = platformerrors.New("identity repository unreachable")

		errorCode, state := h.redirectError(h.authorize(authorizeParams(reg.ClientID), login()))
		test.EqOp(t, oauth2server.ErrorCodeServerError, errorCode)
		test.EqOp(t, "opaque-state", state)
	})

	T.Run("an authenticator that refuses without saying so does not authorize anybody", func(t *testing.T) {
		t.Parallel()

		// (nil, nil) is an authenticator that meant to refuse and forgot to say
		// so, and a subject with no identifier is one that authorizes whoever
		// the resource server decides the empty string is. Read as success,
		// either would issue a code.
		for name, authenticator := range map[string]oauth2server.SubjectAuthenticator{
			"nil subject, nil error": oauth2server.SubjectAuthenticatorFunc(
				func(context.Context, *http.Request) (*oauth2server.Subject, error) {
					return nil, nil
				}),
			"subject with no identifier": oauth2server.SubjectAuthenticatorFunc(
				func(context.Context, *http.Request) (*oauth2server.Subject, error) {
					return &oauth2server.Subject{Claims: map[string]string{"account_id": "acct_9"}}, nil
				}),
		} {
			h := newHarnessWith(t, authenticator)
			reg := h.registerConfidential()

			res := h.authorize(authorizeParams(reg.ClientID), login())
			test.EqOp(t, http.StatusUnauthorized, res.StatusCode, test.Sprintf("%s", name))
		}
	})
}

func TestServer_Revocation(T *testing.T) {
	T.Parallel()

	T.Run("revoking an access token stops it working immediately", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		test.EqOp(t, http.StatusOK, h.revoke(reg.ClientID, reg.ClientSecret, tokens.AccessToken, ""))

		// This is the property the opaque-token decision is for. With a signed
		// token this assertion could only be written as "in fifteen minutes".
		_, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
	})

	T.Run("revoking a refresh token ends the whole session", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		test.EqOp(t, http.StatusOK,
			h.revoke(reg.ClientID, reg.ClientSecret, tokens.RefreshToken, "refresh_token"))

		// The access token minted alongside it goes too. A sign-out that left it
		// working for another quarter of an hour is not a sign-out.
		_, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		test.ErrorIs(t, err, oauth2server.ErrNotFound)

		refreshed := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})

		test.EqOp(t, http.StatusBadRequest, refreshed.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, refreshed.Error)
	})

	T.Run("a token nobody issued is answered 200", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		// RFC 7009 §2.2. An endpoint that answered 404 here would let anybody
		// enumerate which tokens exist by sending guesses at it.
		test.EqOp(t, http.StatusOK, h.revoke(reg.ClientID, reg.ClientSecret, "not-a-token", ""))
	})

	T.Run("another client's token is not revocable", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		victim := h.registerConfidential()
		attacker := h.registerConfidential()

		tokens := h.exchange(victim)

		// Answered 200, as the RFC requires, and nothing happened. Without the
		// ownership check any registered client could end any other client's
		// sessions by presenting their tokens.
		test.EqOp(t, http.StatusOK, h.revoke(attacker.ClientID, attacker.ClientSecret, tokens.AccessToken, ""))

		access, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		must.NoError(t, err)
		test.NotNil(t, access)
	})

	T.Run("a wrong hint costs a lookup rather than a revocation", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		// The hint is an optimization, per RFC 7009 §2.1: a client that
		// mislabels its own token still meant to revoke it.
		test.EqOp(t, http.StatusOK,
			h.revoke(reg.ClientID, reg.ClientSecret, tokens.AccessToken, "refresh_token"))

		_, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
	})

	T.Run("a revocation with no token is a request error", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		test.EqOp(t, http.StatusBadRequest, h.revoke(reg.ClientID, reg.ClientSecret, "", ""))
	})
}

func TestServer_Authenticate(T *testing.T) {
	T.Parallel()

	T.Run("rejects an empty bearer", func(t *testing.T) {
		t.Parallel()

		token, err := newHarness(t).server.Authenticate(t.Context(), "")
		test.ErrorIs(t, err, oauth2server.ErrEmptyIdentifier)
		test.Nil(t, token)
	})

	T.Run("rejects a token nobody issued", func(t *testing.T) {
		t.Parallel()

		token, err := newHarness(t).server.Authenticate(t.Context(), "made-up")
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
		test.Nil(t, token)
	})

	T.Run("rejects an expired token", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		// Written straight to the store, because the alternative is waiting out
		// an access token lifetime.
		value, digest := "expired-token", oauth2server.Hash("expired-token")
		now := time.Now().UTC()

		must.NoError(t, h.store.CreateAccessToken(t.Context(), &oauth2server.AccessToken{
			IssuedAt:  now.Add(-time.Hour),
			ExpiresAt: now.Add(-time.Minute),
			Hash:      digest,
			ClientID:  "client",
			Subject:   oauth2server.Subject{ID: "user"},
		}))

		token, err := h.server.Authenticate(t.Context(), value)
		test.ErrorIs(t, err, oauth2server.ErrExpired)
		test.Nil(t, token)
	})
}

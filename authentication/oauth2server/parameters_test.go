package oauth2server_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// malformedForm is a body no url.ParseQuery will read: "%zz" is not an escape.
// It is how a request reaches the one branch every endpoint here opens with.
const malformedForm = "grant_type=%zz"

func TestAuthorize_Parameters(T *testing.T) {
	T.Parallel()

	T.Run("a request with no client_id is refused before anything is read", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		// There is no client, so there is no registered redirect URI, so there
		// is nowhere to send this but the browser.
		res := h.get(oauth2server.PathAuthorize)
		test.EqOp(t, http.StatusBadRequest, res.StatusCode)
		test.StrContains(t, readBody(t, res), oauth2server.ErrorCodeInvalidRequest)
	})

	T.Run("a body that is not a form is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res := h.do(http.MethodPost, oauth2server.PathAuthorize, formContentType, strings.NewReader(malformedForm))
		test.EqOp(t, http.StatusBadRequest, res.StatusCode)
		test.StrContains(t, readBody(t, res), oauth2server.ErrorCodeInvalidRequest)
	})

	T.Run("a client not registered for the code grant cannot start a flow", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		reg := h.register(map[string]any{
			"redirect_uris": []string{testRedirectURI},
			"grant_types":   []string{oauth2server.GrantTypeRefreshToken},
		})
		must.EqOp(t, http.StatusCreated, reg.status)

		// Redirected rather than shown in the browser: the redirect URI has
		// been matched by this point, so the client is the one that needs to
		// hear this.
		errorCode, _ := h.redirectError(h.authorize(authorizeParams(reg.ClientID), login()))
		test.EqOp(t, oauth2server.ErrorCodeUnauthorizedClient, errorCode)
	})

	T.Run("a scope the client did not register for is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		reg := h.register(map[string]any{
			"redirect_uris": []string{testRedirectURI},
			"scope":         "read",
		})
		must.EqOp(t, http.StatusCreated, reg.status)

		params := authorizeParams(reg.ClientID)
		params.Set("scope", "write")

		// Refused rather than narrowed to "read". A narrowed token looks like
		// the one that was asked for and is not, and the client finds out at
		// the resource server as a 403 it cannot attribute.
		errorCode, _ := h.redirectError(h.authorize(params, login()))
		test.EqOp(t, oauth2server.ErrorCodeInvalidScope, errorCode)
	})

	T.Run("a challenge of the right length in the wrong alphabet is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		params := authorizeParams(reg.ClientID)

		// 43 characters, which is the length an S256 digest encodes to, but
		// standard base64 rather than base64url. A client with this bug would
		// otherwise get a form, a password, and a code it can never redeem.
		params.Set("code_challenge", strings.Repeat("a", 42)+"+")

		errorCode, _ := h.redirectError(h.authorize(params, login()))
		test.EqOp(t, oauth2server.ErrorCodeInvalidRequest, errorCode)
	})
}

func TestToken_Parameters(T *testing.T) {
	T.Parallel()

	T.Run("a body that is not a form is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res := h.do(http.MethodPost, oauth2server.PathToken, formContentType, strings.NewReader(malformedForm))
		test.EqOp(t, http.StatusBadRequest, res.StatusCode)
	})

	T.Run("a code grant with no code is a request error", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		out := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type": {oauth2server.GrantTypeAuthorizationCode},
		})

		// invalid_request rather than invalid_grant: nothing was presented, so
		// there is no grant to call invalid.
		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidRequest, out.Error)
	})

	T.Run("a refresh grant with no refresh_token is a request error", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		out := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type": {oauth2server.GrantTypeRefreshToken},
		})

		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidRequest, out.Error)
	})

	T.Run("a verifier outside the unreserved set is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		out := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {strings.Repeat("a", 42) + "%"},
		})

		// The length is right and the alphabet is not, which RFC 7636 §4.1
		// forbids for the same reason it fixes the length: what a client sends
		// here has to survive a URL round trip unchanged.
		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidRequest, out.Error)
	})

	T.Run("a client registered for one grant cannot use the other", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		codeOnly := h.register(map[string]any{
			"redirect_uris": []string{testRedirectURI},
			"grant_types":   []string{oauth2server.GrantTypeAuthorizationCode},
		})
		must.EqOp(t, http.StatusCreated, codeOnly.status)

		refreshOnly := h.register(map[string]any{
			"redirect_uris": []string{testRedirectURI},
			"grant_types":   []string{oauth2server.GrantTypeRefreshToken},
		})
		must.EqOp(t, http.StatusCreated, refreshOnly.status)

		// Checked before the credential is looked at, so a client that was
		// never granted a grant type cannot use a stolen credential to probe
		// whether one exists.
		refused := h.token(codeOnly.ClientID, codeOnly.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {"anything"},
		})
		test.EqOp(t, http.StatusBadRequest, refused.status)
		test.EqOp(t, oauth2server.ErrorCodeUnauthorizedClient, refused.Error)

		refused = h.token(refreshOnly.ClientID, refreshOnly.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {"anything"},
			"code_verifier": {testVerifier},
		})
		test.EqOp(t, http.StatusBadRequest, refused.status)
		test.EqOp(t, oauth2server.ErrorCodeUnauthorizedClient, refused.Error)
	})
}

func TestRevoke_Parameters(T *testing.T) {
	T.Parallel()

	T.Run("a body that is not a form is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res := h.do(http.MethodPost, oauth2server.PathRevoke, formContentType, strings.NewReader(malformedForm))
		test.EqOp(t, http.StatusBadRequest, res.StatusCode)
	})

	T.Run("another client's refresh token is not revocable", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		victim := h.registerConfidential()
		attacker := h.registerConfidential()

		tokens := h.exchange(victim)

		// The hint sends this down the refresh path, which has its own
		// ownership check: without it, the family revocation a refresh token
		// triggers would be the most destructive thing a registered client
		// could do to another.
		test.EqOp(t, http.StatusOK,
			h.revoke(attacker.ClientID, attacker.ClientSecret, tokens.RefreshToken, "refresh_token"))

		out := h.token(victim.ClientID, victim.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})
		test.EqOp(t, http.StatusOK, out.status)
	})
}

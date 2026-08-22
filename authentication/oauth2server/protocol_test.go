package oauth2server_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The redirect URI is the check the map-backed examples store and never read
// again, so it gets its own group.
func TestAuthorize_RedirectURI(T *testing.T) {
	T.Parallel()

	T.Run("an unregistered redirect_uri receives nothing, and is not redirected to", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		params := authorizeParams(reg.ClientID)
		params.Set("redirect_uri", "https://attacker.example/steal")

		res := h.authorize(params, login())

		// Answered in the browser rather than redirected. Sending "your
		// redirect_uri is wrong" to the redirect_uri would be sending it to the
		// address this server has just decided is not the client's — which is
		// the attack registered URIs exist to stop.
		test.EqOp(t, http.StatusBadRequest, res.StatusCode)
		test.EqOp(t, "", res.Header.Get("Location"))
	})

	T.Run("matching is exact, not by prefix or host", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		// Every one of these has been somebody's takeover: a suffix, an added
		// query, a path traversal under the same host.
		for _, near := range []string{
			testRedirectURI + ".attacker.example",
			testRedirectURI + "?next=https://attacker.example",
			"https://client.example/callback/../../evil",
			"https://client.example",
		} {
			params := authorizeParams(reg.ClientID)
			params.Set("redirect_uri", near)

			res := h.authorize(params, login())
			test.EqOp(t, http.StatusBadRequest, res.StatusCode, test.Sprintf("redirect_uri %q", near))
		}
	})

	T.Run("an absent redirect_uri is not guessed from the registration", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		params := authorizeParams(reg.ClientID)
		params.Del("redirect_uri")

		// RFC 6749 permits defaulting to the single registered URI; OAuth 2.1
		// requires the parameter, for the same reason exact matching exists.
		res := h.authorize(params, login())
		test.EqOp(t, http.StatusBadRequest, res.StatusCode)
	})

	T.Run("an unknown client_id is refused before any redirect", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res := h.authorize(authorizeParams("no-such-client"), login())
		test.EqOp(t, http.StatusBadRequest, res.StatusCode)
		test.EqOp(t, "", res.Header.Get("Location"))
	})

	T.Run("the token endpoint re-checks the redirect_uri the code was issued for", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		out := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {"https://attacker.example/steal"},
			"code_verifier": {testVerifier},
		})

		// The URI is part of what the code is bound to, so naming a different
		// one is a request to complete somebody else's flow.
		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, out.Error)
	})
}

// Client authentication at /token is the check whose absence makes registration
// decorative.
func TestToken_ClientAuthentication(T *testing.T) {
	T.Parallel()

	T.Run("a confidential client's secret is verified", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		out := h.token(reg.ClientID, "not-the-secret", url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {testVerifier},
		})

		test.EqOp(t, http.StatusUnauthorized, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidClient, out.Error)
	})

	T.Run("a missing secret from a client that registered one is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		out := h.token(reg.ClientID, "", url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {testVerifier},
		})

		test.EqOp(t, http.StatusUnauthorized, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidClient, out.Error)
	})

	T.Run("HTTP Basic carries the same credential", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		form := url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {testVerifier},
		}

		// The same request with no credential at all is a 401 — and it does not
		// spend the code, because the client is authenticated before any grant
		// runs. That is what makes the Basic attempt below a test of the
		// credential rather than a second spelling of a request that would have
		// worked anyway.
		anonymous := h.token("", "", form)
		must.EqOp(t, http.StatusUnauthorized, anonymous.status)

		// RFC 6749 §2.3.1 says a server MUST support Basic and MAY support the
		// form parameter. This one supports both, and they carry the same
		// secret over the same TLS.
		basic := h.basicToken(reg.ClientID, reg.ClientSecret, form)
		test.EqOp(t, http.StatusOK, basic.status)
		test.NotEq(t, "", basic.AccessToken)
	})

	T.Run("an unknown client_id is answered like a wrong secret", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		out := h.token("no-such-client", "secret", url.Values{
			"grant_type": {oauth2server.GrantTypeRefreshToken}, "refresh_token": {"x"},
		})

		// Same code, same status. Distinguishing them would tell an anonymous
		// caller which client identifiers exist.
		test.EqOp(t, http.StatusUnauthorized, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidClient, out.Error)
	})

	T.Run("a secret presented by a public client is refused rather than ignored", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		reg := h.register(map[string]any{
			"redirect_uris":              []string{testRedirectURI},
			"token_endpoint_auth_method": oauth2server.AuthMethodNone,
		})
		must.EqOp(t, http.StatusCreated, reg.status)

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		// One side of this exchange is confused about which client it is, and
		// the confusion is worth a 401 rather than a token.
		out := h.token(reg.ClientID, "invented-secret", url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {testVerifier},
		})

		test.EqOp(t, http.StatusUnauthorized, out.status)
	})
}

func TestAuthorize_PKCE(T *testing.T) {
	T.Parallel()

	T.Run("an authorization request without a challenge is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		params := authorizeParams(reg.ClientID)
		params.Del("code_challenge")
		params.Del("code_challenge_method")

		// There is no option that turns this off. A confidential client gains
		// nothing by skipping PKCE and a public one is defenseless without it.
		errorCode, _ := h.redirectError(h.authorize(params, login()))
		test.EqOp(t, oauth2server.ErrorCodeInvalidRequest, errorCode)
	})

	T.Run("plain is refused, and an absent method is not agreement", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		// Under RFC 7636 an absent method defaults to "plain", which puts the
		// verifier in the request PKCE exists to protect. Silence is therefore
		// a request for the method this server refuses.
		for _, method := range []string{"", "plain", "S512"} {
			params := authorizeParams(reg.ClientID)
			params.Set("code_challenge_method", method)
			if method == "" {
				params.Del("code_challenge_method")
			}

			errorCode, _ := h.redirectError(h.authorize(params, login()))
			test.EqOp(t, oauth2server.ErrorCodeInvalidRequest, errorCode, test.Sprintf("method %q", method))
		}
	})

	T.Run("a malformed challenge is caught before a password is typed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		params := authorizeParams(reg.ClientID)
		params.Set("code_challenge", "too-short")

		errorCode, _ := h.redirectError(h.authorize(params, login()))
		test.EqOp(t, oauth2server.ErrorCodeInvalidRequest, errorCode)
	})

	T.Run("a wrong verifier does not redeem the code", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		out := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {"9876543210987654321098765432109876543210xyz"},
		})

		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, out.Error)
	})

	T.Run("a verifier outside the RFC 7636 length bounds is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		out := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {"short"},
		})

		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidRequest, out.Error)
	})
}

func TestToken_Grants(T *testing.T) {
	T.Parallel()

	T.Run("an authorization code is spent by the first request that presents it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		form := func() url.Values {
			return url.Values{
				"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
				"code":          {code},
				"redirect_uri":  {testRedirectURI},
				"code_verifier": {testVerifier},
			}
		}

		first := h.token(reg.ClientID, reg.ClientSecret, form())
		must.EqOp(t, http.StatusOK, first.status)

		second := h.token(reg.ClientID, reg.ClientSecret, form())
		test.EqOp(t, http.StatusBadRequest, second.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, second.Error)
	})

	T.Run("a code is spent even by a request that turns out not to be entitled to it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		// Wrong verifier: refused, and the code goes with it. Checking first
		// and consuming after would leave the code redeemable, so whoever holds
		// it could probe the other parameters until they matched.
		failed := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {"9876543210987654321098765432109876543210xyz"},
		})
		must.EqOp(t, http.StatusBadRequest, failed.status)

		retried := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {testVerifier},
		})

		test.EqOp(t, http.StatusBadRequest, retried.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, retried.Error)
	})

	T.Run("a code issued to one client is not redeemable by another", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		victim := h.registerConfidential()
		attacker := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(victim.ClientID), login()))

		out := h.token(attacker.ClientID, attacker.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {testVerifier},
		})

		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, out.Error)
	})

	T.Run("an unsupported grant_type is named as such", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		// OAuth 2.1 removes both of these, and there is no option that brings
		// them back.
		for _, grant := range []string{"password", "implicit", "client_credentials", ""} {
			out := h.token(reg.ClientID, reg.ClientSecret, url.Values{"grant_type": {grant}})

			test.EqOp(t, http.StatusBadRequest, out.status, test.Sprintf("grant %q", grant))
			test.EqOp(t, oauth2server.ErrorCodeUnsupportedGrantType, out.Error, test.Sprintf("grant %q", grant))
		}
	})
}

// Rotation without reuse detection is bookkeeping. This is the half that makes
// it worth doing.
func TestToken_RefreshReuse(T *testing.T) {
	T.Parallel()

	T.Run("presenting a rotated refresh token revokes the whole family", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		tokens := h.exchange(reg)

		refreshed := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})
		must.EqOp(t, http.StatusOK, refreshed.status)

		// The attacker's copy. Refused — and the refusal is what tells this
		// server that two parties hold tokens from one authorization.
		replayed := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})
		test.EqOp(t, http.StatusBadRequest, replayed.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, replayed.Error)

		// Everything minted under that authorization goes, the token the honest
		// client is holding right now included. Without this the replay is
		// refused and the copy the attacker is actually using keeps working.
		_, err := h.server.Authenticate(t.Context(), refreshed.AccessToken)
		test.ErrorIs(t, err, oauth2server.ErrNotFound)

		afterwards := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {refreshed.RefreshToken},
		})
		test.EqOp(t, http.StatusBadRequest, afterwards.status)
	})

	T.Run("detection can be turned off, and then the family survives", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithRefreshReuseDetection(false))
		reg := h.registerConfidential()

		tokens := h.exchange(reg)

		refreshed := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})
		must.EqOp(t, http.StatusOK, refreshed.status)

		replayed := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})
		must.EqOp(t, http.StatusBadRequest, replayed.status)

		// The switch exists because a client that loses the response to a
		// refresh and retries revokes its own session. What it costs is that a
		// theft now produces one failed request and no other trace.
		access, err := h.server.Authenticate(t.Context(), refreshed.AccessToken)
		must.NoError(t, err)
		test.NotNil(t, access)
	})

	T.Run("a refresh token presented by another client takes its family with it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		victim := h.registerConfidential()
		attacker := h.registerConfidential()

		tokens := h.exchange(victim)

		out := h.token(attacker.ClientID, attacker.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})
		must.EqOp(t, http.StatusBadRequest, out.status)

		// The token has moved. Whoever has it should not, so the family goes
		// for the same reason a replay does.
		_, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
	})

	T.Run("a refresh may narrow the granted scope but never widen it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithScopes("read", "write"))

		reg := h.register(map[string]any{
			"redirect_uris": []string{testRedirectURI},
			"scope":         "read write",
		})
		must.EqOp(t, http.StatusCreated, reg.status)

		params := authorizeParams(reg.ClientID)
		params.Set("scope", "read write")

		code := h.codeFrom(h.authorize(params, login()))

		tokens := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {testVerifier},
		})
		must.EqOp(t, http.StatusOK, tokens.status)

		narrowed := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
			"scope":         {"read"},
		})
		must.EqOp(t, http.StatusOK, narrowed.status)
		test.EqOp(t, "read", narrowed.Scope)

		widened := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {narrowed.RefreshToken},
			"scope":         {"read write"},
		})
		test.EqOp(t, http.StatusBadRequest, widened.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidScope, widened.Error)
	})
}

// RFC 6749 §4.1.2: a code presented twice revokes what it previously issued.
// This is the same threat refresh reuse detection answers, one step earlier —
// whoever wins the race to /token keeps a token pair, and the loser's replay is
// the only signal that there were two of them.
func TestToken_CodeReplay(T *testing.T) {
	T.Parallel()

	T.Run("a replayed authorization code revokes the pair it issued", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		// The attacker won the race, or the client did — from here they are
		// the same thing, and this is the pair at risk either way.
		tokens := h.redeem(reg, code)
		must.EqOp(t, http.StatusOK, tokens.status)

		// Usable up to here, so what the replay does below is a revocation
		// rather than a token that never worked.
		_, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		must.NoError(t, err)

		replayed := h.redeem(reg, code)
		test.EqOp(t, http.StatusBadRequest, replayed.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, replayed.Error)

		// Refusing the second redemption was never the hard part: the code is
		// spent atomically, so it was already refused. What the family on the
		// code buys is this — the pair the first redemption minted goes too.
		_, err = h.server.Authenticate(t.Context(), tokens.AccessToken)
		test.ErrorIs(t, err, oauth2server.ErrNotFound)

		afterwards := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})
		test.EqOp(t, http.StatusBadRequest, afterwards.status)
	})

	T.Run("turning refresh reuse detection off does not turn this off", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithRefreshReuseDetection(false))
		reg := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		tokens := h.redeem(reg, code)
		must.EqOp(t, http.StatusOK, tokens.status)

		must.EqOp(t, http.StatusBadRequest, h.redeem(reg, code).status)

		// That switch is there because a client that loses the response to a
		// rotation and retries revokes a session it is using. A replayed code
		// cannot cost that — a client holding the pair has nothing to retry —
		// so the switch does not reach this.
		_, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
	})

	T.Run("one authorization's family does not reach another's", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		first := h.exchange(reg)

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))
		second := h.redeem(reg, code)
		must.EqOp(t, http.StatusOK, second.status)

		must.EqOp(t, http.StatusBadRequest, h.redeem(reg, code).status)

		// The family is minted per authorization, so replaying the second
		// login's code does not sign the user out of the first one. A family
		// derived from the subject and client instead would.
		access, err := h.server.Authenticate(t.Context(), first.AccessToken)
		must.NoError(t, err)
		test.NotNil(t, access)
	})
}

func TestAuthorize_ScopesAndResources(T *testing.T) {
	T.Parallel()

	T.Run("a scope this server does not issue is refused, not narrowed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithScopes("read"))
		reg := h.registerConfidential()

		params := authorizeParams(reg.ClientID)
		params.Set("scope", "read admin")

		// Narrowed, this would hand back a token that looks like the one that
		// was asked for and is not — and the client would find out at the
		// resource server, in another process, as a 403 it cannot attribute.
		errorCode, _ := h.redirectError(h.authorize(params, login()))
		test.EqOp(t, oauth2server.ErrorCodeInvalidScope, errorCode)
	})

	T.Run("a resource this server does not serve is invalid_target", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithResources(testResource))
		reg := h.registerConfidential()

		params := authorizeParams(reg.ClientID)
		params.Set("resource", "https://elsewhere.example/")

		errorCode, _ := h.redirectError(h.authorize(params, login()))
		test.EqOp(t, oauth2server.ErrorCodeInvalidTarget, errorCode)
	})

	T.Run("a response_type other than code is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		params := authorizeParams(reg.ClientID)
		params.Set("response_type", "token")

		errorCode, _ := h.redirectError(h.authorize(params, login()))
		test.EqOp(t, oauth2server.ErrorCodeUnsupportedResponseType, errorCode)
	})

	T.Run("a token minted with no resource carries no audience", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		params := authorizeParams(reg.ClientID)
		params.Del("resource")

		code := h.codeFrom(h.authorize(params, login()))

		tokens := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {testVerifier},
		})
		must.EqOp(t, http.StatusOK, tokens.status)

		access, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		must.NoError(t, err)

		// A resource server that requires an audience must refuse this, which
		// is the correct end of the trade: a token with no audience is one that
		// can be replayed at any server trusting the same issuer.
		test.SliceEmpty(t, access.Audience)
	})
}

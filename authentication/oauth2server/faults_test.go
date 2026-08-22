package oauth2server_test

import (
	stderrors "errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/observability/logging"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// What a broken store does to each endpoint.
//
// Every one of these paths is the "and something else went wrong" branch beside
// a sentinel the protocol has an answer for — ErrNotFound at /token is an
// invalid_grant, ErrNotFound at /revoke is a 200 — and getting the two confused
// is how a database outage turns into an account enumeration oracle or into a
// 200 that revoked nothing.
func TestServer_StoreFailures(T *testing.T) {
	T.Parallel()

	T.Run("an unreadable registration is answered in the browser, not redirected", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		reg := h.registerConfidential()
		faults.breaks(methodGetClient, errStoreDown)

		res := h.get(oauth2server.PathAuthorize + "?" + authorizeParams(reg.ClientID).Encode())

		// Not a redirect: until the client registration has been read there is
		// nothing to say the redirect_uri belongs to anybody, so a broken store
		// must not become a redirect to an attacker-supplied address.
		test.EqOp(t, http.StatusInternalServerError, res.StatusCode)
		test.StrContains(t, readBody(t, res), oauth2server.ErrorCodeServerError)
	})

	T.Run("a code that cannot be stored reaches the client as server_error", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		reg := h.registerConfidential()
		faults.breaks(methodCreateCode, errStoreDown)

		errorCode, state := h.redirectError(h.authorize(authorizeParams(reg.ClientID), login()))

		// Redirected, unlike the case above: by this point the redirect URI has
		// been matched against the registration, so the client is entitled to
		// hear that its flow failed — and to match the answer to its own
		// pending request.
		test.EqOp(t, oauth2server.ErrorCodeServerError, errorCode)
		test.EqOp(t, "opaque-state", state)
	})

	T.Run("/token cannot authenticate a client it cannot read", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		reg := h.registerConfidential()
		faults.breaks(methodGetClient, errStoreDown)

		out := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type": {oauth2server.GrantTypeAuthorizationCode},
			"code":       {"whatever"},
		})

		// A 500 rather than the 401 an unknown client gets. The client did not
		// fail to authenticate; this server failed to check, and answering 401
		// would tell a client to go and get new credentials it does not need.
		test.EqOp(t, http.StatusInternalServerError, out.status)
		test.EqOp(t, oauth2server.ErrorCodeServerError, out.Error)
	})

	T.Run("a redemption that fails for no protocol reason is a server error", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		reg := h.registerConfidential()
		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		faults.breaks(methodConsumeCode, errStoreDown)

		out := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
			"code":          {code},
			"redirect_uri":  {testRedirectURI},
			"code_verifier": {testVerifier},
		})

		// Distinct from invalid_grant, which is what a client acts on by
		// starting a new authorization. A client told invalid_grant here would
		// send the human back through the login form to redeem a code that was
		// never spent.
		test.EqOp(t, http.StatusInternalServerError, out.status)
		test.EqOp(t, oauth2server.ErrorCodeServerError, out.Error)
	})

	T.Run("a token pair that cannot be stored is refused rather than half issued", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		reg := h.registerConfidential()
		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		faults.breaks(methodCreateAccessToken, errStoreDown)

		out := h.redeem(reg, code)
		test.EqOp(t, http.StatusInternalServerError, out.status)
		test.EqOp(t, oauth2server.ErrorCodeServerError, out.Error)
		test.EqOp(t, "", out.AccessToken)
	})

	T.Run("a refresh token that cannot be stored leaves a session that expires", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		reg := h.registerConfidential()
		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		faults.breaks(methodCreateRefreshToken, errStoreDown)

		out := h.redeem(reg, code)

		// The access token was written before the refresh token and survives
		// the failure, which is the ordering issueTokenPair documents: the
		// worst outcome here is a session that ends in fifteen minutes, not a
		// client holding a refresh token for a session it was never given.
		test.EqOp(t, http.StatusInternalServerError, out.status)
		test.EqOp(t, "", out.RefreshToken)
	})

	T.Run("a rotation that fails for no protocol reason is a server error", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		faults.breaks(methodConsumeRefresh, errStoreDown)

		out := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})

		test.EqOp(t, http.StatusInternalServerError, out.status)
		test.EqOp(t, oauth2server.ErrorCodeServerError, out.Error)
	})

	T.Run("a rotation whose new tokens cannot be stored is a server error", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		faults.breaks(methodCreateAccessToken, errStoreDown)

		// The previous refresh token has already been spent by the time this
		// fails, which is what makes it a 500 rather than something the client
		// can retry: there is no longer a credential to retry with.
		out := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})

		test.EqOp(t, http.StatusInternalServerError, out.status)
		test.EqOp(t, oauth2server.ErrorCodeServerError, out.Error)
	})

	T.Run("a family that cannot be revoked still refuses the replay", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		refresh := url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		}

		must.EqOp(t, http.StatusOK, h.token(reg.ClientID, reg.ClientSecret, refresh).status)

		faults.breaks(methodRevokeFamily, errStoreDown)

		// The revocation is best effort and the refusal is not. A store that
		// cannot revoke leaves a family live that should not be — which is
		// worth a recorded error and not worth turning this into a 500 the
		// client would retry with the same replayed token.
		out := h.token(reg.ClientID, reg.ClientSecret, refresh)
		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, out.Error)
	})

	T.Run("a replayed code whose family cannot be revoked is still refused", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		reg := h.registerConfidential()
		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		must.EqOp(t, http.StatusOK, h.redeem(reg, code).status)

		faults.breaks(methodRevokeFamily, errStoreDown)

		// Best effort here for the same reason it is on the refresh path: the
		// replay is refused either way, and a 500 is an invitation to retry
		// with the same spent code.
		out := h.redeem(reg, code)
		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, out.Error)
	})

	T.Run("/register answers 500 when the registration cannot be stored", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		faults.breaks(methodCreateClient, errStoreDown)

		reg := h.register(map[string]any{"redirect_uris": []string{testRedirectURI}})
		test.EqOp(t, http.StatusInternalServerError, reg.status)
		test.EqOp(t, "", reg.ClientID)
	})

	T.Run("/revoke answers 200 to a store it cannot read", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		faults.breaks(methodGetAccessToken, errStoreDown)
		faults.breaks(methodGetRefreshToken, errStoreDown)

		// RFC 7009 §2.2 gives this endpoint one answer, and a broken store does
		// not earn it a second one: a 500 here would let a caller tell a
		// database outage apart from a token that does not exist.
		test.EqOp(t, http.StatusOK, h.revoke(reg.ClientID, reg.ClientSecret, tokens.AccessToken, ""))
	})

	T.Run("/revoke answers 200 when the revocation itself fails", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		faults.breaks(methodRevokeAccessToken, errStoreDown)

		test.EqOp(t, http.StatusOK, h.revoke(reg.ClientID, reg.ClientSecret, tokens.AccessToken, ""))

		// And the token is still live, which is exactly why the failure has to
		// be recorded somewhere: the client has been told nothing.
		faults.heals(methodRevokeAccessToken)

		token, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		must.NoError(t, err)
		test.EqOp(t, reg.ClientID, token.ClientID)
	})

	T.Run("Authenticate reports a broken store rather than an unknown token", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h := newStoreHarness(t, faults)

		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		faults.breaks(methodGetAccessToken, errStoreDown)

		got, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		test.Nil(t, got)
		must.Error(t, err)

		// A resource server branching on ErrNotFound would answer 401 and send
		// a live session back through a login it does not need.
		test.False(t, stderrors.Is(err, oauth2server.ErrNotFound))
	})
}

// The severity split, which is the one thing about these refusals that has no
// visible effect on the response.
func TestServer_RefusalSeverity(T *testing.T) {
	T.Parallel()

	T.Run("a client's own mistake is not recorded as a fault", func(t *testing.T) {
		t.Parallel()

		h, logger, spans := newObservedHarness(t, newFaultStore())

		// An unknown client_id: a 400, and entirely the caller's doing.
		res := h.get(oauth2server.PathAuthorize + "?" + authorizeParams("client_nobody_registered").Encode())
		must.EqOp(t, http.StatusBadRequest, res.StatusCode)

		test.SliceEmpty(t, logger.at(logging.ErrorLevel))
		test.SliceEmpty(t, spans.errored())
		test.SliceNotEmpty(t, logger.at(logging.InfoLevel))
	})

	T.Run("a broken store is", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h, logger, spans := newObservedHarness(t, faults)

		reg := h.registerConfidential()
		faults.breaks(methodGetClient, errStoreDown)

		res := h.get(oauth2server.PathAuthorize + "?" + authorizeParams(reg.ClientID).Encode())
		must.EqOp(t, http.StatusInternalServerError, res.StatusCode)

		// Both pillars, because the two are read by different people: the span
		// is what an operator finds while looking at a slow endpoint, and the
		// line is what a log-based alert fires on.
		errors := logger.at(logging.ErrorLevel)
		must.SliceNotEmpty(t, errors)
		test.StrContains(t, errors[0].err.Error(), oauth2server.ErrorCodeServerError)
		test.SliceNotEmpty(t, spans.errored())
	})
}

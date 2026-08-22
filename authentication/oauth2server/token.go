package oauth2server

import (
	"context"
	stderrors "errors"
	"net/http"
	"slices"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// TokenHandler serves POST /token: the authorization_code and refresh_token
// grants.
func (s *Server) TokenHandler() http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		ctx, op := s.o11y.BeginCustom(req.Context(), operationName(endpointToken))
		defer s.end(ctx, op, endpointToken, s.clock.Now())

		s.ops.Attempt(ctx, metric.WithAttributes(attribute.String(endpointKey, endpointToken)))

		if err := req.ParseForm(); err != nil {
			writeProtocolError(res, s.fail(ctx, op, endpointToken,
				newProtocolError(http.StatusBadRequest, ErrorCodeInvalidRequest, "malformed request", err)))

			return
		}

		client, perr := s.authenticateClient(ctx, req)
		if perr != nil {
			writeProtocolError(res, s.fail(ctx, op, endpointToken, perr))

			return
		}

		grantType := req.Form.Get(paramGrantType)
		op.Set(clientIDKey, client.ID).Set(grantTypeKey, grantType)

		var response *TokenResponse

		switch grantType {
		case GrantTypeAuthorizationCode:
			response, perr = s.grantAuthorizationCode(ctx, op, req, client)
		case GrantTypeRefreshToken:
			response, perr = s.grantRefreshToken(ctx, op, req, client)
		default:
			perr = newProtocolError(http.StatusBadRequest, ErrorCodeUnsupportedGrantType,
				"unsupported grant_type", platformerrors.Wrapf(ErrUnsupportedGrantType, "%q", grantType))
		}

		if perr != nil {
			writeProtocolError(res, s.fail(ctx, op, endpointToken, perr))

			return
		}

		writeJSON(res, http.StatusOK, response)
	})
}

// authenticateClient resolves and verifies the client behind a token or
// revocation request.
//
// This is the check the map-backed examples do not have. Their metadata
// advertises client_secret_post and their endpoint reads no secret, comparing
// client_id only against the one recorded on the authorization code — so the
// registration is decorative, and any caller holding a stolen code can present
// any client_id they like.
//
// A public client is a real thing and is handled here too: it presents no
// secret and is bound by PKCE instead. What is refused is the middle case — a
// client that registered a secret and did not present one.
func (s *Server) authenticateClient(ctx context.Context, req *http.Request) (*Client, *protocolError) {
	clientID, secret, viaBasic := req.BasicAuth()
	if !viaBasic {
		clientID, secret = req.Form.Get(paramClientID), req.Form.Get(paramClientSecret)
	}

	if clientID == "" {
		return nil, newProtocolError(http.StatusUnauthorized, ErrorCodeInvalidClient,
			"client authentication is required", ErrClientAuthenticationFailed)
	}

	client, err := s.store.GetClient(ctx, clientID)
	if err != nil {
		if stderrors.Is(err, ErrNotFound) {
			// The same answer as a wrong secret, and at the same status: an
			// endpoint that distinguished them would tell an anonymous caller
			// which client identifiers exist.
			return nil, newProtocolError(http.StatusUnauthorized, ErrorCodeInvalidClient,
				"client authentication failed", platformerrors.Wrap(ErrUnknownClient, clientID))
		}

		return nil, newProtocolError(http.StatusInternalServerError, ErrorCodeServerError,
			"could not read the client registration", err)
	}

	if client.Public() {
		// Nothing to verify. A public client is authenticated by holding the
		// PKCE verifier for the code it is redeeming, which the grant checks.
		//
		// A secret sent by a client that registered none is refused rather than
		// ignored: it means one side of this exchange is confused about which
		// client it is, and the confusion is worth a 401 rather than a token.
		if secret != "" {
			return nil, newProtocolError(http.StatusUnauthorized, ErrorCodeInvalidClient,
				"client authentication failed", platformerrors.Wrap(ErrClientAuthenticationFailed, "secret presented by a public client"))
		}

		return client, nil
	}

	if secret == "" || !equalHash(client.SecretHash, Hash(secret)) {
		return nil, newProtocolError(http.StatusUnauthorized, ErrorCodeInvalidClient,
			"client authentication failed", ErrClientAuthenticationFailed)
	}

	// A client that registered client_secret_basic and posts its secret in the
	// body is accepted. RFC 6749 §2.3.1 says a server MUST support Basic and
	// MAY support the form parameter; refusing the form because the
	// registration named the other one would break clients over a preference
	// neither end can see, and both carry the same secret over the same TLS.
	return client, nil
}

// grantAuthorizationCode exchanges an authorization code for a token pair.
func (s *Server) grantAuthorizationCode(
	ctx context.Context,
	op observability.Operation,
	req *http.Request,
	client *Client,
) (*TokenResponse, *protocolError) {
	if !clientAllows(client.GrantTypes, GrantTypeAuthorizationCode) {
		return nil, newProtocolError(http.StatusBadRequest, ErrorCodeUnauthorizedClient,
			"this client is not registered for the authorization_code grant", ErrUnsupportedGrantType)
	}

	value := req.Form.Get(paramCode)
	if value == "" {
		return nil, newProtocolError(http.StatusBadRequest, ErrorCodeInvalidRequest,
			"code is required", platformerrors.ErrEmptyInputParameter)
	}

	verifier := req.Form.Get(paramCodeVerifier)
	if !validCodeVerifier(verifier) {
		return nil, newProtocolError(http.StatusBadRequest, ErrorCodeInvalidRequest,
			"code_verifier is required and must be 43 to 128 unreserved characters", ErrPKCERequired)
	}

	// Consumed before anything about it is checked, and that ordering is the
	// point: the code is spent by the first request that presents it, whether
	// or not that request turns out to be entitled to it. Checking first would
	// leave a code redeemable after a failed attempt, so an attacker who holds
	// one could probe the other parameters until they matched.
	code, err := s.store.ConsumeAuthorizationCode(ctx, Hash(value))
	if err != nil {
		return nil, s.codeRedemptionError(ctx, op, code, err)
	}

	if code.ClientID != client.ID {
		return nil, newProtocolError(http.StatusBadRequest, ErrorCodeInvalidGrant,
			"authorization code was issued to a different client", ErrCodeClientMismatch)
	}

	// Re-checked here, not merely at /authorize. The redirect_uri the code was
	// issued against is part of what the code is bound to, and a token request
	// naming a different one is a request to complete somebody else's flow.
	if redirectURI := req.Form.Get(paramRedirectURI); redirectURI != code.RedirectURI {
		return nil, newProtocolError(http.StatusBadRequest, ErrorCodeInvalidGrant,
			"redirect_uri does not match the authorization request", ErrRedirectURIMismatch)
	}

	if !VerifyPKCE(verifier, code.CodeChallenge) {
		return nil, newProtocolError(http.StatusBadRequest, ErrorCodeInvalidGrant,
			"code_verifier does not match the code_challenge", ErrPKCEVerificationFailed)
	}

	// The family came with the code, minted at /authorize so that a replay of
	// this code can name what it issued. A code carrying none was written by
	// something other than this server — a hand-built record, or a row from
	// before the column existed — and gets one now: an empty family is not a
	// family, and issuing a pair under it would put every such token in one
	// group that RevokeFamily then refuses to touch.
	family := code.FamilyID
	if family == "" {
		family = newFamilyID()
	}

	op.Set(familyIDKey, family).Set(scopeKey, joinScopes(code.Scopes))

	response, err := s.issueTokenPair(ctx, client.ID, family,
		code.Subject, code.Scopes, audienceFor(code.Resources), code.Resources)
	if err != nil {
		return nil, newProtocolError(http.StatusInternalServerError, ErrorCodeServerError, "could not issue tokens", err)
	}

	return response, nil
}

// codeRedemptionError renders a failed code consumption, revoking whatever the
// code previously issued if it turns out to be a replay.
func (s *Server) codeRedemptionError(
	ctx context.Context,
	op observability.Operation,
	code *AuthorizationCode,
	err error,
) *protocolError {
	if stderrors.Is(err, ErrAlreadyRedeemed) {
		// RFC 6749 §4.1.2: a code presented twice revokes the tokens it
		// previously issued. Whoever won the race to redeem it holds a pair
		// that whoever lost was supposed to have, and the replay is the only
		// place this server ever hears that there were two of them.
		s.handleCodeReplay(ctx, op, code)

		return newProtocolError(http.StatusBadRequest, ErrorCodeInvalidGrant,
			"authorization code has already been redeemed", err)
	}

	if stderrors.Is(err, ErrNotFound) {
		return newProtocolError(http.StatusBadRequest, ErrorCodeInvalidGrant,
			"authorization code is invalid or expired", err)
	}

	return newProtocolError(http.StatusInternalServerError, ErrorCodeServerError,
		"could not redeem the authorization code", err)
}

// grantRefreshToken rotates a refresh token, detecting reuse.
func (s *Server) grantRefreshToken(
	ctx context.Context,
	op observability.Operation,
	req *http.Request,
	client *Client,
) (*TokenResponse, *protocolError) {
	if !clientAllows(client.GrantTypes, GrantTypeRefreshToken) {
		return nil, newProtocolError(http.StatusBadRequest, ErrorCodeUnauthorizedClient,
			"this client is not registered for the refresh_token grant", ErrUnsupportedGrantType)
	}

	value := req.Form.Get(paramRefreshToken)
	if value == "" {
		return nil, newProtocolError(http.StatusBadRequest, ErrorCodeInvalidRequest,
			"refresh_token is required", platformerrors.ErrEmptyInputParameter)
	}

	previous, err := s.store.ConsumeRefreshToken(ctx, Hash(value))
	if err != nil {
		return nil, s.refreshRotationError(ctx, op, previous, err)
	}

	if previous.ClientID != client.ID {
		// Presented by a client other than the one it was issued to, which
		// means the token has moved. The family goes, for the same reason a
		// replay does: whoever has it should not.
		s.revokeFamily(ctx, op, previous.FamilyID, "refresh token presented by another client")

		return nil, newProtocolError(http.StatusBadRequest, ErrorCodeInvalidGrant,
			"refresh token was issued to a different client", ErrCodeClientMismatch)
	}

	scopes := previous.Scopes

	// RFC 6749 §6: a refresh request may narrow the scope but never widen it.
	if requested := splitScopes(req.Form.Get(paramScope)); len(requested) > 0 {
		if !subsetOf(requested, previous.Scopes) {
			return nil, newProtocolError(http.StatusBadRequest, ErrorCodeInvalidScope,
				"refresh cannot widen the granted scope", ErrInvalidScope)
		}

		scopes = requested
	}

	op.Set(familyIDKey, previous.FamilyID).Set(scopeKey, joinScopes(scopes))

	response, err := s.issueTokenPair(ctx, client.ID, previous.FamilyID,
		previous.Subject, scopes, previous.Audience, previous.Resources)
	if err != nil {
		return nil, newProtocolError(http.StatusInternalServerError, ErrorCodeServerError, "could not issue tokens", err)
	}

	return response, nil
}

// refreshRotationError renders a failed rotation, revoking the family when the
// failure is a replay.
//
// The replay branch is the half of rotation that makes rotation worth doing.
// Without it a stolen refresh token is refused once and the copy the victim
// holds keeps working, so the theft leaves one failed request and no other
// trace — which is indistinguishable from a flaky network.
func (s *Server) refreshRotationError(
	ctx context.Context,
	op observability.Operation,
	previous *RefreshToken,
	err error,
) *protocolError {
	if stderrors.Is(err, ErrAlreadyRedeemed) {
		s.reuseDetected.Add(ctx, 1)

		if s.detectRefreshReuse && previous != nil {
			s.revokeFamily(ctx, op, previous.FamilyID, "refresh token reuse detected")
		}

		return newProtocolError(http.StatusBadRequest, ErrorCodeInvalidGrant,
			"refresh token has already been used", err)
	}

	if stderrors.Is(err, ErrNotFound) {
		return newProtocolError(http.StatusBadRequest, ErrorCodeInvalidGrant,
			"refresh token is invalid, expired, or revoked", err)
	}

	return newProtocolError(http.StatusInternalServerError, ErrorCodeServerError,
		"could not rotate the refresh token", err)
}

// revokeFamily revokes a token family and records why, reporting how many
// records went with it.
//
// Best-effort by design: the caller is refusing the request either way, and a
// store that cannot write leaves a family live that should not be — which is
// worth a recorded error and not worth turning a 400 into a 500 the client
// would retry. The count is zero both for a family that was already gone and
// for a store that could not be written, which is what /revoke's observer has
// to tell a real revocation from a no-op.
func (s *Server) revokeFamily(ctx context.Context, op observability.Operation, familyID, reason string) int64 {
	if familyID == "" {
		return 0
	}

	revoked, err := s.store.RevokeFamily(ctx, familyID)
	if err != nil {
		op.Acknowledge(err, "revoking token family after %s", reason)

		return 0
	}

	s.revocations.Add(ctx, revoked)
	op.Set(familyIDKey, familyID).Set(revokedKey, revoked)
	op.Logger().WithValue(familyIDKey, familyID).WithValue(revokedKey, revoked).Info(reason)

	return revoked
}

// handleCodeReplay revokes what a twice-presented authorization code issued,
// and records that it happened.
//
// The family is on the code, minted at /authorize, which is what makes this
// possible at all: the replay hands back the record, the record names the
// family, and the family is exactly the pair the first redemption minted.
//
// Unlike refresh reuse this has no switch, and the asymmetry is the argument
// for it. WithRefreshReuseDetection exists because a client that loses the
// response to a rotation and retries revokes a session it is in the middle of
// using — a real cost, paid by a client nobody can fix. A replayed code cannot
// cost that: a client that received the pair has nothing to retry, so a client
// presenting the code again is one that never got what the code minted, and
// what is revoked is a pair nobody is holding. The recovery is a fresh
// /authorize, which the client just came from.
//
// A code with no family is left alone rather than revoked by an empty
// identifier, which would be a predicate matching every token that also names
// none. The counter and the log line still happen: a replay is either a client
// bug or somebody holding a code they should not, and
// oauth2server_refresh_reuse_detected is where both show up.
func (s *Server) handleCodeReplay(ctx context.Context, op observability.Operation, code *AuthorizationCode) {
	if code == nil {
		return
	}

	s.reuseDetected.Add(ctx, 1)
	op.Set(clientIDKey, code.ClientID)
	op.Logger().WithValue(clientIDKey, code.ClientID).Info("authorization code replayed")

	s.revokeFamily(ctx, op, code.FamilyID, "authorization code replay detected")
}

// audienceFor renders the audience an access token carries for a set of
// requested resource indicators.
//
// The audience is the resources verbatim. A token minted with no resource
// indicator carries no audience, and a resource server that requires one must
// refuse it — which is the correct end of that trade: RFC 8707 exists so that a
// token minted for one resource server cannot be replayed at another, and a
// token with no audience is one that can be.
func audienceFor(resources []string) []string {
	return slices.Clone(resources)
}

// subsetOf reports whether every element of a is in b.
func subsetOf(a, b []string) bool {
	for _, item := range a {
		if !slices.Contains(b, item) {
			return false
		}
	}

	return true
}

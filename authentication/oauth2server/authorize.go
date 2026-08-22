package oauth2server

import (
	"context"
	stderrors "errors"
	"net/http"
	"net/url"
	"slices"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// authorizeRequest is one /authorize request, after the parameters have been
// read and the client resolved.
type authorizeRequest struct {
	client *Client

	redirectURI   string
	state         string
	nonce         string
	codeChallenge string

	scopes    []string
	resources []string
}

// AuthorizeHandler serves GET and POST /authorize.
//
// Both methods run exactly the same validation, and that is the point of
// putting the authorization parameters in the query string on both. The GET
// renders the login form; the form posts back to the same URL with the same
// query, so the POST re-derives the client, the redirect URI, the scopes and
// the PKCE challenge from the same bytes the GET was checked against. Carrying
// them across in hidden form fields instead would mean the request that issues
// the code is not the request that was validated.
//
// Where the method stops mattering is the optional SubjectResolver, which is
// asked on both before either the form or the SubjectAuthenticator. A request
// that already proves who its resource owner is — a session cookie, a bearer
// token — redirects with a code whichever verb it arrived on, so a client with
// nothing to type is not made to POST to say so.
func (s *Server) AuthorizeHandler() http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		ctx, op := s.o11y.BeginCustom(req.Context(), operationName(endpointAuthorize))
		defer s.end(ctx, op, endpointAuthorize, s.clock.Now())

		s.ops.Attempt(ctx, metric.WithAttributes(attribute.String(endpointKey, endpointAuthorize)))

		if err := req.ParseForm(); err != nil {
			s.writeAuthorizeFault(res, s.fail(ctx, op, endpointAuthorize,
				newProtocolError(http.StatusBadRequest, ErrorCodeInvalidRequest, "malformed request", err)))

			return
		}

		// Everything up to here is answered in the browser, because until the
		// redirect URI is known to be the client's there is nowhere safe to
		// send an error to.
		authorized, perr := s.resolveAuthorizeRequest(ctx, req)
		if perr != nil {
			s.writeAuthorizeFault(res, s.fail(ctx, op, endpointAuthorize, perr))

			return
		}

		op.Set(clientIDKey, authorized.client.ID).Set(scopeKey, joinScopes(authorized.scopes))

		// From here every failure is the client's to hear about, at the URI it
		// registered, with the state it sent so it can match the answer to its
		// own pending request.
		if perr = s.validateAuthorizeRequest(req, authorized); perr != nil {
			s.redirectError(res, req, authorized, s.fail(ctx, op, endpointAuthorize, perr))

			return
		}

		// Consulted before the method is looked at, so a request that already
		// carries proof of who its owner is never meets a form — and a client
		// with nothing to type never has to POST an empty body to say so.
		resolved, perr := s.resolveSubject(ctx, req)
		if perr != nil {
			s.redirectError(res, req, authorized, s.fail(ctx, op, endpointAuthorize, perr))

			return
		}

		if resolved != nil {
			s.issueCode(ctx, op, res, req, authorized, *resolved)

			return
		}

		if req.Method != http.MethodPost {
			s.renderLogin(ctx, res, req, authorized, "")

			return
		}

		subject, err := s.authenticator.AuthenticateSubject(ctx, req)
		switch {
		case err == nil && subject == nil:
			// A nil subject with no error is an authenticator that meant to
			// refuse and forgot to say so. Treating it as success would issue a
			// code for the empty subject.
			err = ErrLoginFailed
		case err == nil && subject.ID == "":
			err = platformerrors.Wrap(ErrLoginFailed, "authenticated subject has no identifier")
		}

		if err != nil {
			if stderrors.Is(err, ErrLoginFailed) {
				// The human is still here, so the answer is the form again
				// rather than a redirect that ends the attempt.
				op.Logger().WithError(err).Info("resource owner authentication failed")
				s.renderLogin(ctx, res, req, authorized, loginMessage(err))

				return
			}

			s.redirectError(res, req, authorized, s.fail(ctx, op, endpointAuthorize,
				newProtocolError(http.StatusInternalServerError, ErrorCodeServerError, "could not authenticate the resource owner", err)))

			return
		}

		s.issueCode(ctx, op, res, req, authorized, *subject)
	})
}

// resolveSubject asks the optional SubjectResolver whether this request
// already carries proof of who its resource owner is.
//
// A nil resolver, and a resolver answering (nil, nil), both mean the same
// thing to the handler — carry on to the form — which is what keeps a Server
// built without one behaving exactly as it did before.
//
// An error is never rendered as a form. The resolver's caller is by
// construction something that presented a credential rather than something
// with a field to type in, so the answer to a refused one is the redirect that
// ends the attempt, not a page it cannot use.
func (s *Server) resolveSubject(ctx context.Context, req *http.Request) (*Subject, *protocolError) {
	if s.resolver == nil {
		return nil, nil
	}

	subject, err := s.resolver.ResolveSubject(ctx, req)
	if err != nil {
		return nil, newProtocolError(http.StatusInternalServerError, ErrorCodeServerError,
			"could not resolve the resource owner", err)
	}

	if subject != nil && subject.ID == "" {
		// The same refusal the authenticator path makes, for the same reason:
		// a token whose subject is the empty string authorizes whoever the
		// resource server decides the empty string is. A resolver that meant
		// to decline says so with a nil Subject.
		return nil, newProtocolError(http.StatusInternalServerError, ErrorCodeServerError,
			"could not resolve the resource owner",
			platformerrors.Wrap(ErrLoginFailed, "resolved subject has no identifier"))
	}

	return subject, nil
}

// resolveAuthorizeRequest reads the parameters that decide where an error may
// be sent: the client and the redirect URI.
//
// Both failures here are answered in the browser rather than redirected, and
// the redirect_uri case is the one that matters. Sending "your redirect_uri is
// wrong" to the redirect_uri would be sending it to an address this server has
// just decided is not the client's — which is the whole attack registered URIs
// exist to stop.
func (s *Server) resolveAuthorizeRequest(ctx context.Context, req *http.Request) (*authorizeRequest, *protocolError) {
	clientID := req.Form.Get(paramClientID)
	if clientID == "" {
		return nil, newProtocolError(http.StatusBadRequest, ErrorCodeInvalidRequest,
			"client_id is required", platformerrors.ErrEmptyInputParameter)
	}

	client, err := s.store.GetClient(ctx, clientID)
	if err != nil {
		if stderrors.Is(err, ErrNotFound) {
			return nil, newProtocolError(http.StatusBadRequest, ErrorCodeInvalidClient,
				"unknown or expired client_id", platformerrors.Wrap(ErrUnknownClient, clientID))
		}

		return nil, newProtocolError(http.StatusInternalServerError, ErrorCodeServerError,
			"could not read the client registration", err)
	}

	redirectURI := req.Form.Get(paramRedirectURI)
	if redirectURI == "" {
		// Not defaulted to the single registered URI, even though RFC 6749
		// permits that when there is exactly one. A client that omits it is a
		// client that has not decided, and OAuth 2.1 requires the parameter for
		// the same reason exact matching exists.
		return nil, newProtocolError(http.StatusBadRequest, ErrorCodeInvalidRequest,
			"redirect_uri is required", platformerrors.ErrEmptyInputParameter)
	}

	// Exact, byte for byte. Not a prefix, not "same host", not "ignoring the
	// query string" — every one of those has been somebody's takeover.
	if !slices.Contains(client.RedirectURIs, redirectURI) {
		return nil, newProtocolError(http.StatusBadRequest, ErrorCodeInvalidRequest,
			"redirect_uri is not registered for this client", ErrInvalidRedirectURI)
	}

	return &authorizeRequest{
		client:        client,
		redirectURI:   redirectURI,
		state:         req.Form.Get(paramState),
		nonce:         req.Form.Get(paramNonce),
		codeChallenge: req.Form.Get(paramCodeChallenge),
		scopes:        splitScopes(req.Form.Get(paramScope)),
		resources:     req.Form[paramResource],
	}, nil
}

// validateAuthorizeRequest checks everything whose failure can safely be sent
// to the client's registered redirect URI.
func (s *Server) validateAuthorizeRequest(req *http.Request, authorized *authorizeRequest) *protocolError {
	if rt := req.Form.Get(paramResponseType); rt != ResponseTypeCode {
		return newProtocolError(http.StatusBadRequest, ErrorCodeUnsupportedResponseType,
			"only the code response_type is supported", platformerrors.Wrapf(ErrUnsupportedResponseType, "%q", rt))
	}

	if !clientAllows(authorized.client.GrantTypes, GrantTypeAuthorizationCode) {
		return newProtocolError(http.StatusBadRequest, ErrorCodeUnauthorizedClient,
			"this client is not registered for the authorization_code grant", ErrUnsupportedGrantType)
	}

	// PKCE, always. There is no configuration that turns this off: a
	// confidential client gains nothing by skipping it, and a public one is
	// defenseless without it.
	if authorized.codeChallenge == "" {
		return newProtocolError(http.StatusBadRequest, ErrorCodeInvalidRequest,
			"code_challenge is required", ErrPKCERequired)
	}

	if method := req.Form.Get(paramCodeChallengeMethod); method != CodeChallengeMethodS256 {
		// An absent method defaults to "plain" under RFC 7636, so silence is
		// not agreement — it is a request for the method this server refuses.
		return newProtocolError(http.StatusBadRequest, ErrorCodeInvalidRequest,
			"code_challenge_method must be S256", platformerrors.Wrapf(ErrPKCERequired, "method %q", method))
	}

	if !validCodeChallenge(authorized.codeChallenge) {
		return newProtocolError(http.StatusBadRequest, ErrorCodeInvalidRequest,
			"code_challenge is not a base64url-encoded S256 digest", ErrPKCERequired)
	}

	if err := s.checkScopes(authorized.client, authorized.scopes); err != nil {
		return newProtocolError(http.StatusBadRequest, ErrorCodeInvalidScope,
			"requested scope is not available to this client", err)
	}

	if err := s.checkResources(authorized.resources); err != nil {
		return newProtocolError(http.StatusBadRequest, ErrorCodeInvalidTarget,
			"requested resource is not served by this authorization server", err)
	}

	return nil
}

// issueCode mints an authorization code for an authenticated subject and
// redirects the browser back to the client with it.
func (s *Server) issueCode(
	ctx context.Context,
	op observability.Operation,
	res http.ResponseWriter,
	req *http.Request,
	authorized *authorizeRequest,
	subject Subject,
) {
	value, digest, err := mintCredential(ctx)
	if err != nil {
		s.redirectError(res, req, authorized, s.fail(ctx, op, endpointAuthorize,
			newProtocolError(http.StatusInternalServerError, ErrorCodeServerError, "could not issue an authorization code", err)))

		return
	}

	now := s.now()

	// The family is decided here rather than at the redemption that uses it,
	// so that a code presented twice names the tokens the first presentation
	// issued. See AuthorizationCode.FamilyID.
	family := newFamilyID()
	op.Set(familyIDKey, family)

	code := &AuthorizationCode{
		IssuedAt:      now,
		ExpiresAt:     now.Add(s.codeTTL),
		Hash:          digest,
		ClientID:      authorized.client.ID,
		FamilyID:      family,
		RedirectURI:   authorized.redirectURI,
		CodeChallenge: authorized.codeChallenge,
		Nonce:         authorized.nonce,
		Subject:       subject,
		Scopes:        authorized.scopes,
		Resources:     authorized.resources,
	}

	if err = s.store.CreateAuthorizationCode(ctx, code); err != nil {
		s.redirectError(res, req, authorized, s.fail(ctx, op, endpointAuthorize,
			newProtocolError(http.StatusInternalServerError, ErrorCodeServerError, "could not issue an authorization code", err)))

		return
	}

	s.codesIssued.Add(ctx, 1)

	params := url.Values{paramCode: {value}}
	if authorized.state != "" {
		params.Set(paramState, authorized.state)
	}

	// RFC 9207. A client configured with more than one authorization server
	// cannot otherwise tell which one answered, which is what a mix-up attack
	// depends on.
	params.Set(paramIss, s.issuer)

	s.redirect(res, req, authorized.redirectURI, params)
}

// renderLogin draws the login form for an authorization request.
func (s *Server) renderLogin(
	ctx context.Context,
	res http.ResponseWriter,
	req *http.Request,
	authorized *authorizeRequest,
	message string,
) {
	name := authorized.client.Name
	if name == "" {
		// Better the identifier than nothing: the human is being asked to
		// approve something, and "" is not something.
		name = authorized.client.ID
	}

	s.renderer.RenderLogin(ctx, res, LoginView{
		// The original query, so the POST is validated against the same
		// parameters this GET was. RequestURI rather than a rebuilt URL, so a
		// parameter this package does not read survives the round trip.
		Action:     req.URL.RequestURI(),
		Error:      message,
		ClientName: name,
		Scopes:     slices.Clone(authorized.scopes),
	})
}

// redirect sends the browser back to the client.
//
// The parameters are merged into whatever query the registered URI already
// carries rather than replacing it: a client that registered
// "https://app.example/cb?tenant=acme" gets its own parameter back, which is
// the only reason it put one there.
func (s *Server) redirect(res http.ResponseWriter, req *http.Request, redirectURI string, params url.Values) {
	target, err := url.Parse(redirectURI)
	if err != nil {
		// Unreachable in practice: this URI was validated at registration and
		// matched exactly at /authorize. If it somehow is not parseable, the
		// browser is the only place left to say so, and it must not be a
		// redirect to an unparsed string.
		http.Error(res, "invalid redirect_uri", http.StatusBadRequest)

		return
	}

	query := target.Query()
	for key, values := range params {
		for _, value := range values {
			query.Set(key, value)
		}
	}

	target.RawQuery = query.Encode()

	// 302 rather than 303: this is what every OAuth client expects, and the
	// response has no body a GET-after-POST would be avoiding.
	res.Header().Set("Cache-Control", "no-store")
	http.Redirect(res, req, target.String(), http.StatusFound)
}

// redirectError sends a protocol error to the client's registered redirect URI.
func (s *Server) redirectError(res http.ResponseWriter, req *http.Request, authorized *authorizeRequest, perr *protocolError) {
	params := url.Values{paramError: {perr.code}}
	if perr.description != "" {
		params.Set(paramErrorDescription, perr.description)
	}
	if authorized.state != "" {
		params.Set(paramState, authorized.state)
	}
	params.Set(paramIss, s.issuer)

	s.redirect(res, req, authorized.redirectURI, params)
}

// writeAuthorizeFault answers an /authorize failure that cannot be redirected.
//
// It is text rather than the JSON the other endpoints send, because what is
// looking at it is a browser: this is the response a human sees when a client
// sends a redirect_uri that is not its own, and a JSON body in a browser window
// tells them nothing.
func (s *Server) writeAuthorizeFault(res http.ResponseWriter, perr *protocolError) {
	res.Header().Set("Content-Type", "text/plain; charset=utf-8")
	res.Header().Set("Cache-Control", "no-store")
	res.WriteHeader(perr.status)

	//nolint:errcheck // the status is already on the wire; a failed write has no error page left to send.
	_, _ = res.Write([]byte(perr.code + ": " + perr.description + "\n"))
}

// checkScopes reports whether every requested scope is one the client
// registered for and one this server issues.
//
// Refused rather than narrowed. A narrowed token looks like the token that was
// asked for and is not, and the client discovers the difference at the resource
// server, in another process, as a 403 it has no way to attribute.
func (s *Server) checkScopes(client *Client, requested []string) error {
	for _, scope := range requested {
		if len(client.Scopes) > 0 && !slices.Contains(client.Scopes, scope) {
			return platformerrors.Wrapf(ErrInvalidScope, "client is not registered for scope %q", scope)
		}

		if len(s.scopes) > 0 && !slices.Contains(s.scopes, scope) {
			return platformerrors.Wrapf(ErrInvalidScope, "this server does not issue scope %q", scope)
		}
	}

	return nil
}

// checkResources reports whether every requested RFC 8707 resource indicator is
// one this server mints tokens for.
func (s *Server) checkResources(requested []string) error {
	if len(s.resources) == 0 {
		return nil
	}

	for _, resource := range requested {
		if !slices.Contains(s.resources, resource) {
			return platformerrors.Wrapf(ErrInvalidResource, "%q", resource)
		}
	}

	return nil
}

// clientAllows reports whether a registration permits something. An empty list
// is permissive, which is what RFC 7591's defaults amount to — a registration
// that named no grant types gets the defaults filled in at /register, so an
// empty list here means a Client built by hand rather than one this server
// registered.
func clientAllows(registered []string, want string) bool {
	return len(registered) == 0 || slices.Contains(registered, want)
}

// validCodeChallenge reports whether c is the shape an S256 challenge has:
// 43 characters of unpadded base64url, which is what a 256-bit digest encodes
// to.
//
// Checked here rather than only at redemption so that a client with a broken
// PKCE implementation finds out before a human has typed a password into a form
// whose code can never be redeemed.
func validCodeChallenge(c string) bool {
	const s256ChallengeLength = 43

	if len(c) != s256ChallengeLength {
		return false
	}

	for i := range len(c) {
		switch ch := c[i]; {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
		case ch == '-', ch == '_':
		default:
			return false
		}
	}

	return true
}

// loginMessage renders what the form shows for a failed login.
//
// An error's own text is never shown. An authenticator that returned "no user
// with that email address" would otherwise turn this form into an account
// enumeration oracle, and the authenticator's author would have no way to know
// they had done it. Showing something specific is possible and is a decision:
// return a *LoginError, whose Message is the only string that reaches a
// browser.
func loginMessage(err error) string {
	var loginErr *LoginError
	if stderrors.As(err, &loginErr) && loginErr.Message != "" {
		return loginErr.Message
	}

	return DefaultLoginFailureMessage
}

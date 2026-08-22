package oauth2server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/authentication/oauth2server/memory"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test/must"
)

const (
	testIssuer      = "https://auth.example"
	testRedirectURI = "https://client.example/callback"
	testResource    = "https://api.example/"

	// testVerifier is a syntactically valid RFC 7636 code verifier: 43
	// characters of the unreserved set, which is the minimum length.
	testVerifier = "0123456789012345678901234567890123456789abc"

	testUsername = "chef"
	testPassword = "correct-horse"
)

// testSubject is what the harness's authenticator returns: an identifier plus
// the application-shaped claims this package must round-trip and never read.
func testSubject() oauth2server.Subject {
	return oauth2server.Subject{
		ID:     "user_1",
		Claims: map[string]string{"account_id": "acct_9"},
	}
}

// passwordAuthenticator is the harness's SubjectAuthenticator: a password check
// standing in for whatever an application actually does.
type passwordAuthenticator struct {
	// err, when set, is returned instead of a subject — for the cases about
	// what a failing identity store does to the flow.
	err error
}

func (a *passwordAuthenticator) AuthenticateSubject(_ context.Context, req *http.Request) (*oauth2server.Subject, error) {
	if a.err != nil {
		return nil, a.err
	}

	if req.PostFormValue(oauth2server.FieldUsername) != testUsername ||
		req.PostFormValue(oauth2server.FieldPassword) != testPassword {
		return nil, oauth2server.NewLoginError("Those details did not match.", nil)
	}

	subject := testSubject()

	return &subject, nil
}

// harness is one authorization server, its store, and an httptest server in
// front of it.
type harness struct {
	t             *testing.T
	store         oauth2server.Store
	server        *oauth2server.Server
	http          *httptest.Server
	authenticator *passwordAuthenticator
}

// newHarness builds a server over a memory store.
//
// The memory store rather than a SQLite one, deliberately: these tests are
// about the protocol, and the two stores are already held to one conformance
// suite that says they agree. Running the protocol cases against the fast one
// keeps them from also being a test of SQL.
func newHarness(t *testing.T, opts ...oauth2server.Option) *harness {
	t.Helper()

	h := &harness{t: t, authenticator: &passwordAuthenticator{}}

	return h.build(t, h.authenticator, opts)
}

// newHarnessWith builds a server over a SubjectAuthenticator of the caller's
// own, for the cases about what an authenticator's answers do to the flow.
func newHarnessWith(t *testing.T, authenticator oauth2server.SubjectAuthenticator, opts ...oauth2server.Option) *harness {
	t.Helper()

	return (&harness{t: t}).build(t, authenticator, opts)
}

// newStoreHarness builds a server over a store the caller supplies, for the
// cases about what a broken backend or a controllable clock does to the flow.
func newStoreHarness(t *testing.T, store oauth2server.Store, opts ...oauth2server.Option) *harness {
	t.Helper()

	h := &harness{t: t, authenticator: &passwordAuthenticator{}, store: store}

	return h.build(t, h.authenticator, opts)
}

// build wires a store, a server, and an httptest server together.
func (h *harness) build(t *testing.T, authenticator oauth2server.SubjectAuthenticator, opts []oauth2server.Option) *harness {
	t.Helper()

	if h.store == nil {
		h.store = memory.NewStore()
	}

	server, err := oauth2server.NewServer(testIssuer, h.store, authenticator, append([]oauth2server.Option{
		oauth2server.WithLogger(loggingnoop.NewLogger()),
		oauth2server.WithTracerProvider(tracingnoop.NewTracerProvider()),
	}, opts...)...)
	must.NoError(t, err)

	h.server = server
	h.http = httptest.NewServer(server.Handler())
	t.Cleanup(h.http.Close)

	return h
}

// formContentType is what every form-encoded request this harness sends
// carries.
const formContentType = "application/x-www-form-urlencoded"

// do sends one request with the test's context and hands back the response,
// with its body closed at cleanup.
//
// Every request goes through here rather than through the Client's Get/Post
// conveniences, which take no context: a test whose server hangs should fail
// when the test's deadline passes rather than when the package's does.
func (h *harness) do(method, path, contentType string, body io.Reader) *http.Response {
	h.t.Helper()

	req, err := http.NewRequestWithContext(h.t.Context(), method, h.http.URL+path, body)
	must.NoError(h.t, err)

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	res, err := h.http.Client().Do(req)
	must.NoError(h.t, err)
	h.t.Cleanup(func() { _ = res.Body.Close() })

	return res
}

// get sends a GET and hands back the response.
func (h *harness) get(path string) *http.Response {
	h.t.Helper()

	return h.do(http.MethodGet, path, "", nil)
}

// registration is what a dynamic registration handed back, kept so the flow
// cases can present the secret.
type registration struct {
	oauth2server.RegistrationResponse

	status int
}

// register runs a dynamic client registration.
func (h *harness) register(body map[string]any) *registration {
	h.t.Helper()

	encoded, err := json.Marshal(body)
	must.NoError(h.t, err)

	res := h.do(http.MethodPost, oauth2server.PathRegister, "application/json", strings.NewReader(string(encoded)))
	defer func() { must.NoError(h.t, res.Body.Close()) }()

	out := &registration{status: res.StatusCode}
	_ = json.NewDecoder(res.Body).Decode(&out.RegistrationResponse)

	return out
}

// registerConfidential registers a client that holds a secret, which is the
// ordinary case and the one where /token has something to verify.
func (h *harness) registerConfidential() *registration {
	h.t.Helper()

	out := h.register(map[string]any{
		"client_name":   "Conformance Client",
		"redirect_uris": []string{testRedirectURI},
	})

	must.EqOp(h.t, http.StatusCreated, out.status)
	must.NotEq(h.t, "", out.ClientID)
	must.NotEq(h.t, "", out.ClientSecret)

	return out
}

// authorizeParams builds a well-formed authorization request for a client.
func authorizeParams(clientID string) url.Values {
	return url.Values{
		"response_type":         {oauth2server.ResponseTypeCode},
		"client_id":             {clientID},
		"redirect_uri":          {testRedirectURI},
		"scope":                 {"read"},
		"state":                 {"opaque-state"},
		"code_challenge":        {oauth2server.S256Challenge(testVerifier)},
		"code_challenge_method": {oauth2server.CodeChallengeMethodS256},
		"resource":              {testResource},
	}
}

// noRedirectClient is an HTTP client that hands back the redirect rather than
// following it, which is the only way to read what /authorize actually sent.
func (h *harness) noRedirectClient() *http.Client {
	client := *h.http.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &client
}

// authorize posts a login to /authorize and returns the response.
func (h *harness) authorize(params, form url.Values) *http.Response {
	h.t.Helper()

	return h.authorizePost(params, form, nil)
}

// authorizePost posts to /authorize, decorating the request first so a test
// can present whatever a SubjectResolver reads alongside the form.
func (h *harness) authorizePost(params, form url.Values, decorate func(*http.Request)) *http.Response {
	h.t.Helper()

	target := h.http.URL + oauth2server.PathAuthorize + "?" + params.Encode()

	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodPost, target, strings.NewReader(form.Encode()))
	must.NoError(h.t, err)
	req.Header.Set("Content-Type", formContentType)

	if decorate != nil {
		decorate(req)
	}

	res, err := h.noRedirectClient().Do(req)
	must.NoError(h.t, err)
	h.t.Cleanup(func() { _ = res.Body.Close() })

	return res
}

// authorizeGet sends a GET to /authorize without following the redirect,
// decorating the request first so a test can present whatever a
// SubjectResolver reads.
//
// Separate from get, which follows redirects: a GET that a resolver answers
// with an authorization code is a redirect, and following it would send the
// code to the client's callback rather than showing it to the test.
func (h *harness) authorizeGet(params url.Values, decorate func(*http.Request)) *http.Response {
	h.t.Helper()

	target := h.http.URL + oauth2server.PathAuthorize + "?" + params.Encode()

	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodGet, target, http.NoBody)
	must.NoError(h.t, err)

	if decorate != nil {
		decorate(req)
	}

	res, err := h.noRedirectClient().Do(req)
	must.NoError(h.t, err)
	h.t.Cleanup(func() { _ = res.Body.Close() })

	return res
}

// login is the form a successful sign-in posts.
func login() url.Values {
	return url.Values{
		oauth2server.FieldUsername: {testUsername},
		oauth2server.FieldPassword: {testPassword},
	}
}

// codeFrom reads the authorization code out of a redirect response, failing if
// the response is not a redirect carrying one.
func (h *harness) codeFrom(res *http.Response) string {
	h.t.Helper()

	must.EqOp(h.t, http.StatusFound, res.StatusCode)

	location, err := url.Parse(res.Header.Get("Location"))
	must.NoError(h.t, err)

	code := location.Query().Get("code")
	must.NotEq(h.t, "", code)

	return code
}

// redirectError reads the OAuth error code out of a redirect response.
func (h *harness) redirectError(res *http.Response) (errorCode, state string) {
	h.t.Helper()

	must.EqOp(h.t, http.StatusFound, res.StatusCode)

	location, err := url.Parse(res.Header.Get("Location"))
	must.NoError(h.t, err)

	return location.Query().Get("error"), location.Query().Get("state")
}

// tokenResponse is a decoded /token answer, successful or not.
type tokenResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`

	oauth2server.TokenResponse

	status int
}

// token posts a token request, authenticating the client with its secret.
func (h *harness) token(clientID, secret string, form url.Values) *tokenResponse {
	h.t.Helper()

	if clientID != "" {
		form.Set("client_id", clientID)
	}
	if secret != "" {
		form.Set("client_secret", secret)
	}

	res := h.do(http.MethodPost, oauth2server.PathToken, formContentType, strings.NewReader(form.Encode()))

	out := &tokenResponse{status: res.StatusCode}
	_ = json.NewDecoder(res.Body).Decode(out)

	return out
}

// basicToken posts a token request authenticating the client with HTTP Basic
// rather than with form parameters.
func (h *harness) basicToken(clientID, secret string, form url.Values) *tokenResponse {
	h.t.Helper()

	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodPost,
		h.http.URL+oauth2server.PathToken, strings.NewReader(form.Encode()))
	must.NoError(h.t, err)

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, secret)

	res, err := h.http.Client().Do(req)
	must.NoError(h.t, err)
	h.t.Cleanup(func() { _ = res.Body.Close() })

	out := &tokenResponse{status: res.StatusCode}
	_ = json.NewDecoder(res.Body).Decode(out)

	return out
}

// redeem exchanges one authorization code, whatever the answer turns out to be.
func (h *harness) redeem(reg *registration, code string) *tokenResponse {
	h.t.Helper()

	return h.token(reg.ClientID, reg.ClientSecret, url.Values{
		"grant_type":    {oauth2server.GrantTypeAuthorizationCode},
		"code":          {code},
		"redirect_uri":  {testRedirectURI},
		"code_verifier": {testVerifier},
	})
}

// exchange runs the whole flow for a registered client and returns the token
// response: register, authorize, sign in, redeem.
func (h *harness) exchange(reg *registration) *tokenResponse {
	h.t.Helper()

	out := h.redeem(reg, h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login())))
	must.EqOp(h.t, http.StatusOK, out.status)

	return out
}

// readBody reads a response body as a string, for the two endpoints that answer
// in something other than JSON.
func readBody(t *testing.T, res *http.Response) string {
	t.Helper()

	body, err := io.ReadAll(res.Body)
	must.NoError(t, err)

	return string(body)
}

// revoke posts a revocation request.
func (h *harness) revoke(clientID, secret, token, hint string) int {
	h.t.Helper()

	form := url.Values{"token": {token}, "client_id": {clientID}, "client_secret": {secret}}
	if hint != "" {
		form.Set("token_type_hint", hint)
	}

	res := h.do(http.MethodPost, oauth2server.PathRevoke, formContentType, strings.NewReader(form.Encode()))

	return res.StatusCode
}

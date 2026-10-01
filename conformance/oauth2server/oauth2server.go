package oauth2server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"golang.org/x/oauth2"
)

// surface is this suite's name.
const surface = "oauth2server"

// registry is the surface the suite registers its clients through, and the key
// a subject's per-surface scope is read by for the person who authorizes them:
// the authorization server admits only a person in the registry that issued
// the client.
const registry = "oauth2clients"

// The calls this suite makes, as the names a caller is minted to make them by.
const (
	archiveOAuth2Client = oauth2clientspb.OAuth2ClientsService_ArchiveOAuth2Client_FullMethodName
	createOAuth2Client  = oauth2clientspb.OAuth2ClientsService_CreateOAuth2Client_FullMethodName
)

const (
	// redirect is the one redirect URI every client here registers, and sends
	// byte for byte at /authorize and at /token. It names a host that does not
	// resolve, so a redirect followed by mistake fails rather than lands.
	redirect = "https://example.test/callback"

	// clientName is the name every client here is registered with.
	clientName = "conformance authorization server client"

	// cleanupTimeout bounds archiving a client after its test.
	cleanupTimeout = 10 * time.Second

	// The protocol's own spellings, from RFC 6749 and RFC 7009.
	grantAuthorizationCode = "authorization_code"
	grantRefreshToken      = "refresh_token"
	errorInvalidClient     = "invalid_client"
	errorInvalidGrant      = "invalid_grant"
	errorInvalidRequest    = "invalid_request"
)

// Suite is the authorization server's storage promises, asserted over the
// routes a client reaches: that a code is redeemed once, that a replay ends
// what it issued, and that a revocation ends what it names and nothing else.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name: surface,

		// The HTTP surfaces are not in Surfaces; whether this one is served is
		// read off the probe caller's HTTP inside, and skipped with the reason.
		Mounted: func(conformance.Surfaces) bool { return true },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	probe := s.Subject(t)

	switch {
	case probe.HTTP == nil || !probe.HTTP.OAuth2Server:
		conformance.Skip(t, "conformance: this subject does not serve the authorization server")
	case s.Seams().AnonymousHTTP == nil:
		conformance.Skip(t, "conformance: this subject supplies no callerless HTTP client, which is how a client reaches /token and /revoke")
	}

	anonymous, err := s.Seams().AnonymousHTTP(t.Context())
	must.NoError(t, err, must.Sprint("opening an HTTP client carrying no caller"))
	must.NotNil(t, anonymous, must.Sprint("the subject returned no HTTP client and no error"))

	srv := &server{base: strings.TrimSuffix(probe.HTTP.BaseURL, "/"), client: anonymous}

	t.Run("discovery", func(t *testing.T) {
		t.Parallel()
		discovery(t, srv)
	})

	if probe.Surfaces.OAuth2Clients == nil {
		conformance.Skip(t, "conformance: this subject does not mount the oauth2clients registry, which is where the suite registers its clients")
	}

	t.Run("clients", func(t *testing.T) {
		t.Parallel()
		clients(t, s, srv)
	})

	if s.Seams().Actions.Authorized == nil {
		conformance.Skip(t, "conformance: this subject supplies no Actions.Authorized, so no person approves a request and the suite has no code to redeem")
	}

	t.Run("codes", func(t *testing.T) {
		t.Parallel()
		codes(t, s, srv)
	})
	t.Run("refresh", func(t *testing.T) {
		t.Parallel()
		refresh(t, s, srv)
	})
	t.Run("revocation", func(t *testing.T) {
		t.Parallel()
		revocation(t, s, srv)
	})
}

// server is where the authorization server is, and the client nobody is signed
// in on that a client application reaches it through.
type server struct {
	client *http.Client
	base   string
}

// registered is a client the registry issued, as the protocol names it — its
// client_id, not the registry row's identifier — with the secret the registry
// answered once.
type registered struct {
	clientID, secret string
}

// flow is one person and one client in that person's registry: everything an
// authorization needs.
type flow struct {
	s      *conformance.Session
	srv    *server
	person *conformance.Subject

	// registrar is who registered the client, in the person's registry, and
	// can register another there.
	registrar *conformance.Subject

	client registered
}

// newFlow mints a person, and a client registered in their registry by a
// caller declaring the registration — an operator where the subject reserves
// it.
func newFlow(t *testing.T, s *conformance.Session, srv *server) *flow {
	t.Helper()

	person := s.Subject(t)
	if person.UserID == "" {
		conformance.Skip(t, "conformance: the subject surfaces no user identifier, which is who Actions.Authorized approves the request as")
	}

	registrar := s.Subject(t,
		conformance.Making(createOAuth2Client, archiveOAuth2Client),
		conformance.InTenant(registry, person.ScopeFor(registry)))

	f := &flow{s: s, srv: srv, person: person, registrar: registrar}
	f.client = f.register(t)

	return f
}

// register registers a client in the person's registry, and archives it when
// the test ends so that a deployed database does not accrete them.
func (f *flow) register(t *testing.T) registered {
	t.Helper()

	created, err := f.registrar.Surfaces.OAuth2Clients.CreateOAuth2Client(f.registrar.Context(t.Context()),
		&oauth2clientspb.CreateOAuth2ClientRequest{Input: &oauth2clientspb.OAuth2ClientCreationInput{
			Name:         clientName,
			RedirectUris: []string{redirect},
		}})
	must.NoError(t, err, must.Sprint("registering a client"))

	issued := created.GetIssued()
	must.StrNotEqFold(t, "", issued.GetClient().GetId(), must.Sprint("the registry answered a client with no identifier"))
	must.StrNotEqFold(t, "", issued.GetClient().GetClientId(), must.Sprint("the registry answered a client with no client_id"))
	must.StrNotEqFold(t, "", issued.GetClientSecret(), must.Sprint("the registry answered a client with no secret"))

	id := issued.GetClient().GetId()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), cleanupTimeout)
		defer cancel()

		if _, archiveErr := f.registrar.Surfaces.OAuth2Clients.ArchiveOAuth2Client(f.registrar.Context(ctx),
			&oauth2clientspb.ArchiveOAuth2ClientRequest{Oauth2ClientId: id}); archiveErr != nil {
			t.Logf("conformance: archiving client %q after the test: %v", id, archiveErr)
		}
	})

	return registered{clientID: issued.GetClient().GetClientId(), secret: issued.GetClientSecret()}
}

// authorization is a code the server issued, and the verifier that redeems it.
type authorization struct {
	code, verifier string
}

// authorize has the person approve a request for the flow's client, and reads
// the code off the redirect.
//
// The state round-tripping and the issuer being named are asserted here, on
// every authorization, because they are the flow's own positive control: a
// redirect carrying the state this request sent is one this request caused.
func (f *flow) authorize(t *testing.T) authorization {
	t.Helper()

	verifier := oauth2.GenerateVerifier()
	state := identifiers.New()

	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {f.client.clientID},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {oauth2.S256ChallengeFromVerifier(verifier)},
		"code_challenge_method": {"S256"},
	}

	location, err := f.s.Seams().Actions.Authorized(t.Context(), f.person.ScopeFor(registry), f.person.UserID,
		f.srv.base+oauth2server.PathAuthorize+"?"+query.Encode())
	must.NoError(t, err, must.Sprint("the person approving the authorization request"))

	answered, err := url.Parse(location)
	must.NoError(t, err, must.Sprintf("the authorization server redirected to %q", location))

	must.StrHasPrefix(t, redirect, location,
		must.Sprintf("the authorization server redirected somewhere other than the client's redirect URI: %q", location))

	params := answered.Query()
	must.EqOp(t, "", params.Get("error"),
		must.Sprintf("the authorization server refused the request: %s (%s)", params.Get("error"), params.Get("error_description")))
	must.EqOp(t, state, params.Get("state"), must.Sprint("the redirect did not carry back the state the request sent"))
	test.StrNotEqFold(t, "", params.Get("iss"), test.Sprint("the redirect did not name its issuer"))

	code := params.Get("code")
	must.StrNotEqFold(t, "", code, must.Sprint("the redirect carried no code"))

	return authorization{code: code, verifier: verifier}
}

// exchange redeems a code at /token as the flow's client.
func (f *flow) exchange(t *testing.T, a authorization) *answer {
	t.Helper()

	return f.srv.post(t, oauth2server.PathToken, f.client, url.Values{
		"grant_type":    {grantAuthorizationCode},
		"code":          {a.code},
		"redirect_uri":  {redirect},
		"code_verifier": {a.verifier},
	})
}

// pair authorizes and exchanges once, requiring a token pair back.
func (f *flow) pair(t *testing.T) *tokens {
	t.Helper()

	return f.exchange(t, f.authorize(t)).issued(t, "exchanging a fresh code")
}

// refresh redeems a refresh token at /token as client.
func (f *flow) refresh(t *testing.T, client registered, refreshToken string) *answer {
	t.Helper()

	return f.srv.post(t, oauth2server.PathToken, client, url.Values{
		"grant_type":    {grantRefreshToken},
		"refresh_token": {refreshToken},
	})
}

// revoke asks /revoke to end a token as client.
func (f *flow) revoke(t *testing.T, client registered, token string) *answer {
	t.Helper()

	return f.srv.post(t, oauth2server.PathRevoke, client, url.Values{
		"token":           {token},
		"token_type_hint": {grantRefreshToken},
	})
}

// post makes one form-encoded request as client, authenticating it with
// client_secret_post, and reads what came back.
func (srv *server) post(t *testing.T, path string, client registered, form url.Values) *answer {
	t.Helper()

	form.Set("client_id", client.clientID)
	form.Set("client_secret", client.secret)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.base+path, strings.NewReader(form.Encode()))
	must.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return srv.do(t, req)
}

func (srv *server) do(t *testing.T, req *http.Request) *answer {
	t.Helper()

	res, err := srv.client.Do(req)
	must.NoError(t, err, must.Sprintf("%s %s", req.Method, req.URL.Path))

	defer func() { test.NoError(t, res.Body.Close()) }()

	body, err := io.ReadAll(res.Body)
	must.NoError(t, err)

	a := &answer{status: res.StatusCode, body: body}
	if len(body) > 0 && strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		must.NoError(t, json.Unmarshal(body, &a.fields), must.Sprintf("reading %s %s: %s", req.Method, req.URL.Path, body))
	}

	return a
}

// answer is what an endpoint said: the status, the body as sent, and the
// fields a token response or a protocol error carries.
type answer struct {
	fields struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	body   []byte
	status int
}

// tokens is a pair the token endpoint issued, reduced to the half the
// assertions present again. The access token is required of every pair, and
// whether it still works afterwards is a resource server's question — see the
// package documentation.
type tokens struct {
	refresh string
}

// issued requires a pair, failing with what the endpoint said instead.
func (a *answer) issued(t *testing.T, doing string) *tokens {
	t.Helper()

	must.EqOp(t, http.StatusOK, a.status, must.Sprintf("%s answered %d: %s", doing, a.status, a.body))
	must.StrNotEqFold(t, "", a.fields.AccessToken, must.Sprintf("%s issued no access token", doing))
	must.StrNotEqFold(t, "", a.fields.RefreshToken, must.Sprintf("%s issued no refresh token", doing))

	return &tokens{refresh: a.fields.RefreshToken}
}

// refused asserts a protocol error: the status, the error code RFC 6749 names
// for it, no token, and no store's wording in the body.
func (a *answer) refused(t *testing.T, status int, code, doing string) {
	t.Helper()

	test.EqOp(t, status, a.status, test.Sprintf("%s answered %d rather than %d: %s", doing, a.status, status, a.body))
	test.EqOp(t, code, a.fields.Error, test.Sprintf("%s answered error %q rather than %q", doing, a.fields.Error, code))
	test.EqOp(t, "", a.fields.AccessToken, test.Sprintf("%s issued an access token beside its refusal", doing))
	test.StrNotContains(t, string(a.body), "sql:", test.Sprintf("%s put a database error in front of a client", doing))
}

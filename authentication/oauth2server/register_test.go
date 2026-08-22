package oauth2server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/authentication/oauth2server/memory"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegister(T *testing.T) {
	T.Parallel()

	T.Run("a registration hands back a usable client exactly once", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		reg := h.register(map[string]any{
			"client_name":   "Recipe Importer",
			"redirect_uris": []string{testRedirectURI},
		})

		must.EqOp(t, http.StatusCreated, reg.status)
		test.NotEq(t, "", reg.ClientID)
		test.NotEq(t, "", reg.ClientSecret)
		test.EqOp(t, "Recipe Importer", reg.ClientName)
		test.Eq(t, []string{testRedirectURI}, reg.RedirectURIs)

		// RFC 7591's default is client_secret_basic. A registration that
		// omitted the field and got a public client would have silently opted
		// out of the credential it thought it had.
		test.EqOp(t, oauth2server.AuthMethodClientBasic, reg.TokenEndpointAuthMethod)

		test.Eq(t, []string{oauth2server.GrantTypeAuthorizationCode, oauth2server.GrantTypeRefreshToken},
			reg.GrantTypes)
		test.Eq(t, []string{oauth2server.ResponseTypeCode}, reg.ResponseTypes)

		test.Greater(t, int64(0), reg.ClientIDIssuedAt)

		// The registration lapses, so the table an anonymous caller writes to
		// is bounded without anybody having to decide which rows are garbage.
		test.Greater(t, reg.ClientIDIssuedAt, reg.ClientSecretExpiresAt)
	})

	T.Run("a registration with no expiry reports a secret that does not expire", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithClientRegistrationTTL(0))

		reg := h.register(map[string]any{"redirect_uris": []string{testRedirectURI}})
		must.EqOp(t, http.StatusCreated, reg.status)

		// Zero is RFC 7591's "does not expire". Reporting an expiry the
		// registration does not have would be a lie the client discovers as a
		// 401.
		test.EqOp(t, int64(0), reg.ClientSecretExpiresAt)
	})

	T.Run("an unsupported grant type is narrowed rather than refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		reg := h.register(map[string]any{
			"redirect_uris": []string{testRedirectURI},
			"grant_types":   []string{"implicit", oauth2server.GrantTypeAuthorizationCode},
		})

		must.EqOp(t, http.StatusCreated, reg.status)

		// RFC 7591 §2 permits this, and the response says what was granted — so
		// a client asking for the implicit grant is told it did not get one at
		// registration rather than at the first authorization request.
		test.Eq(t, []string{oauth2server.GrantTypeAuthorizationCode}, reg.GrantTypes)
	})

	T.Run("a registration naming nothing recognizable still gets the code grant", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		reg := h.register(map[string]any{
			"redirect_uris": []string{testRedirectURI},
			"grant_types":   []string{"implicit"},
		})

		// The authorization code grant is the only one that can start a flow,
		// so a registration without it would be unusable.
		must.EqOp(t, http.StatusCreated, reg.status)
		test.Eq(t, []string{oauth2server.GrantTypeAuthorizationCode}, reg.GrantTypes)
	})

	T.Run("scopes are narrowed to what this server issues", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithScopes("read", "write"))

		reg := h.register(map[string]any{
			"redirect_uris": []string{testRedirectURI},
			"scope":         "read admin",
		})

		must.EqOp(t, http.StatusCreated, reg.status)

		// Narrowing is right here and wrong at /authorize: a registration is a
		// client saying what it might ask for, and the response tells it what it
		// may. The refusal happens later, where a specific token is minted.
		test.EqOp(t, "read", reg.Scope)
	})

	T.Run("each registration gets its own identifier and secret", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		first := h.registerConfidential()
		second := h.registerConfidential()

		test.NotEq(t, first.ClientID, second.ClientID)
		test.NotEq(t, first.ClientSecret, second.ClientSecret)
	})
}

func TestRegister_Policy(T *testing.T) {
	T.Parallel()

	T.Run("a registration with no redirect_uri is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		// A client with no registered URI is the client for which "any
		// redirect_uri is accepted" was invented.
		reg := h.register(map[string]any{"client_name": "Nowhere"})
		test.EqOp(t, http.StatusBadRequest, reg.status)
	})

	T.Run("an insecure or unmatchable redirect_uri is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		for _, uri := range []string{
			"http://client.example/cb",       // plaintext to the network
			"/callback",                      // relative: nothing to match exactly
			"https://client.example/cb#frag", // a fragment cannot survive the redirect
			"myapp:/callback",                // a private-use scheme nobody controls
		} {
			reg := h.register(map[string]any{"redirect_uris": []string{uri}})
			test.EqOp(t, http.StatusBadRequest, reg.status, test.Sprintf("redirect_uri %q", uri))
		}
	})

	T.Run("loopback http and reverse-DNS schemes are accepted", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		// The two shapes OAuth 2.1 §8.4 recognizes for a client that cannot be
		// confidential: a native client's ephemeral listener, and a mobile
		// client the operating system routes by scheme.
		for _, uri := range []string{
			"http://127.0.0.1:52341/cb",
			"http://localhost:8080/cb",
			"com.example.recipes:/oauth",
		} {
			reg := h.register(map[string]any{"redirect_uris": []string{uri}})
			test.EqOp(t, http.StatusCreated, reg.status, test.Sprintf("redirect_uri %q", uri))
		}
	})

	T.Run("an unsupported token_endpoint_auth_method is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		reg := h.register(map[string]any{
			"redirect_uris":              []string{testRedirectURI},
			"token_endpoint_auth_method": "private_key_jwt",
		})

		test.EqOp(t, http.StatusBadRequest, reg.status)
	})

	T.Run("a body over the ceiling is refused rather than read", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		// /register is the one endpoint an anonymous caller can write rows
		// through, so the body it sends is read with a ceiling rather than to
		// EOF.
		huge := strings.Repeat("a", oauth2server.MaxRegistrationBodyBytes+1)

		res := h.do(http.MethodPost, oauth2server.PathRegister,
			"application/json", strings.NewReader(`{"client_name":"`+huge+`"}`))

		test.EqOp(t, http.StatusBadRequest, res.StatusCode)
	})

	T.Run("a malformed body is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res := h.do(http.MethodPost, oauth2server.PathRegister,
			"application/json", strings.NewReader(`{not json`))

		test.EqOp(t, http.StatusBadRequest, res.StatusCode)
	})

	T.Run("a replacement policy decides instead", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithRegistrationPolicy(
			oauth2server.RegistrationPolicyFunc(func(_ context.Context, req *oauth2server.RegistrationRequest) error {
				if req.ClientName != "approved" {
					return platformerrors.Wrap(oauth2server.ErrRegistrationRejected, "not on the allowlist")
				}

				return nil
			})))

		refused := h.register(map[string]any{
			"client_name": "anybody", "redirect_uris": []string{testRedirectURI},
		})
		test.EqOp(t, http.StatusBadRequest, refused.status)

		// The replacement decides everything, the shipped rules included — which
		// is why DefaultRegistrationPolicy's own checks are exported as
		// ValidateRedirectURI, for a policy that wants to add to them rather
		// than start over.
		accepted := h.register(map[string]any{
			"client_name": "approved", "redirect_uris": []string{"not-even-a-uri"},
		})
		test.EqOp(t, http.StatusCreated, accepted.status)
	})

	T.Run("a policy failure that is not a rejection is a server error", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithRegistrationPolicy(
			oauth2server.RegistrationPolicyFunc(func(context.Context, *oauth2server.RegistrationRequest) error {
				return platformerrors.New("the allowlist service is down")
			})))

		// A policy that could not decide has not decided, and answering 400
		// would tell the client its registration was wrong when it was not.
		reg := h.register(map[string]any{"redirect_uris": []string{testRedirectURI}})
		test.EqOp(t, http.StatusInternalServerError, reg.status)
	})
}

// A deployment whose clients are administered elsewhere turns this endpoint
// off, and what it turns off has to be the endpoint rather than one route to
// it — the discovery document has already stopped naming it.
func TestRegister_NotServed(T *testing.T) {
	T.Parallel()

	T.Run("the endpoint is not routed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithDynamicRegistration(false))

		out := h.register(map[string]any{"redirect_uris": []string{testRedirectURI}})
		test.EqOp(t, http.StatusNotFound, out.status)
	})

	T.Run("the handler refuses even where a deployment mounted it by hand", func(t *testing.T) {
		t.Parallel()

		server, err := oauth2server.NewServer(testIssuer, memory.NewStore(), &passwordAuthenticator{},
			oauth2server.WithDynamicRegistration(false))
		must.NoError(t, err)

		mounted := httptest.NewServer(server.RegisterHandler())
		t.Cleanup(mounted.Close)

		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, mounted.URL,
			strings.NewReader(`{"redirect_uris":["`+testRedirectURI+`"]}`))
		must.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")

		res, err := mounted.Client().Do(req)
		must.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })

		// Mount's own doc suggests mounting the handlers individually to rate
		// limit this one, so a switch that only reached the router would leave
		// exactly that deployment serving what its document says it does not
		// have.
		test.EqOp(t, http.StatusNotFound, res.StatusCode)
		test.StrContains(t, readBody(t, res), oauth2server.ErrorCodeInvalidRequest)
	})

	T.Run("clients already in the store still sign people in", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithDynamicRegistration(false))

		// The case the switch is for: registrations minted by whatever
		// administers them. Turning the endpoint off stops new ones being
		// written; it does not un-register the ones that were.
		reg := &registration{}
		reg.ClientID, reg.ClientSecret = "seeded-client", "seeded-secret"

		must.NoError(t, h.store.CreateClient(t.Context(), &oauth2server.Client{
			CreatedAt:               time.Now().UTC(),
			ID:                      reg.ClientID,
			SecretHash:              oauth2server.Hash(reg.ClientSecret),
			RedirectURIs:            []string{testRedirectURI},
			TokenEndpointAuthMethod: oauth2server.AuthMethodClientBasic,
		}))

		tokens := h.exchange(reg)
		test.NotEq(t, "", tokens.AccessToken)
	})
}

func TestValidateRedirectURI(T *testing.T) {
	T.Parallel()

	T.Run("names why it refused", func(t *testing.T) {
		t.Parallel()

		for uri, want := range map[string]error{
			"":                                 oauth2server.ErrRedirectURINotAbsolute,
			"/relative":                        oauth2server.ErrRedirectURINotAbsolute,
			"https://client.example/cb#frag":   oauth2server.ErrRedirectURIHasFragment,
			"http://client.example/cb":         oauth2server.ErrRedirectURIInsecure,
			"ftp://client.example/cb":          oauth2server.ErrRedirectURIInsecure,
			strings.Repeat("https://a/", 1000): oauth2server.ErrRedirectURITooLong,
		} {
			err := oauth2server.ValidateRedirectURI(uri)
			test.ErrorIs(t, err, want, test.Sprintf("redirect_uri %q", uri))

			// Every refusal is a rejection, so a policy adding its own rules can
			// return these and a caller reading one can check the general case.
			test.ErrorIs(t, err, oauth2server.ErrRegistrationRejected, test.Sprintf("redirect_uri %q", uri))
		}
	})

	T.Run("accepts what the flow needs", func(t *testing.T) {
		t.Parallel()

		for _, uri := range []string{
			"https://client.example/callback",
			"https://client.example/callback?tenant=acme",
			"http://127.0.0.1:1234/cb",
			"http://[::1]:1234/cb",
			"com.example.app:/callback",
		} {
			test.NoError(t, oauth2server.ValidateRedirectURI(uri), test.Sprintf("redirect_uri %q", uri))
		}
	})
}

// A registration that lapsed is not a registration, at either endpoint that
// reads one.
func TestRegistration_Expiry(T *testing.T) {
	T.Parallel()

	T.Run("a lapsed registration cannot start or finish a flow", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		reg := h.registerConfidential()

		// Aged out by hand rather than by waiting ninety days.
		must.NoError(t, h.store.DeleteClient(t.Context(), reg.ClientID))
		must.NoError(t, h.store.CreateClient(t.Context(), &oauth2server.Client{
			CreatedAt:               time.Now().UTC().Add(-2 * time.Hour),
			ExpiresAt:               time.Now().UTC().Add(-time.Hour),
			ID:                      reg.ClientID,
			SecretHash:              oauth2server.Hash(reg.ClientSecret),
			RedirectURIs:            []string{testRedirectURI},
			TokenEndpointAuthMethod: oauth2server.AuthMethodClientBasic,
		}))

		res := h.authorize(authorizeParams(reg.ClientID), login())
		test.EqOp(t, http.StatusBadRequest, res.StatusCode)

		out := h.token(reg.ClientID, reg.ClientSecret, map[string][]string{
			"grant_type": {oauth2server.GrantTypeRefreshToken}, "refresh_token": {"x"},
		})
		test.EqOp(t, http.StatusUnauthorized, out.status)
	})
}

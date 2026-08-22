package oauth2server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/authentication/oauth2server/memory"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("a nil option is ignored rather than dereferenced", func(t *testing.T) {
		t.Parallel()

		srv, err := oauth2server.NewServer(testIssuer, memory.NewStore(),
			&passwordAuthenticator{}, nil, oauth2server.WithScopes("read"))
		must.NoError(t, err)
		test.Eq(t, []string{"read"}, srv.Metadata().ScopesSupported)
	})

	T.Run("a non-positive lifetime leaves the default in place", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t,
			oauth2server.WithAccessTokenTTL(0),
			oauth2server.WithAuthorizationCodeTTL(-time.Minute),
			oauth2server.WithRefreshTokenTTL(-time.Hour))

		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		// A zero TTL is not a request for a token that has already expired; it
		// is an unset field, and the default is what an unset field means.
		test.EqOp(t, int64(oauth2server.DefaultAccessTokenTTL.Seconds()), tokens.ExpiresIn)
	})

	T.Run("a configured access token lifetime reaches the response", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithAccessTokenTTL(90*time.Second))

		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		test.EqOp(t, int64(90), tokens.ExpiresIn)
	})

	T.Run("a nil renderer and a nil policy leave the shipped ones in place", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t,
			oauth2server.WithLoginRenderer(nil),
			oauth2server.WithRegistrationPolicy(nil))

		// Both still working means the nil did not replace anything: the
		// registration passes the shipped policy, and the form renders.
		reg := h.registerConfidential()

		res := h.get(oauth2server.PathAuthorize + "?" + authorizeParams(reg.ClientID).Encode())
		test.EqOp(t, http.StatusOK, res.StatusCode)
	})

	T.Run("a replacement renderer owns the whole response", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithLoginRenderer(
			oauth2server.LoginRendererFunc(func(_ context.Context, res http.ResponseWriter, view oauth2server.LoginView) {
				// The status included — which a renderer has to be able to
				// choose, or a rate-limited login could not answer 429.
				res.WriteHeader(http.StatusTeapot)
				_, _ = res.Write([]byte(view.ClientName))
			})))

		reg := h.registerConfidential()

		res := h.get(oauth2server.PathAuthorize + "?" + authorizeParams(reg.ClientID).Encode())
		test.EqOp(t, http.StatusTeapot, res.StatusCode)
	})

	T.Run("a registration with no name renders its identifier on the form", func(t *testing.T) {
		t.Parallel()

		var seen oauth2server.LoginView

		h := newHarness(t, oauth2server.WithLoginRenderer(
			oauth2server.LoginRendererFunc(func(_ context.Context, res http.ResponseWriter, view oauth2server.LoginView) {
				seen = view
				res.WriteHeader(http.StatusOK)
			})))

		reg := h.register(map[string]any{"redirect_uris": []string{testRedirectURI}})
		must.EqOp(t, http.StatusCreated, reg.status)

		h.get(oauth2server.PathAuthorize + "?" + authorizeParams(reg.ClientID).Encode())

		// The human is being asked to approve something, and "" is not
		// something.
		test.EqOp(t, reg.ClientID, seen.ClientName)
		test.Eq(t, []string{"read"}, seen.Scopes)

		// The original query, so the POST is validated against the same
		// parameters the GET was rather than against hidden form fields.
		test.StrContains(t, seen.Action, "code_challenge=")
	})
}

// The shipped form is what a deployment gets before it writes anything, so what
// it does with an attacker-supplied client name matters.
func TestDefaultLoginRenderer(T *testing.T) {
	T.Parallel()

	T.Run("escapes the client name", func(t *testing.T) {
		t.Parallel()

		res := httptest.NewRecorder()

		oauth2server.DefaultLoginRenderer.RenderLogin(t.Context(), res, oauth2server.LoginView{
			Action:     "/authorize?client_id=x",
			ClientName: `<script>alert(1)</script>`,
			Scopes:     []string{"read"},
		})

		body := res.Body.String()

		// html/template does this by construction. A renderer that built HTML
		// by concatenation would be choosing to be an XSS, on a page that is
		// about to receive a password.
		test.StrNotContains(t, body, "<script>alert(1)</script>")
		test.StrContains(t, body, "&lt;script&gt;")
		test.EqOp(t, http.StatusOK, res.Code)
	})

	T.Run("answers 401 when there is a message to show", func(t *testing.T) {
		t.Parallel()

		res := httptest.NewRecorder()

		oauth2server.DefaultLoginRenderer.RenderLogin(t.Context(), res, oauth2server.LoginView{
			Error: "Those details did not match.",
		})

		// So that a client driving this without a browser can tell the two
		// renders apart.
		test.EqOp(t, http.StatusUnauthorized, res.Code)
		test.StrContains(t, res.Body.String(), "Those details did not match.")
		test.EqOp(t, "no-store", res.Header().Get("Cache-Control"))
	})
}

func TestLoginError(T *testing.T) {
	T.Parallel()

	T.Run("wraps ErrLoginFailed and whatever it was reacting to", func(t *testing.T) {
		t.Parallel()

		cause := platformerrors.New("the second factor is stale")
		err := oauth2server.NewLoginError("That code has expired.", cause)

		// Wrapping ErrLoginFailed is what gets the re-render behavior without
		// an authenticator also having to say so.
		test.ErrorIs(t, err, oauth2server.ErrLoginFailed)
		test.ErrorIs(t, err, cause)
	})

	T.Run("with no cause still reads as a login failure", func(t *testing.T) {
		t.Parallel()

		err := oauth2server.NewLoginError("Try again.", nil)
		test.ErrorIs(t, err, oauth2server.ErrLoginFailed)
		test.StrContains(t, err.Error(), "Try again.")
	})
}

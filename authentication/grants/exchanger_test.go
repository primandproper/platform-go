package grants_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/grants"
	grantsmock "github.com/primandproper/platform-go/v14/authentication/grants/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"golang.org/x/oauth2"
)

// tokenEndpoint is a provider's token endpoint that answers every refresh with
// status and body, and counts what it was sent.
func tokenEndpoint(t *testing.T, status int, body map[string]any) (*oauth2.Config, *atomic.Value) {
	t.Helper()

	sent := &atomic.Value{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		sent.Store(r.PostForm.Get("refresh_token"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)

	return &oauth2.Config{
		ClientID:     "client",
		ClientSecret: "secret",
		Endpoint:     oauth2.Endpoint{TokenURL: server.URL, AuthStyle: oauth2.AuthStyleInParams},
	}, sent
}

func TestRefresh(T *testing.T) {
	T.Parallel()

	T.Run("an oauth2.Config is an Exchanger, and a refresh comes back as Tokens", func(t *testing.T) {
		t.Parallel()

		config, sent := tokenEndpoint(t, http.StatusOK, map[string]any{
			"access_token":  "fresh-access",
			"refresh_token": "rotated-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})

		tokens, err := grants.Refresh(t.Context(), config, &grants.Grant{AccessToken: "stale", RefreshToken: "held-refresh"})
		must.NoError(t, err)

		test.EqOp(t, "held-refresh", sent.Load())
		test.EqOp(t, "fresh-access", tokens.AccessToken)
		test.EqOp(t, "rotated-refresh", tokens.RefreshToken)
		test.False(t, tokens.Expiry.IsZero())
	})

	T.Run("a provider that does not rotate leaves the refresh token standing", func(t *testing.T) {
		t.Parallel()

		config, _ := tokenEndpoint(t, http.StatusOK, map[string]any{
			"access_token": "fresh-access",
			"token_type":   "Bearer",
		})

		tokens, err := grants.Refresh(t.Context(), config, &grants.Grant{AccessToken: "stale", RefreshToken: "held-refresh"})
		must.NoError(t, err)

		// x/oauth2 carries the refresh token it sent onto a response that names
		// none; either way, Store.Refreshed keeps the stored one when this is
		// empty, so the answer is the same.
		test.EqOp(t, "fresh-access", tokens.AccessToken)
		test.EqOp(t, "held-refresh", tokens.RefreshToken)
	})

	T.Run("invalid_grant is the provider having revoked", func(t *testing.T) {
		t.Parallel()

		config, _ := tokenEndpoint(t, http.StatusBadRequest, map[string]any{
			"error":             "invalid_grant",
			"error_description": "Token has been expired or revoked.",
		})

		_, err := grants.Refresh(t.Context(), config, &grants.Grant{RefreshToken: "held-refresh"})
		test.ErrorIs(t, err, grants.ErrProviderRevoked)

		// The provider's own error is still there for a caller that wants its
		// description.
		retrieveErr, ok := errors.AsType[*oauth2.RetrieveError](err)
		must.True(t, ok)
		test.EqOp(t, "Token has been expired or revoked.", retrieveErr.ErrorDescription)
	})

	T.Run("any other refusal is not a revocation", func(t *testing.T) {
		t.Parallel()

		config, _ := tokenEndpoint(t, http.StatusBadRequest, map[string]any{"error": "invalid_client"})

		_, err := grants.Refresh(t.Context(), config, &grants.Grant{RefreshToken: "held-refresh"})
		must.Error(t, err)
		test.False(t, errors.Is(err, grants.ErrProviderRevoked))
	})

	T.Run("a response with no access token is refused", func(t *testing.T) {
		t.Parallel()

		exchanger := &grantsmock.ExchangerMock{
			TokenSourceFunc: func(context.Context, *oauth2.Token) oauth2.TokenSource {
				return oauth2.StaticTokenSource(&oauth2.Token{RefreshToken: "r"})
			},
		}

		_, err := grants.Refresh(t.Context(), exchanger, &grants.Grant{RefreshToken: "held-refresh"})
		test.ErrorIs(t, err, grants.ErrEmptyAccessToken)
	})

	T.Run("a grant with no refresh token makes no call", func(t *testing.T) {
		t.Parallel()

		exchanger := &grantsmock.ExchangerMock{}

		_, err := grants.Refresh(t.Context(), exchanger, &grants.Grant{AccessToken: "a"})
		test.ErrorIs(t, err, grants.ErrNoRefreshToken)
		test.SliceEmpty(t, exchanger.TokenSourceCalls())
	})

	T.Run("the source is handed nothing it could reuse", func(t *testing.T) {
		t.Parallel()

		var handed *oauth2.Token

		exchanger := &grantsmock.ExchangerMock{
			TokenSourceFunc: func(_ context.Context, t *oauth2.Token) oauth2.TokenSource {
				handed = t

				return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fresh"})
			},
		}

		_, err := grants.Refresh(t.Context(), exchanger, &grants.Grant{AccessToken: "still-valid", RefreshToken: "r"})
		must.NoError(t, err)
		must.NotNil(t, handed)
		test.EqOp(t, "", handed.AccessToken)
		test.EqOp(t, "r", handed.RefreshToken)
	})

	T.Run("nil arguments are refused", func(t *testing.T) {
		t.Parallel()

		_, err := grants.Refresh(t.Context(), nil, &grants.Grant{RefreshToken: "r"})
		test.ErrorIs(t, err, grants.ErrNilExchanger)

		_, err = grants.Refresh(t.Context(), &grantsmock.ExchangerMock{}, nil)
		test.ErrorIs(t, err, grants.ErrNilGrant)
	})
}

package oauth2server_test

import (
	"context"
	stderrors "errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/authentication/oauth2server/memory"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recordlessStore answers every code redemption as a replay with no record.
//
// It is not a hypothetical: a store whose row expired between the guarded
// UPDATE and the SELECT that explains it has nothing to hand back, and the
// caller still has to answer the request rather than dereference the nil.
type recordlessStore struct {
	oauth2server.Store
}

func (s *recordlessStore) ConsumeAuthorizationCode(context.Context, string) (*oauth2server.AuthorizationCode, error) {
	return nil, oauth2server.ErrAlreadyRedeemed
}

// familylessStore answers every rotation as a replay of a token naming no
// family, which is what a record written by hand rather than by this server
// looks like.
type familylessStore struct {
	oauth2server.Store
}

func (s *familylessStore) ConsumeRefreshToken(context.Context, string) (*oauth2server.RefreshToken, error) {
	return &oauth2server.RefreshToken{}, oauth2server.ErrAlreadyRedeemed
}

// familylessCodeStore strips the family off every code it hands back, which is
// what a record written by hand — or by a schema predating the column — looks
// like from the token endpoint.
type familylessCodeStore struct {
	oauth2server.Store
}

func (s *familylessCodeStore) ConsumeAuthorizationCode(
	ctx context.Context,
	hash string,
) (*oauth2server.AuthorizationCode, error) {
	code, err := s.Store.ConsumeAuthorizationCode(ctx, hash)
	if code != nil {
		code.FamilyID = ""
	}

	return code, err
}

func TestServer_ReplayWithoutARecord(T *testing.T) {
	T.Parallel()

	T.Run("a replayed code with no record is still refused", func(t *testing.T) {
		t.Parallel()

		h := newStoreHarness(t, &recordlessStore{Store: memory.NewStore()})
		reg := h.registerConfidential()

		out := h.redeem(reg, "whatever")

		// The record is what a replay would be recorded against, and there is
		// none. Answering anything but invalid_grant — or panicking on the way
		// to the counter — would make a lost row into a different bug.
		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, out.Error)
	})

	T.Run("a replayed refresh token naming no family revokes nothing", func(t *testing.T) {
		t.Parallel()

		h := newStoreHarness(t, &familylessStore{Store: memory.NewStore()})
		reg := h.registerConfidential()

		out := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {"whatever"},
		})

		// An empty family is not a family, and revoking by it would be a
		// predicate matching every token that also names none.
		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, out.Error)
	})

	T.Run("a code naming no family still mints a pair into one", func(t *testing.T) {
		t.Parallel()

		h := newStoreHarness(t, &familylessCodeStore{Store: memory.NewStore()})
		reg := h.registerConfidential()

		tokens := h.exchange(reg)

		// Issuing the pair under the empty family would put every such token
		// in one group, which RevokeFamily then refuses to touch — so the
		// grant mints one rather than carrying the absence forward.
		access, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		must.NoError(t, err)
		must.NotNil(t, access)
		test.NotEq(t, "", access.FamilyID)
	})

	T.Run("a replayed code naming no family revokes nothing", func(t *testing.T) {
		t.Parallel()

		h := newStoreHarness(t, &familylessCodeStore{Store: memory.NewStore()})
		reg := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		tokens := h.redeem(reg, code)
		must.EqOp(t, http.StatusOK, tokens.status)

		// Refused, and nothing is revoked by an empty identifier — the family
		// the pair was actually minted under is not one this code can name.
		out := h.redeem(reg, code)
		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, out.Error)

		access, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		must.NoError(t, err)
		test.NotNil(t, access)
	})
}

func TestNewServer_Issuer(T *testing.T) {
	T.Parallel()

	T.Run("an issuer that does not parse is refused", func(t *testing.T) {
		t.Parallel()

		// Refused at construction rather than discovered later: every metadata
		// document and every "iss" this server sends is derived from it.
		server, err := oauth2server.NewServer("https://[::1", memory.NewStore(), &passwordAuthenticator{})
		test.Nil(t, server)
		test.ErrorIs(t, err, oauth2server.ErrInvalidIssuer)
	})

	T.Run("a scheme other than http or https is refused", func(t *testing.T) {
		t.Parallel()

		// The issuer is concatenated with the endpoint paths and handed to
		// clients as URLs to fetch. A scheme no browser will follow makes a
		// discovery document nobody can use.
		server, err := oauth2server.NewServer("ftp://auth.example", memory.NewStore(), &passwordAuthenticator{})
		test.Nil(t, server)
		test.ErrorIs(t, err, oauth2server.ErrInvalidIssuer)
	})
}

func TestOptions_RefreshTokenTTL(T *testing.T) {
	T.Parallel()

	T.Run("a configured refresh lifetime is what expires the token", func(t *testing.T) {
		t.Parallel()

		h, clk := newTimedHarness(t, oauth2server.WithRefreshTokenTTL(2*time.Hour))

		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		clk.advance(2*time.Hour + time.Second)

		out := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})

		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, out.Error)
	})
}

func TestLoginError_Message(T *testing.T) {
	T.Parallel()

	T.Run("names what it was reacting to", func(t *testing.T) {
		t.Parallel()

		// The cause is for the operator; LoginError.Message is the only string
		// that reaches a browser. Both are in the error, and only one is on the
		// page.
		err := oauth2server.NewLoginError("Those details did not match.",
			platformerrors.New("no user with that email address"))

		test.StrContains(t, err.Error(), "no user with that email address")
		test.StrNotContains(t, err.Error(), "Those details did not match.")
	})

	T.Run("a refused login shows the message and not the cause", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWith(t, &enumeratingAuthenticator{})
		reg := h.registerConfidential()

		res := h.authorize(authorizeParams(reg.ClientID), login())
		must.EqOp(t, http.StatusUnauthorized, res.StatusCode)

		body := readBody(t, res)
		test.StrContains(t, body, "Those details did not match.")
		test.StrNotContains(t, body, "no user with that email address")
	})
}

// enumeratingAuthenticator refuses every login with a message safe to show and a
// cause that is not.
type enumeratingAuthenticator struct{}

func (a *enumeratingAuthenticator) AuthenticateSubject(context.Context, *http.Request) (*oauth2server.Subject, error) {
	return nil, oauth2server.NewLoginError("Those details did not match.",
		platformerrors.New("no user with that email address"))
}

// The recorded error keeps what the response could not.
func TestServer_RecordedCause(T *testing.T) {
	T.Parallel()

	T.Run("a store failure survives into the operation record", func(t *testing.T) {
		t.Parallel()

		faults := newFaultStore()
		h, logger, _ := newObservedHarness(t, faults)

		reg := h.registerConfidential()
		faults.breaks(methodGetClient, errStoreDown)

		must.EqOp(t, http.StatusInternalServerError,
			h.get(oauth2server.PathAuthorize+"?"+authorizeParams(reg.ClientID).Encode()).StatusCode)

		recorded := logger.at(logging.ErrorLevel)
		must.SliceNotEmpty(t, recorded)

		// What went on the wire was "server_error" and nothing else. What was
		// recorded unwraps to the actual failure, which is the whole reason the
		// description and the cause are separate fields.
		test.True(t, stderrors.Is(recorded[0].err, errStoreDown))
	})
}

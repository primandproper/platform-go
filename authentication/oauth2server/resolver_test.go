package oauth2server_test

import (
	"context"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const (
	// testSessionHeader and testSessionToken stand in for whatever a
	// deployment's already-authenticated clients actually present: a session
	// cookie, a first-party bearer token, a header a gateway sets.
	testSessionHeader = "X-Session"
	testSessionToken  = "session-of-user-1"
)

// countingAuthenticator records whether the form seam was reached at all,
// which is the assertion several of these cases turn on: a request the
// resolver answered must not have been offered to it.
type countingAuthenticator struct {
	calls atomic.Int64
}

func (a *countingAuthenticator) AuthenticateSubject(ctx context.Context, req *http.Request) (*oauth2server.Subject, error) {
	a.calls.Add(1)

	return (&passwordAuthenticator{}).AuthenticateSubject(ctx, req)
}

// sessionResolver resolves the subject for a request carrying the test session
// header, and declines every other request.
func sessionResolver(calls *atomic.Int64) oauth2server.SubjectResolver {
	return oauth2server.SubjectResolverFunc(func(_ context.Context, req *http.Request) (*oauth2server.Subject, error) {
		if calls != nil {
			calls.Add(1)
		}

		if req.Header.Get(testSessionHeader) != testSessionToken {
			// Not one of mine: no credential, or one this resolver does not
			// recognize. The form is still the right answer.
			return nil, nil
		}

		subject := testSubject()

		return &subject, nil
	})
}

// withSession decorates a request with the credential the test resolver reads.
func withSession(req *http.Request) {
	req.Header.Set(testSessionHeader, testSessionToken)
}

func TestSubjectResolver(T *testing.T) {
	T.Parallel()

	T.Run("a GET carrying proof redirects with a code rather than a form", func(t *testing.T) {
		t.Parallel()

		auth := &countingAuthenticator{}
		h := newHarnessWith(t, auth, oauth2server.WithSubjectResolver(sessionResolver(nil)))

		reg := h.registerConfidential()

		res := h.authorizeGet(authorizeParams(reg.ClientID), withSession)

		// A GET, no body, and an authorization code — which is the whole
		// point: a client with nothing to type is not made to POST to say so.
		code := h.codeFrom(res)

		tokens := h.redeem(reg, code)
		must.EqOp(t, http.StatusOK, tokens.status)
		test.EqOp(t, int64(0), auth.calls.Load())
	})

	T.Run("the resolved subject is the one the token is minted for", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithSubjectResolver(sessionResolver(nil)))

		reg := h.registerConfidential()
		tokens := h.redeem(reg, h.codeFrom(h.authorizeGet(authorizeParams(reg.ClientID), withSession)))
		must.EqOp(t, http.StatusOK, tokens.status)

		access, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		must.NoError(t, err)

		// The claims round-trip untouched, exactly as they do from the form
		// path: this seam changes who answers, not what an answer means.
		test.EqOp(t, testSubject().ID, access.Subject.ID)
		test.Eq(t, testSubject().Claims, access.Subject.Claims)
	})

	T.Run("a GET carrying nothing still gets the form", func(t *testing.T) {
		t.Parallel()

		var resolverCalls atomic.Int64

		h := newHarness(t, oauth2server.WithSubjectResolver(sessionResolver(&resolverCalls)))

		reg := h.registerConfidential()

		res := h.authorizeGet(authorizeParams(reg.ClientID), nil)
		must.EqOp(t, http.StatusOK, res.StatusCode)
		test.StrContains(t, readBody(t, res), oauth2server.FieldPassword)
		test.EqOp(t, int64(1), resolverCalls.Load())
	})

	T.Run("a resolver that answers wins over the form on POST", func(t *testing.T) {
		t.Parallel()

		auth := &countingAuthenticator{}
		h := newHarnessWith(t, auth, oauth2server.WithSubjectResolver(sessionResolver(nil)))

		reg := h.registerConfidential()

		// The wrong password, deliberately. Proof already held is what decides
		// this request, and if the authenticator were consulted first — or at
		// all — this would re-render the form instead.
		res := h.authorizePost(authorizeParams(reg.ClientID), url.Values{
			oauth2server.FieldUsername: {testUsername},
			oauth2server.FieldPassword: {"not-the-password"},
		}, withSession)

		must.NotEq(t, "", h.codeFrom(res))
		test.EqOp(t, int64(0), auth.calls.Load())
	})

	T.Run("a resolver that declines leaves the POST to the authenticator", func(t *testing.T) {
		t.Parallel()

		auth := &countingAuthenticator{}
		h := newHarnessWith(t, auth, oauth2server.WithSubjectResolver(sessionResolver(nil)))

		reg := h.registerConfidential()

		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))
		must.NotEq(t, "", code)
		test.EqOp(t, int64(1), auth.calls.Load())
	})

	T.Run("a resolver error ends the attempt at the redirect URI", func(t *testing.T) {
		t.Parallel()

		auth := &countingAuthenticator{}
		h := newHarnessWith(t, auth, oauth2server.WithSubjectResolver(
			oauth2server.SubjectResolverFunc(func(context.Context, *http.Request) (*oauth2server.Subject, error) {
				return nil, platformerrors.New("the session store is down")
			})))

		reg := h.registerConfidential()

		res := h.authorizeGet(authorizeParams(reg.ClientID), withSession)

		// Not a form. The caller presented a credential and had it refused, so
		// a login page is an answer it has no way to use.
		errorCode, state := h.redirectError(res)
		test.EqOp(t, oauth2server.ErrorCodeServerError, errorCode)
		test.EqOp(t, "opaque-state", state)

		location, err := url.Parse(res.Header.Get("Location"))
		must.NoError(t, err)
		test.EqOp(t, "", location.Query().Get("code"))
		test.EqOp(t, int64(0), auth.calls.Load())
	})

	T.Run("a resolved subject with no identifier is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithSubjectResolver(
			oauth2server.SubjectResolverFunc(func(context.Context, *http.Request) (*oauth2server.Subject, error) {
				// A resolver that meant to decline says so with a nil Subject.
				// This one hands back a token for whoever the resource server
				// decides the empty string is.
				return &oauth2server.Subject{Claims: map[string]string{"account_id": "acct_9"}}, nil
			})))

		reg := h.registerConfidential()

		res := h.authorizeGet(authorizeParams(reg.ClientID), withSession)

		errorCode, _ := h.redirectError(res)
		test.EqOp(t, oauth2server.ErrorCodeServerError, errorCode)

		location, err := url.Parse(res.Header.Get("Location"))
		must.NoError(t, err)
		test.EqOp(t, "", location.Query().Get("code"))
	})

	T.Run("the resolver is not consulted until the request has been validated", func(t *testing.T) {
		t.Parallel()

		var resolverCalls atomic.Int64

		h := newHarness(t, oauth2server.WithSubjectResolver(sessionResolver(&resolverCalls)))

		reg := h.registerConfidential()

		params := authorizeParams(reg.ClientID)
		params.Set("redirect_uri", "https://attacker.example/callback")

		res := h.authorizeGet(params, withSession)

		// The seam sits after resolveAuthorizeRequest for the reason exact
		// matching exists: a resolver consulted first would mint a code for a
		// redirect_uri this server has just decided is not the client's.
		must.EqOp(t, http.StatusBadRequest, res.StatusCode)
		test.EqOp(t, int64(0), resolverCalls.Load())
	})

	T.Run("a nil resolver registers nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, oauth2server.WithSubjectResolver(nil))

		reg := h.registerConfidential()

		res := h.authorizeGet(authorizeParams(reg.ClientID), withSession)
		must.EqOp(t, http.StatusOK, res.StatusCode)
		test.StrContains(t, readBody(t, res), oauth2server.FieldPassword)
	})

	T.Run("a server built without one behaves exactly as it did", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		reg := h.registerConfidential()

		// The credential a resolver would have read is present and means
		// nothing, on both verbs: the GET renders, the POST authenticates.
		res := h.authorizeGet(authorizeParams(reg.ClientID), withSession)
		must.EqOp(t, http.StatusOK, res.StatusCode)
		test.StrContains(t, readBody(t, res), oauth2server.FieldPassword)

		must.NotEq(t, "", h.codeFrom(h.authorizePost(authorizeParams(reg.ClientID), login(), withSession)))
	})
}

package httpclient

import (
	"net/http"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewBaseURLClient(t *testing.T) {
	t.Parallel()

	t.Run("it reports the root it resolves against", func(t *testing.T) {
		t.Parallel()

		bound, err := NewBaseURLClient(&http.Client{}, "https://leader.example/api")
		must.NoError(t, err)
		test.EqOp(t, "https://leader.example/api", bound.BaseURL())
	})

	for _, tc := range []struct {
		wantErr error
		name    string
		baseURL string
	}{
		{name: "empty", baseURL: "", wantErr: platformerrors.ErrEmptyInputParameter},
		{name: "relative", baseURL: "/api", wantErr: platformerrors.ErrUnrecognizedInputValue},
		{name: "scheme with no host", baseURL: "https://", wantErr: platformerrors.ErrUnrecognizedInputValue},
		{name: "carries a query", baseURL: "https://leader.example/api?v=2", wantErr: platformerrors.ErrUnrecognizedInputValue},
		{name: "carries a fragment", baseURL: "https://leader.example/api#top", wantErr: platformerrors.ErrUnrecognizedInputValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bound, err := NewBaseURLClient(&http.Client{}, tc.baseURL)
			must.ErrorIs(t, err, tc.wantErr)
			must.Nil(t, bound)
		})
	}

	t.Run("an unparseable base URL is reported", func(t *testing.T) {
		t.Parallel()

		bound, err := NewBaseURLClient(&http.Client{}, "://leader.example")
		must.Error(t, err)
		must.Nil(t, bound)
	})

	t.Run("no client is a reported failure rather than a panic", func(t *testing.T) {
		t.Parallel()

		bound, err := NewBaseURLClient(nil, "https://leader.example")
		must.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		must.Nil(t, bound)
	})
}

// The joining is the reason this type exists: the slash that is doubled and the
// slash that is missing both produce a request that reaches a real server and
// comes back 404, which reads like a broken service rather than a broken URL.
func TestBaseURLClientResolution(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		baseURL string
		path    string
		want    string
	}{
		{name: "base without a trailing slash", baseURL: "https://leader.example/api", path: "/v1/claim", want: "https://leader.example/api/v1/claim"},
		{name: "base with a trailing slash", baseURL: "https://leader.example/api/", path: "/v1/claim", want: "https://leader.example/api/v1/claim"},
		{name: "path without a leading slash", baseURL: "https://leader.example/api", path: "v1/claim", want: "https://leader.example/api/v1/claim"},
		{name: "bare host", baseURL: "https://leader.example", path: "/v1/claim", want: "https://leader.example/v1/claim"},
		{name: "empty path", baseURL: "https://leader.example/api", path: "", want: "https://leader.example/api"},
		{name: "a query rides along", baseURL: "https://leader.example/api", path: "/v1/claim?worker=w-1", want: "https://leader.example/api/v1/claim?worker=w-1"},
		{name: "an escaped segment stays escaped", baseURL: "https://leader.example/api", path: "/v1/claim/a%2Fb", want: "https://leader.example/api/v1/claim/a%2Fb"},
		{name: "an absolute URL passes through", baseURL: "https://leader.example/api", path: "https://elsewhere.example/v2/thing", want: "https://elsewhere.example/v2/thing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := &recordingTransport{resp: response(http.StatusOK, `{}`)}

			bound, err := NewBaseURLClient(exchangeClient(t, transport), tc.baseURL)
			must.NoError(t, err)

			_, err = Exchange[claim](t.Context(), bound, http.MethodGet, tc.path, nil)
			must.NoError(t, err)

			test.EqOp(t, tc.want, transport.seen.URL.String())

			// URL.String puts a leading slash back on a rootless path, so the
			// string above cannot tell whether the path itself is rooted — and
			// the path is what reaches StatusError.Path and a log line.
			test.StrHasPrefix(t, "/", transport.seen.URL.Path)
		})
	}
}

func TestBaseURLClientDo(t *testing.T) {
	t.Parallel()

	t.Run("it leaves the caller's request alone", func(t *testing.T) {
		t.Parallel()

		transport := &recordingTransport{resp: response(http.StatusOK, `{}`)}

		bound, err := NewBaseURLClient(exchangeClient(t, transport), "https://leader.example/api")
		must.NoError(t, err)

		req := newRequest(t.Context(), http.MethodGet, "/v1/claim", nil)

		resp, err := bound.Do(req)
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.EqOp(t, "/v1/claim", req.URL.String())
		test.EqOp(t, "https://leader.example/api/v1/claim", transport.seen.URL.String())
	})

	// A request with no URL has nothing to resolve, and the wrapped client
	// already reports it properly. Dereferencing it here to find that out would
	// turn a returned error into a panic.
	t.Run("a request with no URL is passed along to be reported", func(t *testing.T) {
		t.Parallel()

		transport := &recordingTransport{resp: response(http.StatusOK, `{}`)}

		bound, err := NewBaseURLClient(exchangeClient(t, transport), "https://leader.example/api")
		must.NoError(t, err)

		resp, err := bound.Do(&http.Request{})
		must.Error(t, err)
		must.Nil(t, resp)
		must.Nil(t, transport.seen)
	})
}

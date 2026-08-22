package httpclient

import (
	"net/http"
	"net/url"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// BaseURLClient binds a Doer to one service's root, so a call site names a path
// and not a URL:
//
//	leader, err := httpclient.NewBaseURLClient(client, "https://leader.internal/api")
//	claim, err := httpclient.Exchange[ClaimResponse](ctx, leader, http.MethodPost, "/v1/claim", request)
//
// It is a Doer itself, so it goes anywhere one does and composes with nothing
// else: the client it wraps keeps every transport it was built with, and this
// adds no behavior beyond resolving the URL.
//
// The joining is url.URL.JoinPath's, which is the reason to have this at all.
// Concatenating a configured base and a literal path is where the double slash
// and the missing slash come from, and both produce a request that reaches a
// real server and gets a 404 — the failure mode that looks like the service is
// broken rather than like the URL is.
//
// A request whose URL is already absolute passes through untouched, so a caller
// with one endpoint elsewhere does not need a second client.
type BaseURLClient struct {
	doer Doer
	base *url.URL
}

var _ Doer = (*BaseURLClient)(nil)

// NewBaseURLClient binds doer to baseURL.
//
// baseURL must be absolute — scheme and host — and must carry no query or
// fragment. A base with a query is rejected rather than merged, because there
// is no reading of "the base says ?v=2 and the path says ?v=3" that is not a
// surprise to somebody, and a caller who wants a parameter on every request has
// a clearer place to put it than a URL.
//
// Its path is a prefix, not a document: "https://host/api" and
// "https://host/api/" both resolve "/v1/claim" to "https://host/api/v1/claim",
// which is the one thing string concatenation and url.URL.ResolveReference each
// get wrong in a different direction.
func NewBaseURLClient(doer Doer, baseURL string) (*BaseURLClient, error) {
	if doer == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "binding a base URL to no client")
	}

	if baseURL == "" {
		return nil, platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "binding a client to an empty base URL")
	}

	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "parsing base URL %q", baseURL)
	}

	if !base.IsAbs() || base.Host == "" {
		return nil, platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "base URL %q is not absolute", baseURL)
	}

	if base.RawQuery != "" || base.Fragment != "" {
		return nil, platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "base URL %q carries a query or a fragment", baseURL)
	}

	// A bare host has no path, and joining onto nothing yields a path with no
	// leading slash — legal, since URL.String puts one back, but it reaches
	// StatusError.Path and a log line as "v1/claim". Rooting the base here is
	// the one place that can be fixed once.
	if base.Path == "" {
		base.Path = "/"
	}

	return &BaseURLClient{doer: doer, base: base}, nil
}

// Do resolves a relative request URL against the base and sends it.
//
// The request is cloned rather than rewritten, so a caller that built one and
// kept a reference — to read its headers in a test, to send it again elsewhere
// — still holds the request it built.
func (c *BaseURLClient) Do(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.IsAbs() {
		return c.doer.Do(req)
	}

	resolved := req.Clone(req.Context())
	resolved.URL = c.base.JoinPath(req.URL.EscapedPath())
	resolved.URL.RawQuery = req.URL.RawQuery
	resolved.URL.Fragment = req.URL.Fragment

	return c.doer.Do(resolved)
}

// BaseURL reports the root this client resolves against.
func (c *BaseURLClient) BaseURL() string {
	return c.base.String()
}

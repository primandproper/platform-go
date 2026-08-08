package client

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/primandproper/platform-go/v10/encoding"
	platformerrors "github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/observability"
	"github.com/primandproper/platform-go/v10/observability/logging"
	"github.com/primandproper/platform-go/v10/observability/tracing"
)

const observerName = "routing_client"

// Client is one service's address, transport, and wire format. It holds nothing
// about any particular route — that is what an Endpoint is for — so a consumer
// builds one Client per service it calls and passes it to Call for every route
// on that service.
//
// It is safe for concurrent use.
type Client struct {
	base       *url.URL
	httpClient *http.Client
	codec      encoding.Codec
	o11y       observability.Observer

	maxResponseBytes int64
	envelope         bool
}

// New builds a Client for the service at baseURL.
//
// baseURL must be absolute — scheme and host — because the client's job is to
// address another process, and a relative base cannot. A path on it is a prefix
// every route is served under, so a base of "https://api.example.com/v1" and a
// pattern of "/orders" reach /v1/orders.
func New(baseURL string, opts ...Option) (*Client, error) {
	cfg := newOptions(opts)

	base, err := parseBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	logger := logging.EnsureLogger(cfg.logger)
	tracerProvider := tracing.EnsureTracerProvider(cfg.tracerProvider)

	codec := cfg.codec
	if codec == nil {
		codec = encoding.NewClientEncoder(
			encoding.ContentTypeJSON,
			encoding.WithLogger(logger),
			encoding.WithTracerProvider(tracerProvider),
		)
	}

	httpClient := cfg.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Client{
		base:             base,
		httpClient:       httpClient,
		codec:            codec,
		o11y:             observability.NewObserver(observerName, logger, tracerProvider),
		maxResponseBytes: cfg.maxResponseBytes,
		envelope:         cfg.envelope,
	}, nil
}

// BaseURL returns the service address this client was built for.
func (c *Client) BaseURL() string { return c.base.String() }

func parseBaseURL(baseURL string) (*url.URL, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "building a routing client without a base URL")
	}

	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "parsing base URL %q", baseURL)
	}

	if base.Scheme == "" || base.Host == "" {
		return nil, platformerrors.Newf("base URL %q is not absolute: it needs a scheme and a host", baseURL)
	}

	// Held without its trailing slash so that joining a pattern — which always
	// begins with one — cannot produce a doubled separator.
	base.Path = strings.TrimSuffix(base.Path, "/")
	base.RawPath = strings.TrimSuffix(base.RawPath, "/")

	return base, nil
}

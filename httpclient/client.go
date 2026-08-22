package httpclient

import (
	"net"
	"net/http"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const (
	defaultKeepAlive           = 30 * time.Second
	defaultTLSHandshakeTimeout = 10 * time.Second
)

// buildClient constructs an HTTP client from resolved options.
func (c *clientConfig) buildClient() (*http.Client, error) {
	obs, err := newTransportObserver(c.logger, c.tracerProvider, c.metricsProvider)
	if err != nil {
		return nil, platformerrors.Wrap(err, "instrumenting the HTTP client")
	}

	transport := c.transport
	if transport == nil {
		transport = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   c.timeout,
				KeepAlive: defaultKeepAlive,
			}).DialContext,
			MaxIdleConns:          c.maxIdleConns,
			MaxIdleConnsPerHost:   c.maxIdleConnsPerHost,
			TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
			ExpectContinueTimeout: 2 * c.timeout,
			IdleConnTimeout:       3 * c.timeout,
		}
	}

	if c.tracing {
		// The provider is passed explicitly: otelhttp falls back to the
		// OpenTelemetry global when it is not, which means a service that
		// configured a provider of its own would have its client spans quietly
		// go somewhere else. EnsureTracerProvider makes the absent case a noop
		// provider rather than that global.
		transport = otelhttp.NewTransport(
			transport,
			otelhttp.WithSpanNameFormatter(tracing.FormatSpan),
			otelhttp.WithTracerProvider(tracing.EnsureTracerProvider(c.tracerProvider)),
		)
	}

	// The middlewares wrap inside-out, so this reads bottom-up: the signer is
	// nearest the wire and the response cache is outermost. The nesting is
	// fixed rather than following the order the options were passed, because
	// only one arrangement of them is correct and a caller who got it wrong
	// would have a client that looks protected and is not.
	//
	// Tracing sits below all of them, so each attempt is its own client span
	// rather than one span covering a loop.
	layered := false

	// Signing lowest of all, for two reasons. Below retry, so every attempt is
	// signed afresh — a retry carrying the first attempt's timestamp arrives
	// stale after a long backoff, and is refused. Below the limiter, so a
	// request that never leaves is never signed: a key resolution and an HMAC
	// over the whole body is real work to spend on something being dropped.
	if c.signer != nil {
		c.signer.base = transport
		c.signer.obs = obs
		transport = c.signer
		layered = true
	}

	if c.rateLimiter != nil {
		c.rateLimiter.base = transport
		c.rateLimiter.obs = obs
		transport = c.rateLimiter
		layered = true
	}

	// Retry above the limiter: every attempt it makes spends a token, so a retry
	// storm is charged against the provider's budget instead of slipping past it.
	if c.retry != nil {
		c.retry.base = transport
		c.retry.obs = obs
		transport = c.retry
		layered = true
	}

	// The breaker above the retry loop: an open circuit rejects before the loop
	// is entered, which is the difference between failing fast and failing fast
	// three times with backoff in between.
	if c.breaker != nil {
		c.breaker.base = transport
		c.breaker.obs = obs
		transport = c.breaker
		layered = true
	}

	// The response cache above all three, because a hit is not a request. It
	// reaches no wire, so it must not report an outcome to a circuit or spend a
	// token from a budget that counts requests the origin actually saw — and
	// the only placement that guarantees that is one where the hit returns
	// before either layer is consulted.
	if c.responseCache != nil {
		c.responseCache.base = transport
		c.responseCache.obs = obs
		transport = c.responseCache
		layered = true
	}

	// Above even the cache, and only when there is something for it to
	// describe: a client with no middleware has nothing between the caller and
	// the tracing transport, so a second span would only duplicate the one
	// otelhttp already emits.
	if layered {
		transport = &observedTransport{base: transport, obs: obs}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   c.timeout,
	}, nil
}

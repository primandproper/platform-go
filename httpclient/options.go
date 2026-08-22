package httpclient

import (
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/circuitbreaking"
	"github.com/primandproper/platform-go/v13/circuitbreaking/partitioned"
	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/ratelimiting"
	"github.com/primandproper/platform-go/v13/retry"
)

// Option customizes the HTTP client returned by NewHTTPClient. Options are
// applied in order, so a later Option overrides an earlier one.
type Option func(*clientConfig)

// clientConfig is the resolved client configuration.
type clientConfig struct {
	// transport, when non-nil, is used as the client's base RoundTripper instead
	// of one built from the settings below. Tracing, if enabled, still wraps it.
	transport http.RoundTripper

	// The middlewares, each held unattached until buildClient knows what it is
	// wrapping. Nil means the client does not have that layer at all, rather
	// than having one that does nothing.
	responseCache *cacheTransport
	breaker       *breakerTransport
	retry         *retryTransport
	rateLimiter   *rateLimitTransport
	signer        *signingTransport

	// The three pillars, each resolved to its noop at build time when absent, so
	// a client asked for no observability records nowhere rather than nil-checks
	// at every point that would have recorded.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	timeout             time.Duration
	maxIdleConns        int
	maxIdleConnsPerHost int
	tracing             bool
}

// newClientConfig builds the default client configuration.
func newClientConfig() *clientConfig {
	return &clientConfig{
		timeout:             defaultTimeout,
		maxIdleConns:        defaultMaxIdleConns,
		maxIdleConnsPerHost: defaultMaxIdleConnsPerHost,
	}
}

// WithTimeout sets the client's overall request timeout, which also bounds the
// dial. A non-positive duration leaves the default (defaultTimeout) in place.
func WithTimeout(timeout time.Duration) Option {
	return func(c *clientConfig) {
		if timeout > 0 {
			c.timeout = timeout
		}
	}
}

// WithMaxIdleConns sets the transport's maximum number of idle connections across
// all hosts. A non-positive value leaves the default in place. It has no effect
// alongside WithTransport.
func WithMaxIdleConns(n int) Option {
	return func(c *clientConfig) {
		if n > 0 {
			c.maxIdleConns = n
		}
	}
}

// WithMaxIdleConnsPerHost sets the transport's maximum number of idle connections
// per host. A non-positive value leaves the default in place. It has no effect
// alongside WithTransport.
func WithMaxIdleConnsPerHost(n int) Option {
	return func(c *clientConfig) {
		if n > 0 {
			c.maxIdleConnsPerHost = n
		}
	}
}

// WithTracing toggles wrapping the transport in OpenTelemetry instrumentation,
// which emits one client span per attempt. Tracing is off by default.
//
// It does not govern the span the resilience layers open around the logical
// request; that one follows WithTracerProvider. The two answer different
// questions — how did this attempt go, versus what did this call cost in
// attempts and rejections — and a caller who has configured a tracer provider
// has no reason to want the second one suppressed.
func WithTracing(enabled bool) Option {
	return func(c *clientConfig) { c.tracing = enabled }
}

// WithLogger sets the logger the resilience layers write to. Absent, they log
// nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(c *clientConfig) { c.logger = logger }
}

// WithTracerProvider sets the tracer provider used both for the span the
// resilience layers open around the logical request and, when WithTracing is
// on, for the per-attempt spans below it.
//
// Absent, both trace nowhere — including the per-attempt spans, which until now
// silently fell back to the OpenTelemetry global rather than to the provider
// the service configured.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(c *clientConfig) { c.tracerProvider = tracerProvider }
}

// WithMetricsProvider sets the metrics provider the resilience layers record
// to: retries taken and exhausted, Retry-After waits honored, circuit
// rejections and outcomes, and requests the local limiter refused. Absent, they
// record nowhere.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(c *clientConfig) { c.metricsProvider = metricsProvider }
}

// WithPillars attaches a logger, tracer provider, and metrics provider in one
// go, for the common case where a caller has already built them together. A nil
// Pillars attaches nothing.
//
// It is applied in order with the individual options, so a caller can hand over
// its pillars and then override one of them.
func WithPillars(p *observability.Pillars) Option {
	return func(c *clientConfig) { c.logger, c.tracerProvider, c.metricsProvider = p.Deps() }
}

// WithTransport uses the given RoundTripper as the client's base transport rather
// than building one, which is the seam for stubbing responses in tests or layering
// custom middleware. The connection-pool options are ignored when it is set;
// tracing, if enabled, still wraps it. A nil RoundTripper is ignored.
func WithTransport(transport http.RoundTripper) Option {
	return func(c *clientConfig) {
		if transport != nil {
			c.transport = transport
		}
	}
}

// WithRetryPolicy retries failed requests through policy.
//
// Only idempotent methods are retried, and only when the request body can be
// replayed — see WithRetryMethods for both caveats. By default a response is
// retried when it is a 5xx, a 408, or a 429; every other 4xx is reported to
// policy as retry.Unretryable, so the loop stops on the first one instead of
// spending its attempts re-asking a question already answered. Pass
// WithRetryClassifier for a service whose status codes mean something else.
// Retry-After is honored up to DefaultMaxRetryAfter.
//
// When the attempts run out the caller gets the last response, not an error: a
// 503 that survived three tries is still the server's answer, and code reading
// the status does not have to learn a second way to find it.
//
// The client's overall timeout bounds the whole loop, retries included, because
// http.Client applies it to the request context before the transport ever runs.
// A client that retries wants that budget raised to match. A nil policy is
// ignored.
func WithRetryPolicy(policy retry.Policy, opts ...RetryOption) Option {
	return func(c *clientConfig) {
		if policy != nil {
			c.retry = newRetryTransport(policy, opts)
		}
	}
}

// WithCircuitBreaker fails requests fast once breaker has tripped, so one dead
// dependency stops tying up connections and timeouts.
//
// The breaker sees a request's final outcome, after any retrying. By default
// transport errors and 5xx responses count as failures, a request this client's
// own limiter refused counts as nothing at all, and everything else counts as a
// success — pass WithOutcomeClassifier to say otherwise, which most real APIs
// eventually require.
//
// One breaker is shared across every host the client talks to, which is the
// right shape when the client belongs to a single integration. Use
// WithKeyedCircuitBreaker for a client that fans out. A nil breaker is ignored.
func WithCircuitBreaker(breaker circuitbreaking.CircuitBreaker, opts ...BreakerOption) Option {
	return func(c *clientConfig) {
		if breaker != nil {
			c.breaker = newBreakerTransport(partitioned.NewKeyedCircuitBreaker(breaker, nil), opts)
		}
	}
}

// WithKeyedCircuitBreaker breaks per host rather than per client, keyed by the
// request URL's host and port.
//
// Hosts registered with the KeyedCircuitBreaker get their own breaker; the rest
// share its global one, so a client that talks to one critical dependency and
// several incidental ones can isolate the dependency without enumerating the
// world. A nil KeyedCircuitBreaker is ignored, as is a key that resolves to no
// breaker at all.
//
// The outcome rule is the same one WithCircuitBreaker documents, and
// WithOutcomeClassifier overrides it the same way — for every host at once,
// since one classifier serves the whole client.
func WithKeyedCircuitBreaker(breakers partitioned.KeyedCircuitBreaker, opts ...BreakerOption) Option {
	return func(c *clientConfig) {
		if breakers != nil {
			c.breaker = newBreakerTransport(breakers, opts)
		}
	}
}

// WithHTTPCache answers repeated GETs and HEADs of a stable resource from store
// instead of from the origin.
//
// It is the layer above the resilience three, and that placement is the whole
// design: a hit makes no wire request, so it consults no circuit and spends no
// rate-limit token. A miss or a revalidation passes through all three exactly
// as an uncached request would.
//
// The policy is RFC 9111 read narrowly. Cache-Control and Expires decide
// freshness; ETag and Last-Modified drive revalidation, and a 304 refreshes the
// stored entry rather than replacing it. What the RFC leaves to a cache's
// discretion, this errs toward not caching: no-store, private, Set-Cookie,
// Vary: *, and an Authorization header without WithCacheAuthorized all mean the
// response is not stored.
//
// Most origins worth caching send no freshness headers at all, which is why
// callers hand-roll TTL maps in front of them. WithCacheTTL is the supported
// version of that, and it loses to anything the origin actually said:
//
//	client, err := httpclient.NewHTTPClient(
//	    httpclient.WithHTTPCache(store, httpclient.WithCacheTTL(5*time.Minute)),
//	)
//
// The cache's own default expiry governs how long an entry is retained; the
// freshness above governs when it must be revalidated. Retaining an entry past
// its freshness is what makes a 304 possible, so a store configured to expire
// entries the moment they go stale gives up the cheaper half of this.
//
// A nil cache is ignored.
func WithHTTPCache(store cache.Cache[CachedResponse], opts ...CacheOption) Option {
	return func(c *clientConfig) {
		if store != nil {
			c.responseCache = newCacheTransport(store, opts)
		}
	}
}

// WithRequestSigning stamps a signature over every outgoing request body, so
// the far side can prove the call came from a holder of the shared key.
//
// It is the outbound half of what requestsigning/http's middleware does
// inbound, over the same requestsigning.Signer — so a first-party caller and
// the service it calls can be configured from one scheme and one key source.
//
//	keys, err := requestsigning.NewSecretKeySource(secretSource, "SIGNING_KEY", "SIGNING_KEY_PREVIOUS")
//	signer, err := requestsigning.NewSigner(keys)
//
//	client, err := httpclient.NewHTTPClient(
//		httpclient.WithRequestSigning(signer),
//		httpclient.WithRetryPolicy(policy),
//	)
//
// # Where it sits
//
// Below the retry loop, so each attempt is signed afresh. A signature carries a
// timestamp and the receiver rejects a stale one; a retry that fired after
// thirty seconds of backoff still carrying the first attempt's timestamp would
// arrive outside the tolerance and be refused — a failure that appears only
// under the conditions that caused the retry in the first place.
//
// Below the rate limiter too, so a request the local limiter refused is never
// signed at all. Signing costs a key resolution and an HMAC over the whole
// body, and spending either on a request that will not be sent is waste.
//
// # The body
//
// Every signed request's body is buffered whole, because a MAC over it cannot
// be computed any other way, and the buffered copy is what gets sent. A client
// that streams large uploads should not sign them.
//
// A nil signer is ignored.
func WithRequestSigning(signer requestsigning.Signer) Option {
	return func(c *clientConfig) {
		if signer != nil {
			c.signer = &signingTransport{signer: signer}
		}
	}
}

// WithRateLimit spends a token from limiter, keyed by the request URL's host and
// port, before each request reaches the wire.
//
// It is the layer closest to the network, so every attempt a retry loop makes
// pays for itself — which is the point, since a provider's documented budget
// counts requests, not the caller's intentions. A refused request fails with
// ratelimiting.ErrRateLimited; when a retry policy is also installed, its
// backoff is what waits for the bucket to refill. A nil limiter is ignored.
func WithRateLimit(limiter ratelimiting.RateLimiter) Option {
	return func(c *clientConfig) {
		if limiter != nil {
			c.rateLimiter = &rateLimitTransport{limiter: limiter}
		}
	}
}

package http

import (
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v13/idempotency"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

const (
	// HeaderName is the request header carrying the key. Both halves of this
	// package read it from here, so the client and the server cannot drift.
	HeaderName = "Idempotency-Key"
	// ReplayHeader marks a response that was replayed rather than produced.
	ReplayHeader = "Idempotent-Replayed"
	// BodyOmittedHeader marks a replay whose body was dropped for exceeding
	// the recorded-response cap.
	BodyOmittedHeader = "Idempotency-Body-Omitted"

	// DefaultMaxRequestBodyBytes bounds how much of a request body is read to
	// fingerprint it.
	DefaultMaxRequestBodyBytes = 1 << 20 // 1 MiB
	// DefaultMaxResponseBytes bounds how much of a response body is recorded.
	// Beyond it the status is still recorded, so the effect does not repeat,
	// but the body is dropped.
	DefaultMaxResponseBytes = 256 << 10 // 256 KiB
	// DefaultRetryAfter is the Retry-After sent with a 409, giving a client
	// some idea of when the in-flight work might have finished.
	DefaultRetryAfter = time.Second
)

// defaultMethods are the methods that participate. Safe methods are excluded
// even when a key is present: they have no effect to deduplicate, and
// recording them would spend the store on nothing.
var defaultMethods = []string{
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
}

// defaultReplayedHeaders is the allowlist a replay reproduces.
//
// Deliberately minimal. This middleware runs inside the standard stack, so
// CORS, request IDs, and trace headers are reapplied by outer middleware on
// the replay and are correctly fresh; replaying the stored copies would stamp
// the replay with the original request's trace. A stored Set-Cookie is worse
// still — it would re-set a session that has since moved on.
var defaultReplayedHeaders = []string{"Content-Type"}

type (
	// Option configures the middleware.
	Option func(*config)

	// TransportOption configures the client transport.
	TransportOption func(*transport)

	config struct {
		principal   func(*http.Request) (string, error)
		fingerprint func(*http.Request, []byte) (idempotency.Fingerprint, error)

		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		headerName       string
		replayHeader     string
		methods          []string
		replayedHeaders  []string
		maxRequestBody   int64
		maxResponseBytes int
		retryAfter       time.Duration
	}
)

func newConfig(opts ...Option) *config {
	cfg := &config{
		headerName:       HeaderName,
		replayHeader:     ReplayHeader,
		methods:          defaultMethods,
		replayedHeaders:  defaultReplayedHeaders,
		maxRequestBody:   DefaultMaxRequestBodyBytes,
		maxResponseBytes: DefaultMaxResponseBytes,
		retryAfter:       DefaultRetryAfter,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

// WithHeaderName overrides the request header carrying the key.
func WithHeaderName(name string) Option {
	return func(c *config) {
		if name != "" {
			c.headerName = name
		}
	}
}

// WithReplayHeaderName overrides the response header marking a replay. An
// empty name suppresses it.
func WithReplayHeaderName(name string) Option {
	return func(c *config) {
		c.replayHeader = name
	}
}

// WithMethods overrides which methods participate.
func WithMethods(methods ...string) Option {
	return func(c *config) {
		if len(methods) > 0 {
			c.methods = methods
		}
	}
}

// WithPrincipalExtractor supplies the caller identity to fold into the
// fingerprint.
//
// There is no platform-wide notion of a principal to read from, so this is how
// one gets in. Supplying it matters for multi-tenant APIs: without it, two
// users who send the same key for the same request would share a record, and
// the second would be handed the first's response.
func WithPrincipalExtractor(extract func(*http.Request) (string, error)) Option {
	return func(c *config) {
		if extract != nil {
			c.principal = extract
		}
	}
}

// WithFingerprint replaces how a request is fingerprinted.
//
// Use it when the default is too strict — most usefully to canonicalize a JSON
// body, since the default hashes raw bytes and a client that re-serializes
// before retrying would otherwise be reported as reusing its key.
func WithFingerprint(fn func(req *http.Request, body []byte) (idempotency.Fingerprint, error)) Option {
	return func(c *config) {
		if fn != nil {
			c.fingerprint = fn
		}
	}
}

// WithReplayedHeaders overrides the headers a replay reproduces. Keep it
// short — see defaultReplayedHeaders for what goes wrong otherwise.
func WithReplayedHeaders(names ...string) Option {
	return func(c *config) {
		c.replayedHeaders = names
	}
}

// WithMaxRequestBodyBytes bounds how much of a request body is read. A request
// over the limit is answered with 413 rather than fingerprinted on a prefix,
// which would let two different requests share a fingerprint.
func WithMaxRequestBodyBytes(limit int64) Option {
	return func(c *config) {
		if limit > 0 {
			c.maxRequestBody = limit
		}
	}
}

// WithMaxResponseBytes bounds how much of a response body is recorded. Beyond
// it the status is still recorded and the body dropped, so the guarantee holds
// and only the convenience is lost.
func WithMaxResponseBytes(limit int) Option {
	return func(c *config) {
		if limit > 0 {
			c.maxResponseBytes = limit
		}
	}
}

// WithRetryAfter sets the Retry-After sent with a 409.
func WithRetryAfter(after time.Duration) Option {
	return func(c *config) {
		if after > 0 {
			c.retryAfter = after
		}
	}
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(c *config) {
		c.logger = logger
	}
}

// WithTracerProvider attaches a tracer provider.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(c *config) {
		c.tracerProvider = tracerProvider
	}
}

// WithMetricsProvider attaches a metrics provider.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(c *config) {
		c.metricsProvider = metricsProvider
	}
}

// WithTransportHeaderName overrides the header the transport stamps.
func WithTransportHeaderName(name string) TransportOption {
	return func(t *transport) {
		if name != "" {
			t.headerName = name
		}
	}
}

// WithTransportMethods overrides which methods the transport stamps.
func WithTransportMethods(methods ...string) TransportOption {
	return func(t *transport) {
		if len(methods) > 0 {
			t.methods = methods
		}
	}
}

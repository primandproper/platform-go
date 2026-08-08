package client

import (
	"net/http"

	"github.com/primandproper/platform-go/v10/encoding"
	"github.com/primandproper/platform-go/v10/observability/logging"
	"github.com/primandproper/platform-go/v10/observability/tracing"
)

type (
	// Option configures a Client. The zero configuration works: JSON over
	// http.DefaultClient, expecting the platform response envelope, logging and
	// tracing nowhere.
	Option func(*options)

	options struct {
		httpClient       *http.Client
		codec            encoding.Codec
		logger           logging.Logger
		tracerProvider   tracing.Provider
		maxResponseBytes int64
		envelope         bool
	}
)

// defaultMaxResponseBytes caps how much of a response this package will read
// into memory before giving up on it.
//
// A typed client reads the whole body before decoding, and the body comes from
// another process. Without a ceiling, one misbehaving — or hostile — service can
// exhaust the memory of every caller that talks to it, and the caller's own
// timeouts do not help, because the bytes keep arriving. The default is far
// above any JSON response a typed route should be producing; raise it with
// WithMaxResponseBytes for a route that genuinely returns more.
const defaultMaxResponseBytes int64 = 32 << 20 // 32 MiB

func newOptions(opts []Option) *options {
	cfg := &options{envelope: true, maxResponseBytes: defaultMaxResponseBytes}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

// WithHTTPClient supplies the *http.Client every call goes out on. This is where
// retries, circuit breaking, rate limiting, response caching, and request signing
// come from: build the client with the platform's httpclient package and pass it
// here. Absent, http.DefaultClient is used, which has none of those and no
// timeout.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(o *options) { o.httpClient = httpClient }
}

// WithCodec sets how request bodies are marshaled and responses unmarshaled, and
// therefore the Content-Type and Accept headers the client sends. It must be a
// content type the service speaks. Absent, JSON.
func WithCodec(codec encoding.Codec) Option {
	return func(o *options) { o.codec = codec }
}

// WithEnvelope declares whether the service wraps success responses in
// errors/http.APIResponse[Out], mirroring routing.WithDefaultEnvelope on the
// serving side. It is on by default, because it is on by default there.
//
// It is a property of the client rather than of an Endpoint because it is a
// property of the service's wire format, and a descriptor is shared by both
// sides — a service that overrides it for a single route with routing.WithEnvelope
// needs a second Client for those routes.
func WithEnvelope(enabled bool) Option {
	return func(o *options) { o.envelope = enabled }
}

// WithMaxResponseBytes caps how large a response body the client will read. A
// body over the cap fails the call rather than being truncated, because a
// truncated body decodes into a value that is quietly missing fields. A
// non-positive value restores the default.
func WithMaxResponseBytes(n int64) Option {
	return func(o *options) {
		if n <= 0 {
			n = defaultMaxResponseBytes
		}

		o.maxResponseBytes = n
	}
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling a span around every
// call.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

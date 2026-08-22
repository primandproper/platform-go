package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// MetadataKey is the incoming metadata entry carrying the key. gRPC lowercases
// metadata keys, so this must stay lowercase. Both halves of this package read
// it from here, so the client and the server cannot drift.
const MetadataKey = "idempotency-key"

// DefaultMaxResponseBytes is zero: no cap.
//
// Unlike HTTP, gRPC already bounds replies — grpc-go enforces a maximum
// message size on both ends, four megabytes by default — so a second limit
// here would mostly duplicate one that already exists. WithMaxResponseBytes is
// still available for operators who want a tighter bound on what the record
// store holds.
const DefaultMaxResponseBytes = 0

type (
	// Option configures the server interceptor.
	Option func(*config)

	// ClientOption configures the client interceptor.
	ClientOption func(*clientConfig)

	config struct {
		principal    func(context.Context) (string, error)
		methodFilter func(string) bool

		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		metadataKey      string
		maxResponseBytes int
	}

	clientConfig struct {
		methodFilter func(string) bool
		metadataKey  string
	}
)

func newConfig(opts ...Option) *config {
	cfg := &config{
		metadataKey:      MetadataKey,
		maxResponseBytes: DefaultMaxResponseBytes,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

func newClientConfig(opts ...ClientOption) *clientConfig {
	cfg := &clientConfig{metadataKey: MetadataKey}

	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

// WithMetadataKey overrides the metadata entry carrying the key.
func WithMetadataKey(key string) Option {
	return func(c *config) {
		if key != "" {
			c.metadataKey = key
		}
	}
}

// WithMethodFilter limits which methods participate. Methods it rejects pass
// through untouched even when a key is present.
func WithMethodFilter(filter func(fullMethod string) bool) Option {
	return func(c *config) {
		if filter != nil {
			c.methodFilter = filter
		}
	}
}

// WithPrincipalExtractor supplies the caller identity folded into the
// fingerprint.
//
// Supplying it matters for a multi-tenant service: without it, two callers
// sending the same key for the same request would share a record, and the
// second would be handed the first's reply.
func WithPrincipalExtractor(extract func(context.Context) (string, error)) Option {
	return func(c *config) {
		if extract != nil {
			c.principal = extract
		}
	}
}

// WithMaxResponseBytes bounds how much of a reply is recorded. Beyond it the
// outcome is still recorded, so the effect does not repeat, but a replay can
// only report that the reply is gone.
func WithMaxResponseBytes(limit int) Option {
	return func(c *config) {
		if limit > 0 {
			c.maxResponseBytes = limit
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

// WithClientMetadataKey overrides the metadata entry the client stamps.
func WithClientMetadataKey(key string) ClientOption {
	return func(c *clientConfig) {
		if key != "" {
			c.metadataKey = key
		}
	}
}

// WithClientMethodFilter limits which methods the client stamps.
func WithClientMethodFilter(filter func(fullMethod string) bool) ClientOption {
	return func(c *clientConfig) {
		if filter != nil {
			c.methodFilter = filter
		}
	}
}

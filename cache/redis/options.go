package redis

import (
	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures a redis cache at construction.
//
// It carries no type parameter even though the cache does. Go cannot infer a
// type argument from a call's result type, so an Option[T] would force every
// call site to spell the cached type out by hand — WithLogger[MyValue](l) —
// forever. WithCodec is the one option that depends on the cached type; it
// stays generic but still needs no annotation, because T is inferable from the
// codec it is handed.
type Option func(*options)

// options accumulates what the options set, so Option can stay free of the
// cache's type parameter.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	// codec holds a cache.Codec[T] for the T of the cache being built. It is
	// typed as any because Option cannot name T; NewRedisCache asserts it back
	// to the concrete type and reports a mismatch rather than ignoring it.
	codec any

	scanPageSize int64
}

// WithCodec swaps the value codec. The default is cache.NewDefaultCodec; supply
// cache.NewGobCodec for values with interface-typed fields, or a codec of your
// own when a fixed format beats a self-describing one. Values written with one
// codec are unreadable through another — see cache.Codec for the migration
// caveat.
//
// T is inferred from the codec, so this needs no type argument:
//
//	redis.WithCodec(cache.NewGobCodec[Session]())
//
// It must match the cache it configures. Because Option carries no type
// parameter, a codec for the wrong type cannot be rejected by the compiler;
// NewRedisCache returns ErrCodecTypeMismatch instead, at construction.
func WithCodec[T any](codec cache.Codec[T]) Option {
	return func(o *options) {
		if codec != nil {
			o.codec = codec
		}
	}
}

// WithScanPageSize sets the COUNT a single SCAN iteration asks for during
// prefix deletion. It is a hint to redis, not a guarantee: an iteration may
// return more or fewer keys.
//
// The default of 1000 trades round trips against per-command latency. Raise it
// to sweep a large keyspace in fewer round trips; lower it on a latency-
// sensitive shared instance, since redis serves SCAN on its single command
// thread and a large COUNT blocks other clients for the duration. A
// non-positive size is ignored, keeping the default.
func WithScanPageSize(size int64) Option {
	return func(o *options) {
		if size > 0 {
			o.scanPageSize = size
		}
	}
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on every cache
// operation. An absent tracer provider traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the cache's hit, miss,
// set, delete, and error counters and its latency histogram. An absent
// provider records nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

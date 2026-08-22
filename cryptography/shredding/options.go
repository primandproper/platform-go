package shredding

import (
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures Keys.
type Option func(*KeyManager)

// WithClock swaps the clock stamping mint and destruction times, and deciding
// when a cached key has expired.
func WithClock(c clock.Clock) Option {
	return func(k *KeyManager) {
		if c != nil {
			k.clock = c
		}
	}
}

// WithLogger attaches a logger.
//
// Worth setting. A failed invalidation broadcast is reported through it and
// nowhere else — there is no caller to return it to, by design — so without one
// the difference between "erasure completes in milliseconds" and "erasure
// completes in five minutes" is visible only in a counter.
func WithLogger(logger logging.Logger) Option {
	return func(k *KeyManager) {
		k.logger = logger
	}
}

// WithTracerProvider attaches a tracer provider.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(k *KeyManager) {
		k.tracerProvider = tracerProvider
	}
}

// WithMetricsProvider attaches a metrics provider.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(k *KeyManager) {
		k.metricsProvider = metricsProvider
	}
}

// WithKeyTTL bounds how long an unwrapped data key stays in this process, and
// therefore how long after a shred this replica can still read what the key
// protected.
//
// This is a guarantee, not a tuning knob: whatever a deployment tells a subject
// about when erasure completes, this is the number it is telling them. Longer
// buys fewer KMS calls and lengthens that window by exactly as much.
//
// Zero turns the cache off. Every operation then costs an unwrap, and erasure
// completes on the call.
func WithKeyTTL(ttl time.Duration) Option {
	return func(k *KeyManager) {
		if ttl >= 0 {
			k.ttl = ttl
		}
	}
}

// WithMaxCachedKeys bounds how many plaintext data keys this process holds at
// once. At the cap an arbitrary key is dropped to make room; a dropped key costs
// one unwrap to get back.
func WithMaxCachedKeys(maxKeys int) Option {
	return func(k *KeyManager) {
		if maxKeys >= 0 {
			k.maxCached = maxKeys
		}
	}
}

// WithBroadcaster announces every shred to the other replicas, so their cached
// copies of the key go with it instead of surviving to their own expiry.
//
// It shortens the window WithKeyTTL bounds; it does not replace it. Nothing this
// can be wired to delivers to a replica that was restarting at the time, and the
// redis provider is at-most-once by construction, so the TTL stays the number a
// deployment can promise.
func WithBroadcaster(broadcaster Broadcaster) Option {
	return func(k *KeyManager) {
		if broadcaster != nil {
			k.broadcaster = broadcaster
		}
	}
}

// InvalidationOption configures the subscribing half of the invalidation
// broadcast.
type InvalidationOption func(*invalidationHandler)

// WithInvalidationLogger attaches a logger.
func WithInvalidationLogger(logger logging.Logger) InvalidationOption {
	return func(h *invalidationHandler) {
		h.logger = logger
	}
}

// WithInvalidationTracerProvider attaches a tracer provider.
//
// Worth setting. The span it produces is what links a shred on one replica to
// the cached key it dropped on another, which is otherwise two unrelated
// operations minutes apart.
func WithInvalidationTracerProvider(tracerProvider tracing.Provider) InvalidationOption {
	return func(h *invalidationHandler) {
		h.tracerProvider = tracerProvider
	}
}

// WithInvalidationMetricsProvider attaches a metrics provider.
func WithInvalidationMetricsProvider(metricsProvider metrics.Provider) InvalidationOption {
	return func(h *invalidationHandler) {
		h.metricsProvider = metricsProvider
	}
}

// SQLStoreOption configures a SQL Store.
type SQLStoreOption func(*SQLStore)

// WithTablePrefix overrides DefaultTablePrefix. It must be a plain SQL
// identifier fragment: it is interpolated into the query text, not bound as a
// parameter, and it must match the prefix the migrations were rendered with.
func WithTablePrefix(prefix string) SQLStoreOption {
	return func(s *SQLStore) {
		if prefix != "" {
			s.tables = newTables(prefix)
		}
	}
}

// WithStoreLogger attaches a logger.
func WithStoreLogger(logger logging.Logger) SQLStoreOption {
	return func(s *SQLStore) {
		s.logger = logger
	}
}

// WithStoreTracerProvider attaches a tracer provider.
func WithStoreTracerProvider(tracerProvider tracing.Provider) SQLStoreOption {
	return func(s *SQLStore) {
		s.tracerProvider = tracerProvider
	}
}

// WithStoreMetricsProvider attaches a metrics provider.
func WithStoreMetricsProvider(metricsProvider metrics.Provider) SQLStoreOption {
	return func(s *SQLStore) {
		s.metricsProvider = metricsProvider
	}
}

package idempotency

import (
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

type (
	// Option configures a Manager at construction.
	//
	// It is deliberately not parameterized on the Manager's T. None of these
	// settings depend on it, and Go cannot infer a type argument from a call's
	// result type — so an Option would force every call site to spell the
	// Manager's type out by hand — WithTTL[Receipt](time.Hour) — forever.
	//
	// WithRecordable is the one setting that does depend on T. It stays generic
	// but still needs no annotation, because T is inferable from the predicate
	// it is handed; see its documentation for how a mismatch is reported.
	Option func(*managerOptions)

	// managerOptions accumulates what the options set, so that Option can stay
	// free of the Manager's type parameter.
	managerOptions struct {
		clock           clock.Clock
		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		// recordable holds a func(*T) bool for the T of the Manager being
		// built. It is typed as any because Option cannot name T; NewManager
		// asserts it back to the concrete signature and reports a mismatch
		// rather than ignoring it.
		recordable any

		keyPrefix          *string
		ttl                time.Duration
		inFlightTTL        time.Duration
		maxKeyLength       int
		storeFailurePolicy StoreFailurePolicy
	}
)

// WithTTL sets how long a completed record stays replayable.
func WithTTL(ttl time.Duration) Option {
	return func(o *managerOptions) {
		if ttl > 0 {
			o.ttl = ttl
		}
	}
}

// WithInFlightTTL bounds how long a claim survives without completing.
//
// It is the deadline for the guarded work, not a performance knob. Set it
// below the work's worst case and a slow execution loses its claim while still
// running, which is the one path that can still produce a duplicate effect —
// watch idempotency_claims_lost.
func WithInFlightTTL(ttl time.Duration) Option {
	return func(o *managerOptions) {
		if ttl > 0 {
			o.inFlightTTL = ttl
		}
	}
}

// WithMaxKeyLength overrides the longest accepted key.
func WithMaxKeyLength(maxLength int) Option {
	return func(o *managerOptions) {
		if maxLength > 0 {
			o.maxKeyLength = maxLength
		}
	}
}

// WithKeyPrefix overrides the namespace applied to store and lock keys.
//
// An empty prefix is honored rather than ignored, so a caller can deliberately
// opt out of namespacing; that is why this is the one setting held as a pointer.
func WithKeyPrefix(prefix string) Option {
	return func(o *managerOptions) {
		o.keyPrefix = &prefix
	}
}

// WithRecordable sets the predicate deciding whether a result is worth
// recording. A result it rejects releases the claim instead, so the next
// attempt runs the work again.
//
// This is how a caller expresses "that failure was ours, not theirs": a
// server-side error usually means the effect did not land, and pinning it for
// the whole TTL would strand a client that could have succeeded on retry.
//
// T is inferred from the predicate, so this needs no type argument:
//
//	idempotency.WithRecordable(func(r *Receipt) bool { return r.Charged })
//
// It must match the Manager it configures. Because Option carries no type
// parameter, a predicate for the wrong type cannot be rejected by the compiler;
// NewManager returns ErrRecordableTypeMismatch instead, at construction, before
// any work runs through it.
func WithRecordable[T any](recordable func(*T) bool) Option {
	return func(o *managerOptions) {
		if recordable != nil {
			o.recordable = recordable
		}
	}
}

// WithStoreFailurePolicy chooses what happens when the store cannot be read.
func WithStoreFailurePolicy(policy StoreFailurePolicy) Option {
	return func(o *managerOptions) {
		o.storeFailurePolicy = policy
	}
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *managerOptions) {
		o.logger = logger
	}
}

// WithTracerProvider attaches a tracer provider.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *managerOptions) {
		o.tracerProvider = tracerProvider
	}
}

// WithMetricsProvider attaches a metrics provider.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *managerOptions) {
		o.metricsProvider = metricsProvider
	}
}

// WithClock swaps the clock used to stamp records.
func WithClock(c clock.Clock) Option {
	return func(o *managerOptions) {
		if c != nil {
			o.clock = c
		}
	}
}

// DoOption overrides a Manager-level setting for one call.
//
// The Manager's own settings are the defaults for every call through it; these
// exist so that one Manager can serve endpoints whose requirements differ,
// rather than forcing a second Manager per variation.
//
// Like Option, it carries no type parameter: nothing here depends on the
// Manager's T, and one would only force it onto every call site.
type DoOption func(*doOptions)

// doOptions holds the per-call overrides. A nil field means "inherit from the
// Manager", which is what keeps an option's absence distinguishable from an
// option set to a zero value.
type doOptions struct {
	ttl *time.Duration
}

// newDoOptions applies opts, ignoring nil entries.
func newDoOptions(opts []DoOption) *doOptions {
	o := &doOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithCallTTL overrides how long this call's completed record is retained.
//
// Retention is the window in which a retry replays instead of re-running, so it
// belongs to the operation rather than to the Manager: a payment worth
// protecting for a day and a profile update worth protecting for a minute can
// then share one Manager. A non-positive value inherits the Manager's TTL.
func WithCallTTL(ttl time.Duration) DoOption {
	return func(o *doOptions) {
		if ttl > 0 {
			o.ttl = &ttl
		}
	}
}

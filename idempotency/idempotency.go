package idempotency

import (
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/distributedlock"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

const (
	// DefaultTTL is how long a completed record is replayable for.
	DefaultTTL = 24 * time.Hour
	// DefaultInFlightTTL bounds how long a claim survives without being
	// completed. It must exceed the worst-case duration of the work being
	// guarded — see the package documentation on choosing it.
	DefaultInFlightTTL = 2 * time.Minute
	// DefaultMaxKeyLength is the longest key accepted, matching the limit
	// Stripe publishes for the same header.
	DefaultMaxKeyLength = 255
	// DefaultKeyPrefix namespaces both the store and lock keys, so an
	// idempotency key cannot collide with an unrelated entry in a cache or
	// locker shared with something else.
	DefaultKeyPrefix = "idempotency:"

	// serviceName names the loggers, spans, and metrics this package emits.
	serviceName = "idempotency"

	// recordVersion stamps every record written. A deploy that changes the
	// shape of T (or of Record itself) bumps it, and records written by the
	// previous shape are then ignored rather than misread. Without it a
	// gob-decoded record from an older binary would surface as a plausible
	// but wrong replay.
	recordVersion = 1
)

// State is the lifecycle stage of a record.
type State uint8

const (
	// StateInFlight marks a claim: work has started and has not reported back.
	StateInFlight State = iota + 1
	// StateCompleted marks a finished result that is safe to replay.
	StateCompleted
)

// StoreFailurePolicy decides what happens when the record store cannot be
// read. It is the most consequential setting in this package, because the two
// answers fail in opposite directions.
type StoreFailurePolicy uint8

const (
	// FailClosed refuses the request when the store is unreachable. The
	// default, and the right answer whenever the guarded work costs money: a
	// brief outage becomes downtime rather than duplicate charges.
	FailClosed StoreFailurePolicy = iota
	// FailOpen runs the work anyway, trading the guarantee for availability.
	// Appropriate only where a duplicate effect is cheaper than a rejection.
	FailOpen
)

type (
	// Record is what the store holds for a key. It is written twice per
	// execution: once to claim the key, once to record the outcome.
	//
	// T must be a concrete struct with exported fields — see the package
	// documentation on what the store can round-trip.
	Record[T any] struct {
		// CreatedAt is when this revision of the record was written.
		CreatedAt time.Time
		// Value is the recorded result, set only once State is
		// StateCompleted.
		Value *T
		// Fingerprint identifies the request this key was used for, so a
		// second, different request under the same key can be detected.
		Fingerprint Fingerprint
		// ClaimID identifies the execution that owns the claim. Only its owner
		// may complete or release it, which is what stops an execution that
		// outlived its claim from overwriting whoever re-claimed the key.
		ClaimID string
		// Version is the record shape this was written with.
		Version int
		// State is the lifecycle stage.
		State State
	}

	// Result is the outcome of Do.
	Result[T any] struct {
		// Value is the result of the work, whether it just ran or was
		// replayed.
		Value *T
		// Replayed reports whether Value came from a stored record rather
		// than from running the work.
		Replayed bool
	}

	// Manager runs work at most once per key.
	//
	// It is a concrete type rather than an interface: there is one
	// implementation, and the seams worth swapping — the store and the locker
	// — are already interfaces with their own mocks.
	Manager[T any] struct {
		store  cache.Cache[Record[T]]
		locker distributedlock.ScopedLocker
		o11y   observability.Observer
		clock  clock.Clock

		recordable func(*T) bool

		requestCounter       metrics.Int64Counter
		claimLostCounter     metrics.Int64Counter
		recordFailureCounter metrics.Int64Counter
		storeErrorCounter    metrics.Int64Counter
		staleRecordCounter   metrics.Int64Counter
		latencyHist          metrics.Float64Histogram

		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		keyPrefix          string
		ttl                time.Duration
		inFlightTTL        time.Duration
		maxKeyLength       int
		storeFailurePolicy StoreFailurePolicy
	}
)

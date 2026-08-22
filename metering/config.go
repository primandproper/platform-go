package metering

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// MaxIdempotencyKeyLength bounds an ingest idempotency key, matching the
	// limit Stripe publishes for the same header and the one the idempotency
	// package uses. It is the width of the key column, so raising it here without
	// a migration produces a truncation error rather than a longer key.
	MaxIdempotencyKeyLength = 255

	// DefaultStaleness is how far behind Enforcer.Check may be when a meter names
	// no budget of its own.
	//
	// Ten seconds. The number is a trade between two costs, and neither is
	// symmetric. Longer, and a subject at their limit keeps getting allowed for
	// the length of the budget — bounded overage, at the meter's unit price.
	// Shorter, and Check degenerates into a durable read on every request, which
	// is the thing it exists not to be. Ten seconds keeps the worst case to
	// whatever one subject can consume in ten seconds, which for anything worth
	// metering is a rounding error against a monthly bill.
	DefaultStaleness = 10 * time.Second

	// DefaultCachePrefix namespaces the enforcer's cache keys, so a total cannot
	// collide with an unrelated entry in a cache shared with something else.
	DefaultCachePrefix = "metering:"

	// DefaultBatchSize is how many usage records one Record call folds per
	// statement round.
	DefaultBatchSize = 100

	// DefaultFlushInterval is the recommended cadence for running the Flusher.
	//
	// It is a suggestion for the caller's scheduler, not something this package
	// acts on: the Flusher has no ticker of its own and does one pass per call.
	// It was previously also a config field, which read as though setting it made
	// the Flusher run on that interval — nothing ever did.
	DefaultFlushInterval = 5 * time.Minute

	// DefaultFlushBatchSize is how many subject-meter-period totals one flush
	// pass claims.
	DefaultFlushBatchSize = 100

	// DefaultFlushConcurrency is how many claimed totals are posted at once. Each
	// post is one provider API call, and providers rate-limit.
	DefaultFlushConcurrency = 4

	// DefaultFlushLeaseDuration is how long a claimed total stays leased to one
	// flusher. It must exceed DefaultFlushTimeout, or a second flusher starts
	// posting a total the first is still posting.
	DefaultFlushLeaseDuration = 5 * time.Minute

	// DefaultFlushTimeout bounds one provider post.
	DefaultFlushTimeout = 30 * time.Second

	// DefaultMaxFlushAttempts is how many times a total is re-posted before the
	// flusher gives up on it and leaves it for a human.
	//
	// A total that has failed this many times is not failing transiently — it is
	// a customer that was deleted, or a meter that no longer exists at the
	// provider. Retrying it forever costs a provider API call every interval and
	// buries the totals that would succeed.
	DefaultMaxFlushAttempts = 10

	// DefaultEventRetention is how long a usage event row is kept after its
	// period was flushed.
	//
	// The event ledger is what makes ingest idempotent, so it must outlive every
	// retry that could present the same key again — a queue's dead-letter
	// redelivery, a batch reprocessed by hand — and it is the only record of what
	// a total is made of when a customer disputes an invoice. Ninety days covers
	// a billing period, the dispute window after it, and the month somebody takes
	// to get round to asking.
	DefaultEventRetention = 90 * 24 * time.Hour

	// DefaultReapBatchSize caps how many event rows one retention pass deletes.
	DefaultReapBatchSize = 1000
)

// RecorderConfig configures the ingest path.
type RecorderConfig struct {
	// BatchSize is how many usage records one Record call folds per statement
	// round. Defaults to DefaultBatchSize.
	//
	// It bounds the parameter count of a single statement, which matters:
	// Postgres refuses a statement with more than 65535 bound parameters, and a
	// caller that hands Record ten thousand records would otherwise hit that
	// instead of being chunked.
	BatchSize int `env:"BATCH_SIZE" json:"batchSize,omitempty" yaml:"batchSize,omitempty"`

	// RejectUnknownMeters refuses usage naming an unregistered meter instead of
	// dropping it.
	//
	// The default — false — drops the record and counts it, because the failure
	// this guards against is asymmetric. A deploy that adds a meter reaches the
	// ingest path before it reaches the wiring on some replica somewhere, and a
	// Record that returns an error for a meter the next replica knows about turns
	// a rollout into an outage on the path that was supposed to be cheap. An
	// operator who would rather find out loudly sets this.
	RejectUnknownMeters bool `env:"REJECT_UNKNOWN_METERS" json:"rejectUnknownMeters,omitempty" yaml:"rejectUnknownMeters,omitempty"`
}

var _ validation.ValidatableWithContext = (*RecorderConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *RecorderConfig) EnsureDefaults() {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
}

// ValidateWithContext validates a RecorderConfig.
func (cfg *RecorderConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.BatchSize, validation.Required, validation.Min(1)),
	)
}

// EnforcerConfig configures the read path.
type EnforcerConfig struct {
	// CachePrefix namespaces cache keys. Defaults to DefaultCachePrefix.
	CachePrefix string `env:"CACHE_PREFIX" json:"cachePrefix,omitempty" yaml:"cachePrefix,omitempty"`

	// Staleness is the default staleness budget for Check, used by meters that
	// name none of their own. Defaults to DefaultStaleness.
	Staleness time.Duration `env:"STALENESS" json:"staleness,omitempty" yaml:"staleness,omitempty"`

	// FailOpen decides what Check does when the durable store cannot be read and
	// the cache has nothing.
	//
	// The default — false, fail closed — refuses. It is the right answer whenever
	// the quota guards something that costs money, because an outage that lets
	// every subject past every limit is an outage that bills the operator rather
	// than the customer.
	//
	// It is also the answer that will take a service down when its database
	// blinks, which is why the other one exists. A quota protecting a shared
	// dependency from a noisy neighbor is better off allowing traffic through an
	// outage than adding one of its own. Consume is unaffected either way: an
	// exact answer has nowhere to fail open to.
	FailOpen bool `env:"FAIL_OPEN" json:"failOpen,omitempty" yaml:"failOpen,omitempty"`
}

var _ validation.ValidatableWithContext = (*EnforcerConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *EnforcerConfig) EnsureDefaults() {
	if cfg.Staleness <= 0 {
		cfg.Staleness = DefaultStaleness
	}

	if cfg.CachePrefix == "" {
		cfg.CachePrefix = DefaultCachePrefix
	}
}

// ValidateWithContext validates an EnforcerConfig.
func (cfg *EnforcerConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Staleness, validation.Required),
	)
}

// FlusherConfig configures the push of accumulated usage to the billing
// provider.
type FlusherConfig struct {
	// Backoff schedules the retry of a total whose post failed.
	Backoff retrycfg.Config `env:",init" envPrefix:"BACKOFF_" json:"backoff,omitzero" yaml:"backoff,omitempty"`

	// LeaseDuration is how long a claimed total stays leased. It must exceed
	// FlushTimeout — see ValidateWithContext.
	LeaseDuration time.Duration `env:"LEASE_DURATION" json:"leaseDuration,omitempty" yaml:"leaseDuration,omitempty"`

	// FlushTimeout bounds one provider post.
	FlushTimeout time.Duration `env:"FLUSH_TIMEOUT" json:"flushTimeout,omitempty" yaml:"flushTimeout,omitempty"`

	// EventRetention is how long a flushed period's usage events are kept.
	// Defaults to DefaultEventRetention.
	EventRetention time.Duration `env:"EVENT_RETENTION" json:"eventRetention,omitempty" yaml:"eventRetention,omitempty"`

	// BatchSize is how many totals one flush pass claims.
	BatchSize int `env:"BATCH_SIZE" json:"batchSize,omitempty" yaml:"batchSize,omitempty"`

	// Concurrency is how many claimed totals are posted at once.
	Concurrency int `env:"CONCURRENCY" json:"concurrency,omitempty" yaml:"concurrency,omitempty"`

	// MaxAttempts is how many times a total is re-posted before the flusher gives
	// up. Defaults to DefaultMaxFlushAttempts.
	MaxAttempts int `env:"MAX_ATTEMPTS" json:"maxAttempts,omitempty" yaml:"maxAttempts,omitempty"`

	// ReapBatchSize caps how many event rows one retention pass deletes.
	ReapBatchSize int `env:"REAP_BATCH_SIZE" json:"reapBatchSize,omitempty" yaml:"reapBatchSize,omitempty"`

	// DisableReap stops the flusher deleting event rows past retention.
	//
	// It exists because the event ledger is the evidence behind an invoice, and
	// how long that evidence must be kept is a jurisdiction's answer rather than
	// a library's. An operator whose answer is "forever" should be able to say so
	// without setting a retention of a hundred years.
	DisableReap bool `env:"DISABLE_REAP" json:"disableReap,omitempty" yaml:"disableReap,omitempty"`
}

var _ validation.ValidatableWithContext = (*FlusherConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *FlusherConfig) EnsureDefaults() {
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = DefaultFlushLeaseDuration
	}
	if cfg.FlushTimeout <= 0 {
		cfg.FlushTimeout = DefaultFlushTimeout
	}
	if cfg.EventRetention <= 0 {
		cfg.EventRetention = DefaultEventRetention
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultFlushBatchSize
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultFlushConcurrency
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxFlushAttempts
	}
	if cfg.ReapBatchSize <= 0 {
		cfg.ReapBatchSize = DefaultReapBatchSize
	}

	cfg.Backoff.EnsureDefaults()
}

// ValidateWithContext validates a FlusherConfig.
func (cfg *FlusherConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.FlushTimeout, validation.Required),
		validation.Field(&cfg.EventRetention, validation.Required),
		validation.Field(&cfg.BatchSize, validation.Required, validation.Min(1)),
		validation.Field(&cfg.Concurrency, validation.Required, validation.Min(1)),
		validation.Field(&cfg.MaxAttempts, validation.Required, validation.Min(1)),
		validation.Field(&cfg.ReapBatchSize, validation.Required, validation.Min(1)),
		validation.Field(&cfg.LeaseDuration, validation.Required, validation.By(func(any) error {
			// A lease that expires while the post it covers is still in flight is
			// not a lease. Two flushers posting the same total concurrently is the
			// duplicate charge this package spends most of its complexity
			// preventing, and it would arrive through the one path the
			// idempotency key cannot cover — two different sequence numbers for
			// the same delta.
			if cfg.LeaseDuration <= cfg.FlushTimeout {
				return platformerrors.Newf(
					"metering flush lease duration %s must exceed flush timeout %s",
					cfg.LeaseDuration, cfg.FlushTimeout,
				)
			}

			return nil
		})),
	)
}

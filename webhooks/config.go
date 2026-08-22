package webhooks

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// DefaultBatchSize is how many dispatches one cycle claims.
	DefaultBatchSize = 100
	// DefaultConcurrency is how many deliveries run at once within a batch.
	DefaultConcurrency = 16
	// DefaultPollInterval is how often the worker looks for work.
	DefaultPollInterval = time.Second
	// DefaultLeaseDuration is how long a claim is held before another worker
	// may reclaim the dispatch. It must comfortably exceed RequestTimeout, or
	// two workers will deliver the same payload concurrently.
	DefaultLeaseDuration = 60 * time.Second
	// DefaultRequestTimeout bounds one delivery request.
	DefaultRequestTimeout = 10 * time.Second
	// DefaultCircuitOpenRetryDelay is how long a dispatch waits after being
	// short-circuited.
	DefaultCircuitOpenRetryDelay = 30 * time.Second
	// DefaultRetention is how long delivered dispatches and their attempts are
	// kept before reaping.
	DefaultRetention = 7 * 24 * time.Hour
	// DefaultReapInterval is how often the reaper runs.
	DefaultReapInterval = 5 * time.Minute
	// DefaultReapBatchSize caps one reap, so a large backlog is removed over
	// several passes instead of one long-running DELETE.
	DefaultReapBatchSize = 1000
	// DefaultUserAgent identifies deliveries to subscribers.
	DefaultUserAgent = "platform-go-webhooks/1"
)

// WorkerConfig configures a Worker.
type WorkerConfig struct {
	// UserAgent identifies deliveries to subscribers.
	UserAgent string `env:"USER_AGENT" json:"userAgent,omitempty" yaml:"userAgent,omitempty"`
	// Backoff drives the retry schedule for failed deliveries. MaxAttempts is
	// the threshold past which a dispatch is marked dead.
	Backoff retrycfg.Config `envPrefix:"BACKOFF_" json:"backoff,omitzero" yaml:"backoff,omitempty"`
	// PollInterval is how often the worker looks for work.
	PollInterval time.Duration `env:"POLL_INTERVAL" json:"pollInterval,omitempty" yaml:"pollInterval,omitempty"`
	// LeaseDuration is how long a claim is held before it can be reclaimed.
	LeaseDuration time.Duration `env:"LEASE_DURATION" json:"leaseDuration,omitempty" yaml:"leaseDuration,omitempty"`
	// RequestTimeout bounds one delivery request.
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" json:"requestTimeout,omitempty" yaml:"requestTimeout,omitempty"`
	// CircuitOpenRetryDelay is how long a short-circuited dispatch waits before
	// becoming claimable again. It is a flat delay rather than a backoff step,
	// because backing off exponentially against an open circuit means the first
	// delivery after recovery can wait far longer than the outage did.
	CircuitOpenRetryDelay time.Duration `env:"CIRCUIT_OPEN_RETRY_DELAY" json:"circuitOpenRetryDelay,omitempty" yaml:"circuitOpenRetryDelay,omitempty"`
	// Retention is how long delivered dispatches are kept before reaping.
	Retention time.Duration `env:"RETENTION" json:"retention,omitempty" yaml:"retention,omitempty"`
	// ReapInterval is how often the reaper runs.
	ReapInterval time.Duration `env:"REAP_INTERVAL" json:"reapInterval,omitempty" yaml:"reapInterval,omitempty"`
	// BatchSize is how many dispatches one cycle claims.
	BatchSize int `env:"BATCH_SIZE" json:"batchSize,omitempty" yaml:"batchSize,omitempty"`
	// Concurrency is how many deliveries run at once within a batch.
	Concurrency int `env:"CONCURRENCY" json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
	// ReapBatchSize caps how many rows one reap deletes.
	ReapBatchSize int `env:"REAP_BATCH_SIZE" json:"reapBatchSize,omitempty" yaml:"reapBatchSize,omitempty"`
}

var _ validation.ValidatableWithContext = (*WorkerConfig)(nil)

// ErrLeaseTooShort indicates a lease that does not outlast a request, which
// would let two workers deliver the same payload concurrently.
var ErrLeaseTooShort = platformerrors.New("webhooks lease duration must exceed the request timeout")

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *WorkerConfig) EnsureDefaults() {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = DefaultRequestTimeout
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = DefaultLeaseDuration
	}
	if cfg.CircuitOpenRetryDelay <= 0 {
		cfg.CircuitOpenRetryDelay = DefaultCircuitOpenRetryDelay
	}
	if cfg.Retention <= 0 {
		cfg.Retention = DefaultRetention
	}
	if cfg.ReapInterval <= 0 {
		cfg.ReapInterval = DefaultReapInterval
	}
	if cfg.ReapBatchSize <= 0 {
		cfg.ReapBatchSize = DefaultReapBatchSize
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}

	cfg.Backoff.EnsureDefaults()
}

// ValidateWithContext validates a WorkerConfig.
func (cfg *WorkerConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.UserAgent, validation.Required),
		validation.Field(&cfg.BatchSize, validation.Required, validation.Min(1)),
		validation.Field(&cfg.Concurrency, validation.Required, validation.Min(1)),
		validation.Field(&cfg.PollInterval, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.RequestTimeout, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.CircuitOpenRetryDelay, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.Retention, validation.Required, validation.Min(time.Minute)),
		validation.Field(&cfg.ReapInterval, validation.Required, validation.Min(time.Second)),
		validation.Field(&cfg.ReapBatchSize, validation.Required, validation.Min(1)),
		// The lease has to outlast the request it covers. A shorter one expires
		// while the request is still in flight, a second worker reclaims the
		// dispatch, and the subscriber receives the same payload twice from two
		// workers at once — which at-least-once permits but nothing here
		// intended.
		validation.Field(&cfg.LeaseDuration, validation.Required, validation.Min(time.Second), validation.By(func(any) error {
			if cfg.LeaseDuration <= cfg.RequestTimeout {
				return platformerrors.Wrapf(ErrLeaseTooShort, "lease %s, request timeout %s", cfg.LeaseDuration, cfg.RequestTimeout)
			}

			return nil
		})),
	)
}

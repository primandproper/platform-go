package outbox

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// DefaultBatchSize is how many messages one cycle claims.
	DefaultBatchSize = 100
	// DefaultPollInterval is how often the relay looks for work.
	DefaultPollInterval = time.Second
	// DefaultLeaseDuration is how long a claim is held before another relay may
	// reclaim the message. It must comfortably exceed the time to publish a
	// batch, or two relays will publish the same message concurrently.
	DefaultLeaseDuration = 30 * time.Second
	// DefaultRetention is how long published rows are kept before reaping.
	DefaultRetention = 24 * time.Hour
	// DefaultReapInterval is how often the reaper runs.
	DefaultReapInterval = 5 * time.Minute
	// DefaultReapBatchSize caps one reap, so a large backlog is removed over
	// several passes instead of one long-running DELETE.
	DefaultReapBatchSize = 1000
	// DefaultMinWakeInterval is the floor between two wake-driven cycles. It
	// only bites during a burst: a wake that arrives when the last cycle is
	// already older than this runs immediately, which is the ordinary case and
	// the whole point of a wakeup.
	DefaultMinWakeInterval = 100 * time.Millisecond
)

// ClaimMode selects how the relay takes ownership of messages.
type ClaimMode string

const (
	// ClaimSkipLocked claims with FOR UPDATE SKIP LOCKED, so several relays can
	// run at once without contending. Requires Postgres or MySQL.
	ClaimSkipLocked ClaimMode = "skip_locked"
	// ClaimLease claims with a lease alone. Correct everywhere — and the only
	// option on SQLite — and the right choice when a single relay is running.
	ClaimLease ClaimMode = "lease"
)

// Valid reports whether m is a known claim mode.
func (m ClaimMode) Valid() bool {
	return m == ClaimSkipLocked || m == ClaimLease
}

// RelayConfig configures a Relay.
//
// There is deliberately no Dialect field. The SQL a relay emits has to match
// the database it runs against, and a config that names the dialect separately
// makes the mismatch representable — so NewRelay reads it from the
// database.Client instead, which is the one thing that cannot be wrong about
// its own dialect.
type RelayConfig struct {
	// TablePrefix is the namespace the outbox table carries. Empty renders
	// outbox_messages; "ddb" renders ddb_outbox_messages.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
	// table is TablePrefix resolved to a full name, filled by EnsureDefaults so
	// every query builder below reads one already-qualified string.
	table string
	// ClaimMode selects lease-only or SKIP LOCKED claiming.
	ClaimMode ClaimMode `env:"CLAIM_MODE" json:"claimMode,omitempty" yaml:"claimMode,omitempty"`
	// NotifyChannel makes the Writer emit a payload-free pg_notify on this
	// channel inside the caller's transaction, so a relay listening on it wakes
	// the moment the enqueue commits instead of on the next poll.
	//
	// Empty — the default — emits nothing at all, and the SQL running inside
	// every caller's transaction is unchanged. It is Postgres-only, and a
	// channel configured on any other dialect is refused at construction rather
	// than ignored.
	//
	// The Relay does not read this. Nothing in this package speaks LISTEN: a
	// wakeup arrives as a bare channel through WithRelayWakeup, which
	// database/postgres/pgnotify is one way to fill.
	NotifyChannel string `env:"NOTIFY_CHANNEL" json:"notifyChannel,omitempty" yaml:"notifyChannel,omitempty"`
	// Backoff drives the retry schedule for messages that fail to publish.
	// MaxAttempts is the quarantine threshold.
	Backoff retrycfg.Config `envPrefix:"BACKOFF_" json:"backoff,omitzero" yaml:"backoff,omitempty"`
	// BatchSize is how many messages one cycle claims.
	BatchSize int `env:"BATCH_SIZE" json:"batchSize,omitempty" yaml:"batchSize,omitempty"`
	// PollInterval is how often the relay looks for work.
	PollInterval time.Duration `env:"POLL_INTERVAL" json:"pollInterval,omitempty" yaml:"pollInterval,omitempty"`
	// LeaseDuration is how long a claim is held before it can be reclaimed.
	LeaseDuration time.Duration `env:"LEASE_DURATION" json:"leaseDuration,omitempty" yaml:"leaseDuration,omitempty"`
	// Retention is how long published rows are kept before reaping.
	Retention time.Duration `env:"RETENTION" json:"retention,omitempty" yaml:"retention,omitempty"`
	// ReapInterval is how often the reaper runs.
	ReapInterval time.Duration `env:"REAP_INTERVAL" json:"reapInterval,omitempty" yaml:"reapInterval,omitempty"`
	// ReapBatchSize caps how many rows one reap deletes.
	ReapBatchSize int `env:"REAP_BATCH_SIZE" json:"reapBatchSize,omitempty" yaml:"reapBatchSize,omitempty"`
	// MinWakeInterval floors the rate of wake-driven cycles, so that a table
	// taking thousands of inserts a second cannot drive thousands of relay
	// cycles a second. It is inert without WithRelayWakeup.
	MinWakeInterval time.Duration `env:"MIN_WAKE_INTERVAL" json:"minWakeInterval,omitempty" yaml:"minWakeInterval,omitempty"`
}

var _ validation.ValidatableWithContext = (*RelayConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
//
// The claim mode defaults to SKIP LOCKED here and is narrowed later by
// resolveForDialect, once NewRelay has read the dialect off the client: a
// dialect without SKIP LOCKED is forced to ClaimLease.
func (cfg *RelayConfig) EnsureDefaults() {
	cfg.table = tableFor(cfg.TablePrefix)
	if cfg.ClaimMode == "" {
		cfg.ClaimMode = ClaimSkipLocked
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = DefaultLeaseDuration
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
	if cfg.MinWakeInterval <= 0 {
		cfg.MinWakeInterval = DefaultMinWakeInterval
	}

	cfg.Backoff.EnsureDefaults()
}

// ValidateWithContext validates a RelayConfig.
func (cfg *RelayConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.ClaimMode, validation.Required, validation.By(func(any) error {
			if !cfg.ClaimMode.Valid() {
				return platformerrors.Wrapf(ErrInvalidClaimMode, "claim mode %q", cfg.ClaimMode)
			}

			return nil
		})),
		validation.Field(&cfg.BatchSize, validation.Required, validation.Min(1)),
		validation.Field(&cfg.PollInterval, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.LeaseDuration, validation.Required, validation.Min(time.Second)),
		validation.Field(&cfg.Retention, validation.Required, validation.Min(time.Minute)),
		validation.Field(&cfg.ReapInterval, validation.Required, validation.Min(time.Second)),
		validation.Field(&cfg.ReapBatchSize, validation.Required, validation.Min(1)),
		validation.Field(&cfg.MinWakeInterval, validation.Required, validation.Min(time.Millisecond)),
	)
}

// resolveForDialect narrows the claim mode to what d can actually do, and
// reports a dialect this package cannot emit SQL for.
//
// It is separate from EnsureDefaults because the dialect is not the config's to
// know: NewRelay reads it from the database.Client and applies it here.
func (cfg *RelayConfig) resolveForDialect(d dialect.Dialect) error {
	if !d.Valid() {
		return platformerrors.Wrapf(dialect.ErrUnsupported, "outbox dialect %q", d)
	}

	// Only the SKIP LOCKED mode is downgraded. Rewriting any other value would
	// also rewrite a typo, hiding it from the validation that runs next.
	if cfg.ClaimMode == ClaimSkipLocked && !d.SupportsSkipLocked() {
		cfg.ClaimMode = ClaimLease
	}

	return nil
}

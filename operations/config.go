package operations

import (
	"context"
	"time"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// DefaultTablePrefix is the namespace the operations table carries when none
	// is configured, which is none — rendering operations.
	//
	// The table's own name is the schema's, not the caller's: a table always
	// says which package created it. Setting a namespace of "app" renders
	// app_operations, for a database shared between applications. A namespace
	// must not end in '_'; database/ddl supplies the separator.
	DefaultTablePrefix = ""

	// DefaultQueueName is the logical workqueue an operation's key is enqueued
	// on when none is configured.
	DefaultQueueName = "operations"

	// DefaultRetention is how long a terminal operation is kept before Reap may
	// delete it.
	//
	// It is long. The row is the receipt for a piece of work somebody asked for
	// and waited on, and "did that export actually finish, and when" is a
	// question asked days later by somebody holding a support ticket. It is also
	// the only record of a failure, once the logs have rolled.
	DefaultRetention = 30 * 24 * time.Hour

	// DefaultReapBatchSize caps one reap, so a long-neglected table is drained
	// over several passes instead of one long-running DELETE.
	DefaultReapBatchSize = 1000

	// DefaultRecoverAfter is how long an operation may sit pending before
	// Recover assumes its enqueue was lost and re-offers it.
	//
	// It is not zero, and the margin is the point: an operation is pending for a
	// perfectly ordinary reason between Start's insert and its enqueue, and a
	// sweep with no grace period would re-enqueue every operation the fleet
	// starts, at the exact moment the fleet is busiest starting them.
	DefaultRecoverAfter = time.Minute

	// DefaultRecoverBatchSize caps one recovery sweep.
	DefaultRecoverBatchSize = 200

	// MaxRequestBytes bounds an encoded request. A request describes work; a
	// request that is itself megabytes is data, and data belongs in the store
	// the work will read it from.
	MaxRequestBytes = 64 * 1024

	// MaxResultDetailBytes bounds Result.Detail. It is smaller than a request
	// because a result is read on every poll of a finished operation, where a
	// request is written once.
	MaxResultDetailBytes = 16 * 1024

	// MaxMessageLength bounds Progress.Message and Error.Message. Both are
	// rendered into a fixed-width column and both are read by humans, so an
	// over-long one is truncated rather than refused — losing the tail of a
	// message is not worth failing an operation that otherwise worked.
	MaxMessageLength = 1024
)

// Config configures the operations store and the service over it.
//
// There is deliberately no Dialect field. The SQL has to match the database it
// runs against, so the constructors read the dialect off the database.Client —
// the one thing that cannot be wrong about its own dialect.
type Config struct {
	// TablePrefix is the namespace the operations table carries. Empty renders
	// operations; "app" renders app_operations. It must match the namespace the
	// migrations were rendered with.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// QueueName is the logical work queue operation keys are enqueued on. It has
	// to match the queue the Worker claims from, which is why it is named here
	// rather than left to whoever builds the queue.
	QueueName string `env:"QUEUE_NAME" json:"queueName,omitempty" yaml:"queueName,omitempty"`

	// NotifyChannel makes every write to an operation row emit a payload-free
	// pg_notify on this channel, so a Watcher listening on it re-reads the row
	// at once instead of on its next poll.
	//
	// This is what turns a subscription from a poll into a push. Without it the
	// watch path still works and still delivers every state the operation passes
	// through — it simply learns about them a poll interval late.
	//
	// Empty — the default — emits nothing at all. It must be a plain SQL
	// identifier: it is bound as text here, but a listener has to render it into
	// a LISTEN, which takes no parameters.
	//
	// Nothing in this package listens. A wakeup arrives as a bare channel
	// through WithWakeup, which database/postgres/pgnotify is one way to fill.
	NotifyChannel string `env:"NOTIFY_CHANNEL" json:"notifyChannel,omitempty" yaml:"notifyChannel,omitempty"`

	// Retention is how long a terminal operation is kept before Reap may delete
	// it. See DefaultRetention for why it is measured in weeks.
	Retention time.Duration `env:"RETENTION" json:"retention,omitempty" yaml:"retention,omitempty"`

	// RecoverAfter is how long an operation may sit pending before Recover
	// re-enqueues it.
	RecoverAfter time.Duration `env:"RECOVER_AFTER" json:"recoverAfter,omitempty" yaml:"recoverAfter,omitempty"`

	// ReapBatchSize caps how many terminal operations one Reap deletes.
	ReapBatchSize int `env:"REAP_BATCH_SIZE" json:"reapBatchSize,omitempty" yaml:"reapBatchSize,omitempty"`

	// RecoverBatchSize caps how many stranded operations one Recover re-offers.
	RecoverBatchSize int `env:"RECOVER_BATCH_SIZE" json:"recoverBatchSize,omitempty" yaml:"recoverBatchSize,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *Config) EnsureDefaults() {
	if cfg.QueueName == "" {
		cfg.QueueName = DefaultQueueName
	}
	if cfg.Retention <= 0 {
		cfg.Retention = DefaultRetention
	}
	if cfg.RecoverAfter <= 0 {
		cfg.RecoverAfter = DefaultRecoverAfter
	}
	if cfg.ReapBatchSize <= 0 {
		cfg.ReapBatchSize = DefaultReapBatchSize
	}
	if cfg.RecoverBatchSize <= 0 {
		cfg.RecoverBatchSize = DefaultRecoverBatchSize
	}
}

// ValidateWithContext validates a Config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.QueueName, validation.Required, validation.Length(1, MaxKindLength)),
		validation.Field(&cfg.Retention, validation.Required, validation.Min(time.Minute)),
		validation.Field(&cfg.RecoverAfter, validation.Required, validation.Min(time.Second)),
		validation.Field(&cfg.ReapBatchSize, validation.Required, validation.Min(1)),
		validation.Field(&cfg.RecoverBatchSize, validation.Required, validation.Min(1)),
	)
}

const (
	// DefaultWorkerPoll is the backstop interval between claim attempts. It is
	// short relative to a timer's because a work queue has no next-due time to
	// sleep until, and long relative to a spin because a wakeup is how a worker
	// is meant to learn there is work.
	DefaultWorkerPoll = 5 * time.Second

	// DefaultWorkerLease is how long a worker holds a claimed operation before
	// the fleet takes it back.
	//
	// It is minutes rather than seconds because the work is long-running by
	// definition — that is the entire premise — and a lease shorter than the
	// work means every operation is reclaimed and run twice. It is nonetheless
	// far shorter than the work is allowed to take: the lease is extended for as
	// long as the Runner keeps reporting progress. See Reporter.
	DefaultWorkerLease = 2 * time.Minute

	// DefaultWorkerBatch is how many operations one pass claims.
	//
	// It is small. Operations are long, so a large batch means the tail of it
	// waits on the head for minutes while other workers sit idle — the opposite
	// of what batching a queue of short work buys.
	DefaultWorkerBatch = 4

	// DefaultWorkerConcurrency is how many claimed operations run at once.
	DefaultWorkerConcurrency = 2

	// DefaultWorkerRetryDelay is how long a failed operation is held back before
	// it is offered again.
	DefaultWorkerRetryDelay = 30 * time.Second

	// DefaultWorkerMaxAttempts is how many times an operation may be claimed
	// before it is failed with CodeAttemptsExhausted.
	//
	// Unlike a work queue's, this defaults to a real ceiling rather than to
	// unlimited, and it must: the promise this package makes is that an
	// operation always reaches a terminal state, and an unlimited budget is
	// exactly the case where it never does. A client polling an operation that
	// will be retried forever is worse served than one told it failed.
	DefaultWorkerMaxAttempts = 5

	// DefaultProgressInterval is how often a Runner's buffered progress is
	// flushed to the row, and therefore also how often a cancellation is
	// noticed and how often the lease is extended.
	DefaultProgressInterval = 2 * time.Second
)

// WorkerConfig configures a Worker.
type WorkerConfig struct {
	// Poll is how long a worker sleeps when it claimed nothing. It is a
	// backstop: with a wakeup wired, a fresh operation is claimed in
	// milliseconds and this only bounds how long a lost wake can delay one.
	Poll time.Duration `env:"POLL" json:"poll,omitempty" yaml:"poll,omitempty"`

	// Lease is how long a claimed operation is held before the fleet may take it
	// back.
	//
	// It does not have to cover the whole operation, which is the difference
	// between this and every other leased loop in the module. A Runner that
	// reports progress extends its own lease as a side effect of each flush, so
	// the bound that matters is not "how long does the work take" but "how long
	// may the work go without saying anything" — which is a property a Runner
	// controls and a lease length cannot guess.
	//
	// A Runner that reports no progress at all is bounded by this and nothing
	// else, and will be reclaimed and run again if it takes longer. That is the
	// case to size this for, or to fix by reporting progress.
	Lease time.Duration `env:"LEASE" json:"lease,omitempty" yaml:"lease,omitempty"`

	// RetryDelay is how long a failed operation is held back before it becomes
	// claimable again. It is a flat delay rather than a backoff curve because
	// MaxAttempts is what bounds a failing operation, and five attempts do not
	// need a curve.
	RetryDelay time.Duration `env:"RETRY_DELAY" json:"retryDelay,omitempty" yaml:"retryDelay,omitempty"`

	// ProgressInterval is how often buffered progress is written to the row.
	//
	// It paces three things at once, which is why there is one knob and not
	// three: how fresh a watching client's view is, how quickly a cancellation
	// is noticed, and how often the lease is extended. Shortening it makes all
	// three more responsive at one statement per operation per interval.
	ProgressInterval time.Duration `env:"PROGRESS_INTERVAL" json:"progressInterval,omitempty" yaml:"progressInterval,omitempty"`

	// Batch is how many operations one pass claims.
	Batch int `env:"BATCH" json:"batch,omitempty" yaml:"batch,omitempty"`

	// Concurrency is how many claimed operations run at once. One means strictly
	// sequential.
	Concurrency int `env:"CONCURRENCY" json:"concurrency,omitempty" yaml:"concurrency,omitempty"`

	// MaxAttempts is how many times an operation may be claimed before it is
	// failed with CodeAttemptsExhausted. A kind may raise or lower it for itself
	// with Definition.MaxAttempts.
	//
	// It cannot be unlimited. See DefaultWorkerMaxAttempts.
	MaxAttempts int `env:"MAX_ATTEMPTS" json:"maxAttempts,omitempty" yaml:"maxAttempts,omitempty"`
}

var _ validation.ValidatableWithContext = (*WorkerConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *WorkerConfig) EnsureDefaults() {
	if cfg.Poll <= 0 {
		cfg.Poll = DefaultWorkerPoll
	}
	if cfg.Lease <= 0 {
		cfg.Lease = DefaultWorkerLease
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = DefaultWorkerRetryDelay
	}
	if cfg.ProgressInterval <= 0 {
		cfg.ProgressInterval = DefaultProgressInterval
	}
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultWorkerBatch
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultWorkerConcurrency
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultWorkerMaxAttempts
	}
}

// ValidateWithContext validates a WorkerConfig.
//
// ProgressInterval is required to be shorter than Lease, which is the one
// cross-field rule worth enforcing: a flush is what extends the lease, so an
// interval longer than the lease guarantees that every operation reporting
// progress is reclaimed by somebody else between flushes and run twice.
func (cfg *WorkerConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Poll, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.Lease, validation.Required, validation.Min(time.Second)),
		validation.Field(&cfg.RetryDelay, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.ProgressInterval, validation.Required,
			validation.Min(100*time.Millisecond), validation.Max(cfg.Lease/2)),
		validation.Field(&cfg.Batch, validation.Required, validation.Min(1)),
		validation.Field(&cfg.Concurrency, validation.Required, validation.Min(1)),
		validation.Field(&cfg.MaxAttempts, validation.Required, validation.Min(1)),
	)
}

const (
	// DefaultWatcherPoll is how often a Watcher re-reads its subscribed
	// operations without a wakeup.
	//
	// It is the whole of the watch path's latency when no notify channel is
	// configured, and the backstop when one is. Two seconds is the number a
	// person watching a progress bar does not notice.
	DefaultWatcherPoll = 2 * time.Second

	// DefaultWatcherMaxSubscriptions bounds how many operations one Watcher
	// follows at once.
	//
	// Every subscription is a row in the re-read a wake triggers, so this is
	// what stops one client's reconnect loop from turning each notification into
	// an unbounded query.
	DefaultWatcherMaxSubscriptions = 1024

	// DefaultWatcherMinReadInterval floors how often the watch loop may query,
	// however many wakes arrive. It is the anti-spin guard: a busy fleet writing
	// progress emits a notification per flush per operation, and without a floor
	// a watcher would issue a read for every one of them.
	DefaultWatcherMinReadInterval = 250 * time.Millisecond
)

// WatcherConfig configures a Watcher.
type WatcherConfig struct {
	// Poll is how often subscribed operations are re-read without a wakeup, and
	// the backstop interval when there is one. A notification is at-most-once
	// and connection-scoped, so a listener that reconnects has missed
	// everything sent while it was away — this is what makes that a latency
	// problem rather than a correctness one.
	Poll time.Duration `env:"POLL" json:"poll,omitempty" yaml:"poll,omitempty"`

	// MinReadInterval floors how often the loop may query, however many wakes
	// arrive.
	MinReadInterval time.Duration `env:"MIN_READ_INTERVAL" json:"minReadInterval,omitempty" yaml:"minReadInterval,omitempty"`

	// MaxSubscriptions bounds how many operations one Watcher follows at once.
	// Watch returns ErrTooManyWatchers past it.
	MaxSubscriptions int `env:"MAX_SUBSCRIPTIONS" json:"maxSubscriptions,omitempty" yaml:"maxSubscriptions,omitempty"`
}

var _ validation.ValidatableWithContext = (*WatcherConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *WatcherConfig) EnsureDefaults() {
	if cfg.Poll <= 0 {
		cfg.Poll = DefaultWatcherPoll
	}
	if cfg.MinReadInterval <= 0 {
		cfg.MinReadInterval = DefaultWatcherMinReadInterval
	}
	if cfg.MaxSubscriptions <= 0 {
		cfg.MaxSubscriptions = DefaultWatcherMaxSubscriptions
	}
}

// ValidateWithContext validates a WatcherConfig.
func (cfg *WatcherConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Poll, validation.Required, validation.Min(100*time.Millisecond)),
		validation.Field(&cfg.MinReadInterval, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.MaxSubscriptions, validation.Required, validation.Min(1)),
	)
}

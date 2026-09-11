package workqueue

import (
	"context"
	"math"
	"time"

	"github.com/primandproper/platform-go/v14/workqueue/internal/queries"

	"github.com/primandproper/primitives-go/v2/database/ddl"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// DefaultTablePrefix is the namespace the queue table carries when none is
	// configured, which is none — rendering work_queue_items.
	//
	// The work_queue_ segment is the schema's, not the caller's: a table always
	// says which package created it. Setting a namespace of "ddb" renders
	// ddb_work_queue_items, for a database shared between applications.
	DefaultTablePrefix = ""

	// DefaultMaxClaimBatch caps a single Claim. It is a guard, not a target: an
	// unbounded claim on a deep queue leases more work than the caller can
	// finish inside the lease, so the excess is reclaimed by somebody else and
	// done twice.
	DefaultMaxClaimBatch = 500

	// DefaultRetention is how long a completed item is kept before reaping.
	DefaultRetention = 24 * time.Hour

	// DefaultReapBatchSize caps one reap, so a long-neglected queue is drained
	// over several passes instead of one long-running DELETE.
	DefaultReapBatchSize = 1000

	// DefaultWriteAttempts is how many times a writer re-runs a statement
	// Postgres asked it to retry. Ordered locking makes a deadlock between two
	// of this package's writers impossible; this covers the residual case where
	// something else in the consumer's schema touches these rows.
	DefaultWriteAttempts = 3

	// DefaultMinWakeInterval is the floor between two wake-driven returns from
	// Wait. It only bites during a burst: a wake arriving when the last one is
	// already older than this is served immediately, which is the ordinary case
	// and the whole point of a wakeup.
	DefaultMinWakeInterval = 100 * time.Millisecond
)

// Config configures a Queue.
//
// There is deliberately no Dialect field, and no clock. The SQL has to match the
// database it runs against, so New reads the dialect off the database.Client —
// the one thing that cannot be wrong about its own dialect — and every timestamp
// that governs scheduling comes from that database's now().
type Config struct {
	// Name is the logical queue. It is required and has no default: one table
	// holds every queue in the database, partitioned by this column, so an
	// unnamed queue would quietly share rows with every other unnamed one.
	//
	// It is bound as a parameter, never interpolated, so it is free-form text
	// rather than an SQL identifier.
	Name string `env:"NAME" json:"name,omitempty" yaml:"name,omitempty"`

	// TablePrefix is the namespace the queue table carries. Empty renders
	// work_queue_items; "ddb" renders ddb_work_queue_items. It must match the
	// namespace the migrations were rendered with.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// table is TablePrefix resolved to a full name, filled by EnsureDefaults so
	// the identifier check has one already-qualified string to vet. Nothing
	// interpolates it into a statement: the prefix reaches the generated querier
	// on its own, once, at construction.
	table string

	// NotifyChannel makes Enqueue emit a payload-free pg_notify on this channel
	// after the rows land, so a claim loop listening on it wakes at once
	// instead of on its next poll.
	//
	// Empty — the default — emits nothing at all, and the enqueue path runs
	// exactly the statements it always did. It must be a plain SQL identifier:
	// it is bound as text here, but a listener has to render it into a LISTEN,
	// which takes no parameters.
	//
	// Nothing in this package listens. A wakeup arrives as a bare channel
	// through WithWakeup, which database/postgres/pgnotify is one way to fill.
	NotifyChannel string `env:"NOTIFY_CHANNEL" json:"notifyChannel,omitempty" yaml:"notifyChannel,omitempty"`

	// Retention is how long a completed item is kept before Reap may delete it.
	Retention time.Duration `env:"RETENTION" json:"retention,omitempty" yaml:"retention,omitempty"`

	// MaxAttempts is how many times an item may be claimed before the queue
	// stops handing it out. Zero — the default — means unlimited, which is the
	// right answer when the work is idempotent and a failure is transient. A
	// negative ceiling is rejected rather than read as a second spelling of
	// unlimited.
	//
	// Set it when an item can be poisonous. Without a ceiling, one key that
	// reliably kills its worker is claimed, half-processed, and reclaimed
	// forever, and because it sorts to the front on every pass it takes the
	// whole queue's throughput with it. Stalled items are counted by
	// Stats.Stalled and excluded from every claim; they are not deleted, so the
	// keys remain available for inspection and a Release resets nothing on its
	// own — an operator re-enqueues them once the cause is fixed.
	MaxAttempts int `env:"MAX_ATTEMPTS" json:"maxAttempts,omitempty" yaml:"maxAttempts,omitempty"`

	// MaxClaimBatch caps how many items one Claim may lease. A larger limit is
	// clamped to it rather than rejected, and a non-positive limit means "as
	// many as allowed".
	MaxClaimBatch int `env:"MAX_CLAIM_BATCH" json:"maxClaimBatch,omitempty" yaml:"maxClaimBatch,omitempty"`

	// ReapBatchSize caps how many completed items one Reap deletes.
	ReapBatchSize int `env:"REAP_BATCH_SIZE" json:"reapBatchSize,omitempty" yaml:"reapBatchSize,omitempty"`

	// WriteAttempts is how many times a writer re-runs a statement that failed
	// with a serialization failure or a deadlock — the two conditions Postgres
	// resolves by asking the caller to try the whole thing again. Anything else
	// is returned on the first failure.
	WriteAttempts uint `env:"WRITE_ATTEMPTS" json:"writeAttempts,omitempty" yaml:"writeAttempts,omitempty"`

	// MinWakeInterval floors the rate at which Wait returns on a wakeup, so a
	// queue taking thousands of enqueues a second cannot drive thousands of
	// claim round trips a second. It is inert without WithWakeup.
	MinWakeInterval time.Duration `env:"MIN_WAKE_INTERVAL" json:"minWakeInterval,omitempty" yaml:"minWakeInterval,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
//
// MaxAttempts is not among them: zero is a meaningful value there, not an unset
// one.
func (cfg *Config) EnsureDefaults() {
	cfg.table = tableFor(cfg.TablePrefix)

	if cfg.Retention <= 0 {
		cfg.Retention = DefaultRetention
	}
	if cfg.MaxClaimBatch <= 0 {
		cfg.MaxClaimBatch = DefaultMaxClaimBatch
	}
	if cfg.ReapBatchSize <= 0 {
		cfg.ReapBatchSize = DefaultReapBatchSize
	}
	if cfg.WriteAttempts == 0 {
		cfg.WriteAttempts = DefaultWriteAttempts
	}
	if cfg.MinWakeInterval <= 0 {
		cfg.MinWakeInterval = DefaultMinWakeInterval
	}
}

// ValidateWithContext validates a Config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Name, validation.Required, validation.Length(1, MaxKeyLength)),
		validation.Field(&cfg.Retention, validation.Required, validation.Min(time.Second)),
		validation.Field(&cfg.MaxAttempts, validation.Min(0)),
		validation.Field(&cfg.MaxClaimBatch, validation.Required, validation.Min(1)),
		validation.Field(&cfg.ReapBatchSize, validation.Required, validation.Min(1)),
		validation.Field(&cfg.WriteAttempts, validation.Required, validation.Min(uint(1))),
		validation.Field(&cfg.MinWakeInterval, validation.Required, validation.Min(time.Millisecond)),
	)
}

// tableFor renders the queue table name under a namespace. It is the one place
// the component segment reaches this package's own code, and it reads it from
// the corpus rather than restating it, so the DDL, the statements and the
// identifier check cannot disagree about what the table is called.
func tableFor(prefix string) string {
	return ddl.Qualify(prefix) + queries.ItemsTable
}

// resolvedTable returns the qualified table name, for the tests and the
// constructor that have to vet it.
func (cfg *Config) resolvedTable() string {
	return cfg.table
}

// attemptCeiling renders MaxAttempts as the bound parameter the predicates
// expect, where a non-positive value means unlimited.
//
// A ceiling that does not fit the attempts column is indistinguishable from
// unlimited anyway, so it saturates rather than reaching Postgres truncated to
// a negative, which would read as "unlimited" and quietly turn the poison-item
// guard off. It saturates at MaxInt32 rather than MaxInt because int32 is the
// width of that column, which also keeps the value in range where int is 32
// bits.
func (cfg *Config) attemptCeiling() int {
	if cfg.MaxAttempts > math.MaxInt32 {
		return math.MaxInt32
	}

	return cfg.MaxAttempts
}

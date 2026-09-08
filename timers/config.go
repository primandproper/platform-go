package timers

import (
	"context"
	"math"
	"time"

	"github.com/primandproper/platform-go/v14/timers/internal/queries"

	"github.com/primandproper/primitives-go/database/ddl"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// DefaultTablePrefix is the namespace the timer table carries when none is
	// configured, which is none — rendering scheduled_timers.
	//
	// The scheduled_ segment is the schema's, not the caller's: a table always
	// says which package created it. Setting a namespace of "ddb" renders
	// ddb_scheduled_timers, for a database shared between applications.
	DefaultTablePrefix = ""

	// DefaultMaxClaimBatch caps a single Claim. It is a guard, not a target: an
	// unbounded claim on a backlog leases more firings than the caller can
	// finish inside the lease, so the excess is reclaimed by somebody else and
	// fired twice.
	DefaultMaxClaimBatch = 200

	// DefaultRetention is how long a fired timer is kept before reaping. It is
	// longer than a work queue's, because "did the expiry actually run, and
	// when" is a question asked days later by somebody holding a support ticket.
	DefaultRetention = 7 * 24 * time.Hour

	// DefaultReapBatchSize caps one reap, so a long-neglected set is drained
	// over several passes instead of one long-running DELETE.
	DefaultReapBatchSize = 1000

	// DefaultMaxAttempts is how many times a timer may be claimed before the set
	// stops handing it out. Unlike a work queue, this defaults to a real
	// ceiling: a timer fires once, so a timer on its twentieth attempt is not
	// busy, it is broken, and the poller that keeps re-leasing it is spending
	// the whole fleet's firing budget on one row.
	DefaultMaxAttempts = 20

	// DefaultWriteAttempts is how many times a writer re-runs a statement
	// Postgres asked it to retry. Ordered locking makes a deadlock between two
	// of this package's writers impossible; this covers the residual case where
	// something else in the consumer's schema touches these rows.
	DefaultWriteAttempts = 3

	// DefaultMinWakeInterval floors how long Wait can sleep, and so how often a
	// claim loop can come back around. It is the anti-spin guard: without it a
	// set holding a due timer that this process cannot claim — another claimer
	// holds it, or it has stalled — would drive a claim round trip as fast as
	// the network allows.
	DefaultMinWakeInterval = 100 * time.Millisecond
)

// Config configures a timer set.
//
// There is deliberately no Dialect field. The SQL has to match the database it
// runs against, so New reads the dialect off the database.Client — the one thing
// that cannot be wrong about its own dialect.
type Config struct {
	// Name is the logical timer set. It is required and has no default: one
	// table holds every set in the database, partitioned by this column, so an
	// unnamed set would quietly share rows with every other unnamed one.
	//
	// It is bound as a parameter, never interpolated, so it is free-form text
	// rather than an SQL identifier.
	Name string `env:"NAME" json:"name,omitempty" yaml:"name,omitempty"`

	// TablePrefix is the namespace the timer table carries. Empty renders
	// scheduled_timers; "ddb" renders ddb_scheduled_timers. It must match the
	// namespace the migrations were rendered with.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// table is TablePrefix resolved to a full name, filled by EnsureDefaults so
	// the identifier check has one already-qualified string to vet. Nothing
	// interpolates it into a statement: the prefix reaches the generated querier
	// on its own, once, at construction.
	table string

	// NotifyChannel makes Schedule emit a payload-free pg_notify on this channel
	// after the rows land, so a poller listening on it re-reads the next due
	// time at once instead of on its next poll.
	//
	// It matters more here than it does for a work queue. A poller sleeps until
	// the nearest timer it knew about, so a timer scheduled for thirty seconds
	// from now, inserted just after a poller went to sleep for an hour, fires an
	// hour late without a notification and on time with one. That is the case
	// this exists for.
	//
	// Empty — the default — emits nothing at all. It must be a plain SQL
	// identifier: it is bound as text here, but a listener has to render it into
	// a LISTEN, which takes no parameters.
	//
	// Nothing in this package listens. A wakeup arrives as a bare channel
	// through WithWakeup, which database/postgres/pgnotify is one way to fill.
	NotifyChannel string `env:"NOTIFY_CHANNEL" json:"notifyChannel,omitempty" yaml:"notifyChannel,omitempty"`

	// Retention is how long a fired timer is kept before Reap may delete it.
	Retention time.Duration `env:"RETENTION" json:"retention,omitempty" yaml:"retention,omitempty"`

	// MaxAttempts is how many times a timer may be claimed before the set stops
	// handing it out.
	//
	// Unset means DefaultMaxAttempts, not unlimited — which is the opposite of
	// a work queue's default, and deliberately so: a timer fires once, so a
	// timer on its twentieth attempt is not busy, it is broken. Unlimited is
	// reachable, but has to be asked for by name, with a negative value.
	//
	// Stalled timers are counted by Stats.Stalled and excluded from every claim.
	// They are not deleted, so the keys remain available for inspection, and a
	// Release resets nothing on its own — an operator reschedules them once the
	// cause is fixed.
	MaxAttempts int `env:"MAX_ATTEMPTS" json:"maxAttempts,omitempty" yaml:"maxAttempts,omitempty"`

	// MaxClaimBatch caps how many timers one Claim may lease. A larger limit is
	// clamped to it rather than rejected, and a non-positive limit means "as
	// many as allowed".
	MaxClaimBatch int `env:"MAX_CLAIM_BATCH" json:"maxClaimBatch,omitempty" yaml:"maxClaimBatch,omitempty"`

	// ReapBatchSize caps how many fired timers one Reap deletes.
	ReapBatchSize int `env:"REAP_BATCH_SIZE" json:"reapBatchSize,omitempty" yaml:"reapBatchSize,omitempty"`

	// WriteAttempts is how many times a writer re-runs a statement that failed
	// with a serialization failure or a deadlock — the two conditions Postgres
	// resolves by asking the caller to try the whole thing again. Anything else
	// is returned on the first failure.
	WriteAttempts uint `env:"WRITE_ATTEMPTS" json:"writeAttempts,omitempty" yaml:"writeAttempts,omitempty"`

	// MinWakeInterval floors how long Wait sleeps. It bounds both a wake storm
	// and the spin that a due-but-unclaimable timer would otherwise cause; see
	// Wait.
	MinWakeInterval time.Duration `env:"MIN_WAKE_INTERVAL" json:"minWakeInterval,omitempty" yaml:"minWakeInterval,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *Config) EnsureDefaults() {
	cfg.table = tableFor(cfg.TablePrefix)

	if cfg.Retention <= 0 {
		cfg.Retention = DefaultRetention
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
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
		validation.Field(&cfg.MaxClaimBatch, validation.Required, validation.Min(1)),
		validation.Field(&cfg.ReapBatchSize, validation.Required, validation.Min(1)),
		validation.Field(&cfg.WriteAttempts, validation.Required, validation.Min(uint(1))),
		validation.Field(&cfg.MinWakeInterval, validation.Required, validation.Min(time.Millisecond)),
	)
}

// tableFor renders the timer table name under a namespace. It is the one place
// the component segment reaches this package's own code, and it reads it from
// the corpus rather than restating it, so the DDL, the statements and the
// identifier check cannot disagree about what the table is called.
func tableFor(prefix string) string {
	return ddl.Qualify(prefix) + queries.TimersTable
}

// resolvedTable returns the qualified table name, for the tests and the
// constructor that have to vet it.
func (cfg *Config) resolvedTable() string {
	return cfg.table
}

// attemptCeiling renders MaxAttempts as the bound parameter the predicates
// expect, where a non-positive value means unlimited.
//
// MaxAttempts is a signed int here, unlike its work-queue counterpart, precisely
// so that unlimited stays reachable after EnsureDefaults has filled a zero: a
// timer set defaults to a real ceiling, and a consumer who genuinely wants none
// says so with -1 rather than by hoping a zero survives.
//
// It saturates at MaxInt32 because int32 is the width of the attempts column,
// which also keeps the value in range where int is 32 bits.
func (cfg *Config) attemptCeiling() int {
	if cfg.MaxAttempts > math.MaxInt32 {
		return math.MaxInt32
	}
	if cfg.MaxAttempts < 0 {
		return 0
	}

	return cfg.MaxAttempts
}

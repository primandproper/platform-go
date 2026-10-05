package saga

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v15/saga/internal/queries"
	"github.com/primandproper/platform-go/v15/saga/internal/sagadb"
	"github.com/primandproper/platform-go/v15/saga/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

const (
	// DefaultCompletedRetention is how long a completed instance is kept. A
	// saga that ran every step has nothing left to say beyond the lifecycle
	// events it already published, so a week is for the support question that
	// arrives a few days late.
	DefaultCompletedRetention = 7 * 24 * time.Hour

	// DefaultCompensatedRetention is how long a compensated instance is kept.
	// It is longer than a completed one's for the reason outbox keeps its
	// quarantine longer than what it published: a saga that unwound is the one
	// somebody debugs, and the row is the state it unwound from.
	DefaultCompensatedRetention = 30 * 24 * time.Hour
)

// ErrUnretirableStatus is what RetentionTarget.Validate reports for a status a
// retention pass may not remove: anything but StatusCompleted and
// StatusCompensated.
//
// A running or compensating instance is work in flight, and a stuck one is
// waiting on an operator. Deleting any of them on a timer is data loss that
// reads as a saga that never happened, so the refusal is at construction rather
// than left to whoever wrote the policy.
var ErrUnretirableStatus = platformerrors.New("saga retention may only remove completed or compensated instances")

// RetentionTarget is the saga instance table expressed as a retention target:
// the instances in one terminal status, aged by when they reached it.
//
// # It satisfies retention.Target without implementing it
//
// saga does not import retention, so the method set below satisfies
// retention.Target structurally, the way audit.PruneTarget does, and the
// compile-time assertion saying so lives in this package's external test. The
// policies a deployment runs are sagacfg.NewRetentionPolicies, which is where
// the two windows are read from configuration.
//
// # Why it is not a retention.Table
//
// A Table deletes by a timestamp alone, and the timestamp here cannot tell a
// finished saga from a live one: last_updated_at is stamped by every
// transition, so a Table pointed at it would remove a running instance that
// happened to sit idle through a long step delay. This target binds a terminal
// status into the statement itself, so a row in any other status is one no
// horizon can select.
//
// It is a value type with exported fields, like retention.Table, so a policy set
// still reads as data.
type RetentionTarget struct {
	// TablePrefix is the namespace the saga table carries. It must match the
	// Store's — sagacfg builds both from one field for exactly that reason.
	TablePrefix string

	// Status is the terminal status this target removes: StatusCompleted or
	// StatusCompensated. Anything else fails Validate with
	// ErrUnretirableStatus.
	Status Status
}

// Describe names the table and the status instances are removed in, for
// telemetry and for the audit entry accounting for the sweep.
func (t RetentionTarget) Describe() string {
	return ddl.Qualify(t.TablePrefix) + queries.InstancesTable + " (" + string(t.Status) + ")"
}

// Validate vets the dialect, the table prefix, and the status.
//
// It runs at retention.Sweeper construction, so a policy aimed at a status that
// must never be swept is a process that does not start.
func (t RetentionTarget) Validate(d dialect.Dialect) error {
	if !d.Valid() {
		return platformerrors.Wrapf(dialect.ErrUnsupported, "saga dialect %q", d)
	}

	if err := migrations.ValidatePrefix(t.TablePrefix); err != nil {
		return err
	}

	switch t.Status {
	case StatusCompleted, StatusCompensated:
		return nil
	case StatusRunning, StatusCompensating, StatusStuck:
		return platformerrors.Wrapf(ErrUnretirableStatus, "status %q", t.Status)
	default:
		return platformerrors.Wrapf(ErrUnretirableStatus, "unknown status %q", t.Status)
	}
}

// Sweep removes at most limit instances in the target's status that last
// changed at or before cutoff, oldest first.
func (t RetentionTarget) Sweep(
	ctx context.Context,
	q database.Tx,
	d dialect.Dialect,
	cutoff time.Time,
	limit int,
) (int64, error) {
	// The querier is built per call because the dialect is a parameter of one:
	// a target is a value in a policy set, assembled before anything knows
	// which database will run it. audit.PruneTarget makes the same trade.
	querier, err := t.querier(d)
	if err != nil {
		return 0, err
	}

	before := cutoff.UTC()

	removed, err := querier.PruneSagaInstances(ctx, q, sagadb.PruneSagaInstancesParams{
		RetiredStatus: string(t.Status),
		RetiredBefore: &before,
		ResultLimit:   int64(limit),
	})
	if err != nil {
		return 0, platformerrors.Wrapf(err, "pruning %s saga instances", t.Status)
	}

	return removed, nil
}

// Backlog counts the instances still at or before cutoff in the target's
// status, saturating at ceiling.
func (t RetentionTarget) Backlog(
	ctx context.Context,
	q database.SQLQueryExecutor,
	d dialect.Dialect,
	cutoff time.Time,
	ceiling int,
) (int64, error) {
	querier, err := t.querier(d)
	if err != nil {
		return 0, err
	}

	before := cutoff.UTC()

	row, err := querier.CountPrunableSagaInstances(ctx, q, sagadb.CountPrunableSagaInstancesParams{
		RetiredStatus: string(t.Status),
		RetiredBefore: &before,
		ResultLimit:   int64(ceiling),
	})
	if err != nil {
		return 0, platformerrors.Wrapf(err, "counting prunable %s saga instances", t.Status)
	}

	return row.Count, nil
}

func (t RetentionTarget) querier(d dialect.Dialect) (sagadb.Querier, error) {
	qd, err := sagadbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := sagadb.New(qd, ddl.Qualify(t.TablePrefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the saga querier")
	}

	return q, nil
}

package audit

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// DefaultRetention is how long entries are kept before a sweep may remove
	// them. Seven years is the default because that is the window the
	// regulations that ask for an audit log in the first place tend to name, and
	// a default that quietly deletes evidence someone was required to keep is a
	// worse failure than a table that grew larger than expected.
	DefaultRetention = 7 * 365 * 24 * time.Hour

	// DefaultRetentionBatchSize caps how many entries one batch removes, so a
	// long-neglected log is trimmed over several passes instead of one DELETE
	// that holds locks for minutes.
	DefaultRetentionBatchSize = 1000

	// DefaultScopePageSize is how many scopes one batch reads at a time. See
	// PruneTarget.ScopePageSize — it is a page, not a cap.
	DefaultScopePageSize = 100

	// DefaultRetentionPolicyName is the retention policy name auditcfg gives
	// the audit log. It is the policy's identity in the audit record and in
	// every metric attribute, so it is a constant rather than a string each
	// deployment picks.
	DefaultRetentionPolicyName = "audit-log"

	// DefaultRetentionBasis is the stated reason the audit log is pruned,
	// recorded in the entry accounting for each sweep. Override it with
	// RetentionConfig.Basis where a deployment has a regulation to name.
	DefaultRetentionBasis = "audit entries are kept for the configured retention window; past it they are " +
		"evidence nobody is obliged to hold, in a table that grows forever if nothing removes them"
)

// RetentionConfig carries the retention window and the bounds a sweep of the
// audit log runs under.
//
// It is not a sweeper's configuration: this package no longer owns a sweep
// loop. The values here become a retention.Policy — see auditcfg.NewRetentionPolicy —
// and the scheduling, the fleet coordination, and the accounting come from
// there.
type RetentionConfig struct {
	// Basis is why the entries are deleted, recorded in the audit entry that
	// accounts for each sweep. Defaults to DefaultRetentionBasis.
	Basis string `env:"BASIS" json:"basis,omitempty" yaml:"basis,omitempty"`

	// Retention is how long an entry is kept. Defaults to DefaultRetention.
	Retention time.Duration `env:"RETENTION" json:"retention,omitempty" yaml:"retention,omitempty"`

	// BatchSize caps how many entries one batch removes. Defaults to
	// DefaultRetentionBatchSize.
	BatchSize int `env:"BATCH_SIZE" json:"batchSize,omitempty" yaml:"batchSize,omitempty"`

	// ScopePageSize is how many scopes one batch reads at a time. Defaults to
	// DefaultScopePageSize.
	ScopePageSize int `env:"SCOPE_PAGE" json:"scopePageSize,omitempty" yaml:"scopePageSize,omitempty"`
}

var _ validation.ValidatableWithContext = (*RetentionConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *RetentionConfig) EnsureDefaults() {
	if cfg.Basis == "" {
		cfg.Basis = DefaultRetentionBasis
	}
	if cfg.Retention <= 0 {
		cfg.Retention = DefaultRetention
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultRetentionBatchSize
	}
	if cfg.ScopePageSize <= 0 {
		cfg.ScopePageSize = DefaultScopePageSize
	}
}

// ValidateWithContext validates a RetentionConfig.
func (cfg *RetentionConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		// An hour, not a second. Retention on an audit log is a compliance
		// parameter, and a misplaced unit that would otherwise mean "keep
		// nothing" is worth refusing to start over.
		//
		// This floor is also the one thing retention.Policy cannot enforce on
		// its own: a zero Age is legal there, because against an expires_at it
		// means "delete as soon as it expires". Against this log it would mean
		// emptying the table, so the refusal has to happen where the column's
		// meaning is known, which is here.
		validation.Field(&cfg.Retention, validation.Required, validation.Min(time.Hour)),
		validation.Field(&cfg.BatchSize, validation.Required, validation.Min(1)),
		validation.Field(&cfg.ScopePageSize, validation.Required, validation.Min(1)),
	)
}

// PruneTarget is the audit log expressed as a retention target: the thing a
// retention.Sweeper drives to enforce the window above.
//
// # It satisfies retention.Target without implementing it
//
// There is no import of retention in this package and there cannot be one —
// retention imports audit, to record an entry accounting for every sweep it
// runs. Go's interfaces are structural, so the method set below satisfies
// retention.Target regardless, and the compile-time assertion saying so lives
// in this package's external test, which may import retention freely. A method
// that drifts from the interface fails there.
//
// # Why the audit log is not a retention.Table
//
// The declarative target deletes rows at or before a cutoff. This one cannot:
// entries chain per scope, and a DELETE that took the rows a timestamp
// predicate selected would punch a hole in the middle of a chain, which is
// indistinguishable from the tampering this package exists to detect.
//
// So each scope is pruned as a prefix, never a slice out of the middle, and the
// hash of the last entry removed is written back as that scope's prune
// watermark — so the oldest survivor still links to something, and Verify can
// tell retention's gap from a deletion. The two happen in the batch's
// transaction, which the Sweeper owns: a deletion whose watermark did not land
// would read as tampering.
//
// # One transaction now covers several scopes
//
// The Sweeper runs one transaction per batch, and a batch here spends its row
// budget across as many scopes as it takes to fill. A failure part-way through
// therefore rolls back the scopes already pruned in that batch, where the
// package's own sweeper used to commit them one scope at a time. The rows come
// back and the next sweep removes them again; the invariant that matters — a
// delete and its watermark are never separated — is strictly stronger for it.
//
// It is a value type with exported fields, like retention.Table, so a policy
// set still reads as data.
type PruneTarget struct {
	// Clock stamps the prune watermark's updated_at. Nil means the system
	// clock; it is here so a test can pin the value, and because nothing else
	// in this package reads the wall clock directly either.
	Clock clock.Clock

	// TablePrefix is the prefix the audit tables carry. It must match the
	// Recorder's — auditcfg builds both from one field for exactly that reason.
	TablePrefix string

	// ScopePageSize is how many scopes one batch reads at a time. Defaults to
	// DefaultScopePageSize.
	//
	// It is a page and not a cap: a batch keeps reading pages until it has
	// spent its row budget or run out of scopes, so the count here changes how
	// many queries a batch costs and never how much it removes. That matters
	// for the Sweeper's drained signal — a batch that stopped because it ran
	// out of scopes to visit really has drained, and one that stopped at a
	// scope cap would only have looked like it.
	ScopePageSize int
}

// Describe names the table entries are removed from, for telemetry and for the
// audit entry accounting for the sweep.
func (t PruneTarget) Describe() string {
	return newTables(t.TablePrefix).entries
}

// Validate vets the dialect and the table prefix.
//
// It runs at Sweeper construction, so a prefix that would not render a legal
// identifier is a process that does not start rather than a policy that fails
// every night into a log nobody reads.
func (t PruneTarget) Validate(d dialect.Dialect) error {
	if !d.Valid() {
		return platformerrors.Wrapf(dialect.ErrUnsupported, "audit dialect %q", d)
	}

	if err := ValidateTablePrefix(t.TablePrefix); err != nil {
		return err
	}

	// ScopePageSize is deliberately not checked. A non-positive one takes the
	// default, and no value of it can change what a batch removes — only how
	// many queries it costs to remove it.
	return nil
}

// Sweep removes at most limit entries recorded at or before cutoff, spending
// that budget across scopes until it is gone or no scope has anything left to
// give.
//
// Returning short of limit is how the Sweeper learns the log has drained, and
// it is honest here: the loop stops only when a page of scopes comes back empty
// or short, which is to say when every scope holding an entry past the cutoff
// has been visited.
//
// One case returns short with the backlog still non-zero, and it is worth
// naming because the backlog gauge will show it. A scope whose lowest position
// holds an entry newer than the cutoff cannot be pruned at all — see
// pruneBoundary — and that happens when two processes recording into one scope
// disagree about the time by more than the width of a write. It resolves itself
// as soon as the blocking entry ages past the cutoff.
func (t PruneTarget) Sweep(
	ctx context.Context,
	q database.SQLQueryExecutor,
	d dialect.Dialect,
	cutoff time.Time,
	limit int,
) (int64, error) {
	var (
		tbls    = newTables(t.TablePrefix)
		page    = t.scopePageSize()
		removed int64
		cursor  *string
	)

	for removed < int64(limit) {
		scopes, err := t.prunableScopes(ctx, q, d, tbls, cutoff, cursor, page)
		if err != nil {
			return 0, err
		}

		if len(scopes) == 0 {
			break
		}

		for _, scope := range scopes {
			if removed >= int64(limit) {
				break
			}

			pruned, pruneErr := t.pruneScope(ctx, q, d, tbls, scope, cutoff, int(int64(limit)-removed))
			if pruneErr != nil {
				// Zero rather than what the earlier scopes removed: the Sweeper
				// rolls this transaction back, so those rows are still there.
				return 0, pruneErr
			}

			removed += pruned
		}

		// Keyset rather than an offset, so a page cannot skip a scope that
		// another writer created behind the cursor while this batch ran.
		last := scopes[len(scopes)-1]
		cursor = &last

		if len(scopes) < page {
			break
		}
	}

	return removed, nil
}

// Backlog counts the entries still at or before cutoff, saturating at ceiling.
//
// It counts entries and not prunable entries: an entry blocked by the
// correctness bound described on Sweep is part of the backlog an operator is
// looking at, and hiding it would turn the one number that says "this is stuck"
// into another one that says everything is fine.
func (t PruneTarget) Backlog(
	ctx context.Context,
	q database.SQLQueryExecutor,
	d dialect.Dialect,
	cutoff time.Time,
	ceiling int,
) (int64, error) {
	query, args := newTables(t.TablePrefix).buildCountPrunableEntries(d, cutoff, ceiling)

	var backlog int64
	if err := q.QueryRowContext(ctx, query, args...).Scan(&backlog); err != nil {
		return 0, platformerrors.Wrap(err, "counting audit entries past the retention window")
	}

	return backlog, nil
}

// prunableScopes reads one page of the scopes holding anything at or before the
// cutoff, ordered so the cursor can advance past them.
func (t PruneTarget) prunableScopes(
	ctx context.Context,
	q database.SQLQueryExecutor,
	d dialect.Dialect,
	tbls *tables,
	cutoff time.Time,
	after *string,
	limit int,
) ([]string, error) {
	query, args := tbls.buildSelectPrunableScopes(d, cutoff, after, limit)

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, platformerrors.Wrap(err, "querying prunable audit scopes")
	}

	scopes := make([]string, 0, limit)
	if err = scanRows(rows, func() error {
		var scope string
		if scanErr := rows.Scan(&scope); scanErr != nil {
			return scanErr
		}
		scopes = append(scopes, scope)

		return nil
	}); err != nil {
		return nil, platformerrors.Wrap(err, "scanning prunable audit scopes")
	}

	return scopes, nil
}

// pruneScope removes a prefix of one scope's chain and records where it pruned
// to, reporting how many entries went.
//
// The two statements are not separable. If they were, a crash between them
// would leave a gap that Verify — correctly — would report as a deletion. They
// are in the same transaction because the Sweeper opened one around the batch.
func (t PruneTarget) pruneScope(
	ctx context.Context,
	q database.SQLQueryExecutor,
	d dialect.Dialect,
	tbls *tables,
	scope string,
	cutoff time.Time,
	budget int,
) (int64, error) {
	boundary, ok, err := t.pruneBoundary(ctx, q, d, tbls, scope, cutoff, budget)
	if !ok || err != nil {
		return 0, err
	}

	query, args := tbls.buildSelectPruneTarget(d, scope, boundary)

	var (
		targetSeq  int64
		targetHash string
	)
	if err = q.QueryRowContext(ctx, query, args...).Scan(&targetSeq, &targetHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}

		return 0, platformerrors.Wrapf(err, "reading audit prune target for scope %q", scope)
	}

	query, args = tbls.buildDeletePruned(d, scope, targetSeq)

	result, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, platformerrors.Wrapf(err, "deleting aged audit entries from scope %q", scope)
	}

	pruned, err := result.RowsAffected()
	if err != nil {
		return 0, platformerrors.Wrapf(err, "counting pruned audit entries for scope %q", scope)
	}

	// The watermark is what keeps the chain verifiable across the gap the
	// DELETE just made: the oldest surviving entry's PrevHash is checked
	// against it rather than against a row that no longer exists.
	query, args = tbls.buildUpdateChainPruned(d, scope, targetHash, targetSeq, t.now())
	if _, err = q.ExecContext(ctx, query, args...); err != nil {
		return 0, platformerrors.Wrapf(err, "recording audit prune watermark for scope %q", scope)
	}

	return pruned, nil
}

// pruneBoundary computes the highest position this batch may remove from a
// scope, reporting false when there is nothing to do.
//
// Two bounds apply and the lower wins. The budget bound is what is left of the
// batch's row allowance. The correctness bound is the first entry that must
// survive the cutoff: pruning strictly below it is what guarantees the
// survivors remain a contiguous suffix, which deleting by timestamp alone would
// not — recorded_at comes from the recording process's clock and so is not
// perfectly ordered with respect to position across several processes.
func (t PruneTarget) pruneBoundary(
	ctx context.Context,
	q database.SQLQueryExecutor,
	d dialect.Dialect,
	tbls *tables,
	scope string,
	cutoff time.Time,
	budget int,
) (boundary int64, ok bool, err error) {
	query, args := tbls.buildSelectPruneBounds(d, scope, cutoff)

	var oldest, firstToKeep sql.NullInt64
	if err = q.QueryRowContext(ctx, query, args...).Scan(&oldest, &firstToKeep); err != nil {
		return 0, false, platformerrors.Wrapf(err, "reading audit prune bounds for scope %q", scope)
	}

	if !oldest.Valid {
		return 0, false, nil
	}

	boundary = oldest.Int64 + int64(budget) - 1
	if firstToKeep.Valid && firstToKeep.Int64 <= boundary {
		boundary = firstToKeep.Int64 - 1
	}

	if boundary < oldest.Int64 {
		return 0, false, nil
	}

	return boundary, true, nil
}

// scopePageSize is ScopePageSize or the default.
func (t PruneTarget) scopePageSize() int {
	if t.ScopePageSize <= 0 {
		return DefaultScopePageSize
	}

	return t.ScopePageSize
}

// now is the Clock's reading, or the wall clock's when none was set.
func (t PruneTarget) now() time.Time {
	if t.Clock == nil {
		return time.Now().UTC()
	}

	return t.Clock.Now().UTC()
}

package audit

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v14/audit/internal/auditdb"
	"github.com/primandproper/platform-go/v14/audit/internal/queries"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

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
	ScopePageSize int `env:"SCOPE_PAGE_SIZE" json:"scopePageSize,omitempty" yaml:"scopePageSize,omitempty"`
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
// # It holds no clock
//
// It held one, and the clock stamped the prune watermark's last_updated_at. The
// generated write takes that from the server instead: the column is bookkeeping
// on a row nothing hashes and nothing compares, so the reason to bind an
// application clock does not reach it, and the reason not to does — two clocks
// writing one column is two answers to when the row last moved. What still
// comes from the caller's clock is everything that decides which rows go: the
// cutoff a retention.Sweeper computes and hands to Sweep.
//
// It is a value type with exported fields, like retention.Table, so a policy
// set still reads as data.
type PruneTarget struct {
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
	return ddl.Qualify(t.TablePrefix) + queries.EntriesTable
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
	q database.Tx,
	d dialect.Dialect,
	cutoff time.Time,
	limit int,
) (int64, error) {
	// The querier is built per call because the dialect is a parameter of one:
	// a PruneTarget is a value in a policy set, assembled before anything knows
	// which database will run it, and retention.Sweeper supplies both the
	// executor and the dialect when it opens the batch's transaction. What that
	// costs is one prefix substitution per statement per batch, against a batch
	// that then issues four statements per scope.
	querier, err := newQuerier(d, t.TablePrefix)
	if err != nil {
		return 0, err
	}

	var (
		page    = t.scopePageSize()
		removed int64
		cursor  *tenancy.Scope
	)

	for removed < int64(limit) {
		scopes, scopeErr := t.prunableScopes(ctx, q, querier, cutoff, cursor, page)
		if scopeErr != nil {
			return 0, scopeErr
		}

		if len(scopes) == 0 {
			break
		}

		for _, scope := range scopes {
			if removed >= int64(limit) {
				break
			}

			pruned, pruneErr := t.pruneScope(ctx, q, querier, scope, cutoff, int64(limit)-removed)
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
	querier, err := newQuerier(d, t.TablePrefix)
	if err != nil {
		return 0, err
	}

	row, err := querier.CountPrunableAuditEntries(ctx, q, auditdb.CountPrunableAuditEntriesParams{
		Horizon:     cutoff.UTC(),
		ResultLimit: int64(ceiling),
	})
	if err != nil {
		return 0, platformerrors.Wrap(err, "counting audit entries past the retention window")
	}

	return row.Count, nil
}

// prunableScopes reads one page of the scopes holding anything at or before the
// cutoff, ordered so the cursor can advance past them.
//
// The first page and the pages after it are two statements rather than one with
// an optional cursor, and the empty scope is why: it is a scope like any other
// — the one platform-level events are recorded in — so a keyset that coalesced
// an absent cursor to the empty string would place the first page just past it,
// and the log's own events would be the ones no sweep ever visited.
func (t PruneTarget) prunableScopes(
	ctx context.Context,
	q database.SQLQueryExecutor,
	querier auditdb.Querier,
	cutoff time.Time,
	after *tenancy.Scope,
	limit int,
) ([]tenancy.Scope, error) {
	horizon := cutoff.UTC()

	if after == nil {
		rows, err := querier.ListPrunableAuditScopes(ctx, q, auditdb.ListPrunableAuditScopesParams{
			Horizon:     horizon,
			ResultLimit: int64(limit),
		})
		if err != nil {
			return nil, platformerrors.Wrap(err, "querying prunable audit scopes")
		}

		return convertRows(rows, func(row *auditdb.ListPrunableAuditScopesRow) (tenancy.Scope, error) {
			return row.Scope, nil
		})
	}

	// The cursor binds as the identifier rather than as the Scope, because it is
	// a keyset position — the last scope this walk handed out — and not the
	// owner of anything. The statement compares it for order, and the argument
	// it shares its name with in the paged entry listing is a position too.
	rows, err := querier.ListPrunableAuditScopesAfter(ctx, q, auditdb.ListPrunableAuditScopesAfterParams{
		Horizon:     horizon,
		PageCursor:  after.Owner(),
		ResultLimit: int64(limit),
	})
	if err != nil {
		return nil, platformerrors.Wrap(err, "querying prunable audit scopes")
	}

	return convertRows(rows, func(row *auditdb.ListPrunableAuditScopesAfterRow) (tenancy.Scope, error) {
		return row.Scope, nil
	})
}

// pruneScope removes a prefix of one scope's chain and records where it pruned
// to, reporting how many entries went.
//
// The delete and the watermark are not separable. If they were, a crash between
// them would leave a gap that Verify — correctly — would report as a deletion.
// They are in the same transaction because the Sweeper opened one around the
// batch.
func (t PruneTarget) pruneScope(
	ctx context.Context,
	q database.SQLQueryExecutor,
	querier auditdb.Querier,
	scope tenancy.Scope,
	cutoff time.Time,
	budget int64,
) (int64, error) {
	boundary, ok, err := t.pruneBoundary(ctx, q, querier, scope, cutoff, budget)
	if !ok || err != nil {
		return 0, err
	}

	target, err := querier.GetAuditPruneTarget(ctx, q, auditdb.GetAuditPruneTargetParams{
		Scope:    scope,
		Boundary: boundary,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}

		return 0, platformerrors.Wrapf(err, "reading audit prune target for scope %s", scope)
	}

	// Capped as well as bounded, and the cap cannot bite: positions are
	// distinct integers, so the rows between the scope's oldest and the
	// boundary computed from it number at most the budget itself. That matters
	// more than it sounds. A cap that truncated the delete would leave rows
	// below the watermark this pass is about to write, and the next
	// verification would find its oldest survivor linking to an entry that is
	// gone — retention reporting itself as tampering.
	//
	// What it buys is the property held by the statement rather than by the
	// arithmetic in front of it: an unbounded DELETE over a long-neglected
	// scope holds locks for minutes and times out somewhere in the middle,
	// after which the next attempt starts over.
	pruned, err := querier.PruneAuditLogEntries(ctx, q, auditdb.PruneAuditLogEntriesParams{
		Scope:       scope,
		ThroughSeq:  target.Seq,
		ResultLimit: budget,
	})
	if err != nil {
		return 0, platformerrors.Wrapf(err, "deleting aged audit entries from scope %s", scope)
	}

	// The watermark is what keeps the chain verifiable across the gap the
	// DELETE just made: the oldest surviving entry's PrevHash is checked
	// against it rather than against a row that no longer exists.
	if _, err = querier.RecordAuditChainPrune(ctx, q, auditdb.RecordAuditChainPruneParams{
		PrunedThroughSeq:  target.Seq,
		PrunedThroughHash: target.Hash,
		Scope:             scope,
	}); err != nil {
		return 0, platformerrors.Wrapf(err, "recording audit prune watermark for scope %s", scope)
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
//
// Both bounds arrive as pointers because both are aggregates over a set that
// may be empty. A scope holding no entries at all has no oldest position, and
// one whose entries are all past the cutoff has no first survivor; the absence
// is the answer in each case rather than a value to substitute for.
func (t PruneTarget) pruneBoundary(
	ctx context.Context,
	q database.SQLQueryExecutor,
	querier auditdb.Querier,
	scope tenancy.Scope,
	cutoff time.Time,
	budget int64,
) (boundary int64, ok bool, err error) {
	bounds, err := querier.GetAuditPruneBounds(ctx, q, auditdb.GetAuditPruneBoundsParams{
		Horizon: cutoff.UTC(),
		Scope:   scope,
	})
	if err != nil {
		return 0, false, platformerrors.Wrapf(err, "reading audit prune bounds for scope %s", scope)
	}

	if bounds.OldestSeq == nil {
		return 0, false, nil
	}

	boundary = *bounds.OldestSeq + budget - 1
	if bounds.FirstKeptSeq != nil && *bounds.FirstKeptSeq <= boundary {
		boundary = *bounds.FirstKeptSeq - 1
	}

	if boundary < *bounds.OldestSeq {
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

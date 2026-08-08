package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/primandproper/platform-go/v10/clock"
	"github.com/primandproper/platform-go/v10/database"
	"github.com/primandproper/platform-go/v10/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/identifiers"
	"github.com/primandproper/platform-go/v10/observability"
	"github.com/primandproper/platform-go/v10/observability/logging"
	"github.com/primandproper/platform-go/v10/observability/metrics"
	"github.com/primandproper/platform-go/v10/observability/tracing"
)

// Recorder writes entries into the audit log.
//
// Record takes the caller's query executor, which is the whole design. An audit
// entry that can commit while the change it describes rolls back — or the
// reverse — is not a record of what happened, and no amount of retrying fixes
// it after the fact. Anything that genuinely can happen after the commit (fan-
// out to a warehouse, notification, retention) happens after the commit;
// nothing that constitutes the record itself does.
type Recorder interface {
	// Record appends entries to the log inside the caller's transaction.
	//
	// It writes the assigned ID, timestamp, and chain fields back into each
	// entry, so a caller can reference or notarize what it just wrote without a
	// re-read.
	//
	// It is variadic where the prior art took one entry, because a transaction
	// that touches three resources should not pay three chain-head lookups and
	// three INSERTs while holding locks. Entries are chained in the order given.
	Record(ctx context.Context, q database.SQLQueryExecutor, entries ...*Entry) error
}

var _ Recorder = (*recorder)(nil)

// recorder is the SQL Recorder.
//
// Like outbox.Writer it holds no database handle: every Record takes the
// caller's executor, so one Recorder serves every transaction in the process.
type recorder struct {
	clock  clock.Clock
	o11y   observability.Observer
	logger logging.Logger
	tables *tables

	redactions map[string]Redaction

	recordedCounter metrics.Int64Counter
	recordLatency   metrics.Float64Histogram

	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	dialect         dialect.Dialect
	prefix          string
}

// NewRecorder builds a Recorder for the given dialect.
func NewRecorder(d dialect.Dialect, opts ...RecorderOption) (Recorder, error) {
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "audit dialect %q", d)
	}

	r := &recorder{
		dialect: d,
		prefix:  DefaultTablePrefix,
		clock:   clock.NewClock(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	if err := ValidateTablePrefix(r.prefix); err != nil {
		return nil, err
	}
	r.tables = newTables(r.prefix)

	r.o11y = observability.NewObserver(serviceName, r.logger, r.tracerProvider)
	r.logger = r.o11y.Logger()

	mp := metrics.EnsureMetricsProvider(r.metricsProvider)

	var err error
	if r.recordedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_entries_recorded", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating entries recorded counter")
	}
	if r.recordLatency, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_record_latency_ms", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating record latency histogram")
	}

	return r, nil
}

// Record appends entries inside the caller's transaction.
func (r *recorder) Record(ctx context.Context, q database.SQLQueryExecutor, entries ...*Entry) error {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		return op.Error(ErrNilExecutor, "recording audit entries")
	}

	if len(entries) == 0 {
		return nil
	}

	startTime := time.Now()
	defer func() {
		r.recordLatency.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	op.Set(entryCountKey, len(entries))

	// Validated up front, before anything is written, so a bad entry in the
	// middle of a batch cannot leave the earlier ones recorded and the chain
	// advanced past them.
	for _, entry := range entries {
		if err := entry.validate(); err != nil {
			return op.Error(err, "validating audit entries")
		}
	}

	// Grouped by scope while preserving the order within each, because the
	// chain is per scope: two entries in different scopes are unrelated
	// positions and must not be chained to one another.
	scopes, byScope := groupByScope(entries)
	op.Set(scopeCountKey, len(scopes))

	now := r.clock.Now().UTC().Truncate(time.Microsecond)

	for _, scope := range scopes {
		if err := r.recordScope(ctx, q, scope, byScope[scope], now); err != nil {
			return op.Error(err, "recording audit entries for scope %q", scope)
		}
	}

	// Counted after the statements succeed, but the caller's transaction can
	// still roll back afterwards — so this counts intent to record, not
	// committed rows. That gap is the caller's rollback rate.
	r.recordedCounter.Add(ctx, int64(len(entries)))

	return nil
}

// recordScope chains and inserts one scope's entries.
func (r *recorder) recordScope(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope string,
	entries []*Entry,
	now time.Time,
) error {
	head, err := r.lockChainHead(ctx, q, scope, now)
	if err != nil {
		return err
	}

	rows := make([]entryRow, 0, len(entries))

	prevHash := head.headHash
	seq := head.headSeq

	for _, entry := range entries {
		if entry.ID == "" {
			entry.ID = identifiers.New()
		}
		if entry.RecordedAt.IsZero() {
			entry.RecordedAt = now
		}
		entry.RecordedAt = entry.RecordedAt.UTC().Truncate(time.Microsecond)

		seq++
		entry.Seq = seq
		entry.PrevHash = prevHash

		row, rowErr := r.buildRow(entry)
		if rowErr != nil {
			return rowErr
		}

		prevHash = entry.Hash
		rows = append(rows, *row)
	}

	for chunk := range slices.Chunk(rows, maxBatchRows) {
		query, args := r.tables.buildInsertEntries(r.dialect, chunk)
		if _, err = q.ExecContext(ctx, query, args...); err != nil {
			return platformerrors.Wrap(err, "inserting audit entries")
		}
	}

	query, args := r.tables.buildUpdateChainHead(r.dialect, scope, prevHash, seq, now)
	if _, err = q.ExecContext(ctx, query, args...); err != nil {
		return platformerrors.Wrap(err, "advancing audit chain head")
	}

	return nil
}

// buildRow applies redaction, encodes the field blobs, and computes the entry's
// hash over the exact bytes that are about to be stored.
func (r *recorder) buildRow(entry *Entry) (*entryRow, error) {
	changes, metadata, err := r.redact(entry)
	if err != nil {
		return nil, err
	}

	encodedChanges, err := encodeFields(changes)
	if err != nil {
		return nil, err
	}

	encodedMetadata, err := encodeFields(metadata)
	if err != nil {
		return nil, err
	}

	if entry.Hash, err = chainHash(entry.PrevHash, canonicalImage(entry, encodedChanges, encodedMetadata)); err != nil {
		return nil, err
	}

	// The caller's Entry is updated to hold what was actually written, redaction
	// included. Leaving it holding the unredacted values would make the value a
	// caller logs or returns disagree with the value in the table, which is the
	// exact confusion redaction exists to prevent.
	entry.Changes = changes
	entry.Metadata = metadata

	return &entryRow{
		id:           entry.ID,
		seq:          entry.Seq,
		scope:        entry.Scope,
		recordedAt:   entry.RecordedAt,
		eventType:    string(entry.EventType),
		resourceType: entry.ResourceType,
		resourceID:   entry.ResourceID,
		actorID:      entry.Actor.ID,
		actorType:    string(entry.Actor.Type),
		actorIP:      entry.Actor.IP,
		changes:      encodedChanges,
		metadata:     encodedMetadata,
		prevHash:     entry.PrevHash,
		hash:         entry.Hash,
	}, nil
}

// chainState is a scope's position in its own chain.
type chainState struct {
	headHash string
	headSeq  int64
}

// lockChainHead reads a scope's chain head and holds it for the remainder of
// the caller's transaction, creating the row if this is the scope's first
// entry.
//
// The lock is the point. Concurrent transactions recording into the same scope
// would otherwise both read the same head and both compute the same next
// position; the unique index would refuse the second, taking down a business
// transaction whose only mistake was arriving second. Holding this row makes
// the second writer wait and then read the head the first one committed.
//
// This is also the answer to whether the head should be cached in the process
// to avoid a read per write. It should not, and it cannot: the read is not the
// point of the statement, the lock is, and a cached value would be stale the
// instant another process wrote to the same scope.
func (r *recorder) lockChainHead(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope string,
	now time.Time,
) (*chainState, error) {
	state, err := r.readChainHead(ctx, q, scope)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	query, args := r.tables.buildInsertChain(r.dialect, scope, now)
	if _, err = q.ExecContext(ctx, query, args...); err != nil {
		return nil, platformerrors.Wrapf(err, "creating audit chain for scope %q", scope)
	}

	// Re-read rather than assume the genesis values: another transaction may
	// have created this scope's chain and recorded into it between the first
	// read and the insert that just did nothing.
	if state, err = r.readChainHead(ctx, q, scope); err != nil {
		return nil, err
	}

	return state, nil
}

// readChainHead reads a scope's chain row, taking a row lock where the dialect
// has them.
func (r *recorder) readChainHead(ctx context.Context, q database.SQLQueryExecutor, scope string) (*chainState, error) {
	query, args := r.tables.buildSelectChainHead(r.dialect, scope, true)

	var (
		state             chainState
		prunedThroughSeq  int64
		prunedThroughHash string
	)

	if err := q.QueryRowContext(ctx, query, args...).Scan(
		&state.headSeq, &state.headHash, &prunedThroughSeq, &prunedThroughHash,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}

		return nil, platformerrors.Wrapf(err, "reading audit chain head for scope %q", scope)
	}

	return &state, nil
}

// groupByScope buckets entries by scope, returning the scopes in the order they
// were first seen so that Record's behavior does not depend on map iteration.
// Entries have already been validated, so none is nil.
func groupByScope(entries []*Entry) (scopes []string, byScope map[string][]*Entry) {
	byScope = make(map[string][]*Entry, 1)

	for _, entry := range entries {
		if _, seen := byScope[entry.Scope]; !seen {
			scopes = append(scopes, entry.Scope)
		}
		byScope[entry.Scope] = append(byScope[entry.Scope], entry)
	}

	return scopes, byScope
}

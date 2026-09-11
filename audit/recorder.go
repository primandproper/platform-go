package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v14/audit/internal/auditdb"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Recorder writes entries into the audit log.
//
// Record takes the caller's query executor, which is the whole design. An audit
// entry that can commit while the change it describes rolls back — or the
// reverse — is not a record of what happened, and no amount of retrying fixes
// it after the fact. Anything that genuinely can happen after the commit (fan-
// out to a warehouse, notification, retention) happens after the commit;
// nothing that constitutes the record itself does.
//
// # Record is never an RPC
//
// audit/grpc serves the Reader and stops there, and this is the reason. A
// recording that crossed a connection would commit on its own, in a
// transaction of the audit service's rather than of the change it describes,
// so the two could disagree in either direction: an entry for a change that
// rolled back, or a committed change no entry names. A retry does not fix
// either after the fact, and a client that could be told the recording failed
// has already committed the change it was about.
//
// It is the sharpest instance of a rule the whole transport lane is made by —
// a write whose caller is already inside the process's own transaction is not
// an RPC — and identity/grpc's package documentation states it once for every
// surface that follows.
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
	Record(ctx context.Context, q database.Tx, entries ...*Entry) error
}

var _ Recorder = (*ChainRecorder)(nil)

// ChainRecorder is the SQL Recorder, hash-chaining the entries it writes.
//
// Like outbox.Writer it holds no database handle: every Record takes the
// caller's executor, so one Recorder serves every transaction in the process.
//
// It is exported, and returned by NewRecorder, so a caller can depend on the
// recorder it built rather than on the Recorder seam.
type ChainRecorder struct {
	clock clock.Clock
	o11y  observability.Observer

	// q is the generated querier, instantiated for the configured dialect at
	// the configured prefix. It takes the executor per call, which is what lets
	// one Recorder serve every transaction in the process — the same property
	// this type had when it held no handle and rendered its own SQL.
	q auditdb.Querier

	recordedCounter  metrics.Int64Counter
	recordErrCounter metrics.Int64Counter
	recordLatency    metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read r.o11y.Logger() for the logger this recorder actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	redactions map[string]Redaction

	dialect dialect.Dialect
	prefix  string

	// precision is what a recorded_at is truncated to before it is hashed and
	// written, which is a property of the dialect's storage — see
	// storedPrecision.
	precision time.Duration
}

// NewRecorder builds a Recorder for the given dialect.
func NewRecorder(d dialect.Dialect, opts ...RecorderOption) (*ChainRecorder, error) {
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "audit dialect %q", d)
	}

	r := &ChainRecorder{
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

	// The generated querier, instantiated once the prefix is settled and the
	// dialect is known — the only two things the generated statements do not
	// already carry. What executes is what sqlc analyzed, with one marker
	// substitution; see audit/internal/auditdb.
	q, err := newQuerier(r.dialect, r.prefix)
	if err != nil {
		return nil, err
	}

	r.q = q
	r.precision = storedPrecision(r.dialect)

	r.o11y = observability.NewObserver(serviceName, r.logger, r.tracerProvider)

	mp := metrics.EnsureMetricsProvider(r.metricsProvider)

	if r.recordedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_entries_recorded", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating entries recorded counter")
	}
	if r.recordErrCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_record_errors", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating record error counter")
	}
	if r.recordLatency, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_record_latency_ms", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating record latency histogram")
	}

	return r, nil
}

// Record appends entries inside the caller's transaction.
func (r *ChainRecorder) Record(ctx context.Context, q database.Tx, entries ...*Entry) error {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		r.recordErrCounter.Add(ctx, 1)

		return op.Error(ErrNilExecutor, "recording audit entries")
	}

	if len(entries) == 0 {
		return nil
	}

	defer op.Time(ctx, r.clock, r.recordLatency)()

	op.Set(entryCountKey, len(entries))

	// Validated up front, before anything is written, so a bad entry in the
	// middle of a batch cannot leave the earlier ones recorded and the chain
	// advanced past them.
	for _, entry := range entries {
		if err := entry.validate(); err != nil {
			r.recordErrCounter.Add(ctx, 1)

			return op.Error(err, "validating audit entries")
		}
	}

	// Grouped by scope while preserving the order within each, because the
	// chain is per scope: two entries in different scopes are unrelated
	// positions and must not be chained to one another.
	scopes, byScope := groupByScope(entries)
	op.Set(scopeCountKey, len(scopes))

	now := r.clock.Now().UTC().Truncate(r.precision)

	for _, scope := range scopes {
		if err := r.recordScope(ctx, q, scope, byScope[scope], now); err != nil {
			r.recordErrCounter.Add(ctx, 1)

			return op.Error(err, "recording audit entries for scope %s", scope)
		}
	}

	// Counted after the statements succeed, but the caller's transaction can
	// still roll back afterwards — so this counts intent to record, not
	// committed rows. That gap is the caller's rollback rate.
	//
	// The errors counter beside it is what makes this number readable. Without
	// one, a failing recorder showed up only as entries_recorded not climbing,
	// which is indistinguishable from a service with nothing to audit — and audit
	// is precisely the subsystem where "nothing happened" must not be the same
	// signal as "nothing was written".
	r.recordedCounter.Add(ctx, int64(len(entries)))

	return nil
}

// recordScope chains and inserts one scope's entries.
//
// One statement per entry, where this used to be a multi-row INSERT capped at
// seventy rows by SQLite's bind-parameter ceiling. The multi-row form's shape is
// the caller's cardinality, so it has no static text for sqlc to check — and
// the cap that kept it legal was arithmetic over a column count nothing
// verified. What it costs is a round trip per entry inside a transaction the
// caller had already opened, on a path whose cardinality is the number of
// resources one request touched.
func (r *ChainRecorder) recordScope(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	entries []*Entry,
	now time.Time,
) error {
	head, err := r.lockChainHead(ctx, q, scope)
	if err != nil {
		return err
	}

	prevHash := head.headHash
	seq := head.headSeq

	for _, entry := range entries {
		if entry.ID == "" {
			entry.ID = identifiers.New()
		}
		if entry.RecordedAt.IsZero() {
			entry.RecordedAt = now
		}
		entry.RecordedAt = entry.RecordedAt.UTC().Truncate(r.precision)

		seq++
		entry.Seq = seq
		entry.PrevHash = prevHash

		params, rowErr := r.buildRow(entry)
		if rowErr != nil {
			return rowErr
		}

		if err = r.q.InsertAuditLogEntry(ctx, q, *params); err != nil {
			return platformerrors.Wrap(err, "inserting audit entries")
		}

		prevHash = entry.Hash
	}

	if _, err = r.q.AdvanceAuditChainHead(ctx, q, auditdb.AdvanceAuditChainHeadParams{
		HeadSeq:  seq,
		HeadHash: prevHash,
		Scope:    scope,
	}); err != nil {
		return platformerrors.Wrap(err, "advancing audit chain head")
	}

	return nil
}

// buildRow applies redaction, encodes the field blobs, and computes the entry's
// hash over the exact bytes that are about to be stored.
func (r *ChainRecorder) buildRow(entry *Entry) (*auditdb.InsertAuditLogEntryParams, error) {
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

	params := insertParams(entry, encodedChanges, encodedMetadata)

	return &params, nil
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
func (r *ChainRecorder) lockChainHead(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
) (*chainState, error) {
	state, err := r.readChainHead(ctx, q, scope)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// The row already there wins, unchanged, so two transactions recording into
	// a scope for the first time do not race: the loser waits for the winner to
	// commit and then locks the row that now exists, rather than failing on the
	// primary key.
	if _, err = r.q.CreateAuditChain(ctx, q, auditdb.CreateAuditChainParams{Scope: scope}); err != nil {
		return nil, platformerrors.Wrapf(err, "creating audit chain for scope %s", scope)
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
//
// The lock is in the statement rather than in an argument to it, because a
// clause is statement text on all three servers — so the locked read and the
// unlocked one the reader uses are two named statements in the corpus, and this
// is the one that holds the row.
func (r *ChainRecorder) readChainHead(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope) (*chainState, error) {
	row, err := r.q.LockAuditChain(ctx, q, auditdb.LockAuditChainParams{Scope: scope})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}

		return nil, platformerrors.Wrapf(err, "reading audit chain head for scope %s", scope)
	}

	return &chainState{headHash: row.HeadHash, headSeq: row.HeadSeq}, nil
}

// groupByScope buckets entries by scope, returning the scopes in the order they
// were first seen so that Record's behavior does not depend on map iteration.
// Entries have already been validated, so none is nil.
//
// A tenancy.Scope is a comparable struct, so it keys the map directly rather
// than through the identifier it names — which is what keeps the bucketing on
// the value the chain is partitioned by.
func groupByScope(entries []*Entry) (scopes []tenancy.Scope, byScope map[tenancy.Scope][]*Entry) {
	byScope = make(map[tenancy.Scope][]*Entry, 1)

	for _, entry := range entries {
		if _, seen := byScope[entry.Scope]; !seen {
			scopes = append(scopes, entry.Scope)
		}
		byScope[entry.Scope] = append(byScope[entry.Scope], entry)
	}

	return scopes, byScope
}

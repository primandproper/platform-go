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
	// Record appends entries to one scope's chain, inside the caller's
	// transaction.
	//
	// It writes the assigned ID, timestamp, and chain fields back into each
	// entry, so a caller can reference or notarize what it just wrote without a
	// re-read.
	//
	// It is variadic where the prior art took one entry, because a transaction
	// that touches three resources should not pay three chain-head lookups and
	// three INSERTs while holding locks. Entries are chained in the order given,
	// into the chain the scope argument names.
	//
	// The scope is an argument rather than a field read off each entry, and
	// here that is more than the module's rule about a write binding a scope it
	// was given. An Entry.Scope also picks the hash-chain partition, so a
	// caller who assembled the wrong one would not mislabel a row — they would
	// append to another tenant's chain, and a chain is the one structure in
	// this module whose whole value is that it cannot be appended to
	// incorrectly. An entry that names no scope adopts this one; an entry whose
	// scope disagrees with it is ErrScopeMismatch rather than either value
	// quietly winning.
	Record(ctx context.Context, q database.Tx, scope tenancy.Scope, entries ...*Entry) error
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

// Record appends entries to one scope's chain, inside the caller's transaction.
//
// One call writes into one chain. The variadic form is a batch of entries for
// the transaction's own tenant — several resources touched by one request — and
// deliberately not a batch across tenants: that shape existed only because the
// scope was read off each entry, and nothing in this module needs it, since a
// transaction spans one tenant's work. If machinery ever does, it comes back
// named for what it does rather than available by accident to every caller who
// passes a slice.
func (r *ChainRecorder) Record(
	ctx context.Context,
	q database.Tx,
	scope tenancy.Scope,
	entries ...*Entry,
) error {
	ctx, op := r.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		r.recordErrCounter.Add(ctx, 1)

		return op.Error(ErrNilExecutor, "recording audit entries")
	}

	// Checked before the empty-batch shortcut, because a scope nobody named is
	// a caller mistake whether or not they also passed entries — and a call
	// that reported success for one would report it again once the batch was
	// not empty.
	if err := scope.Validate(); err != nil {
		r.recordErrCounter.Add(ctx, 1)

		return op.Error(err, "recording audit entries")
	}

	if len(entries) == 0 {
		return nil
	}

	defer op.Time(ctx, r.clock, r.recordLatency)()

	op.Set(entryCountKey, len(entries))

	// Validated and settled up front, before anything is written, so a bad
	// entry in the middle of a batch cannot leave the earlier ones recorded and
	// the chain advanced past them.
	for _, entry := range entries {
		if err := entry.validate(); err != nil {
			r.recordErrCounter.Add(ctx, 1)

			return op.Error(err, "validating audit entries")
		}

		if err := adoptScope(scope, entry); err != nil {
			r.recordErrCounter.Add(ctx, 1)

			return op.Error(err, "validating audit entries")
		}
	}

	now := r.clock.Now().UTC().Truncate(r.precision)

	if err := r.recordScope(ctx, q, scope, entries, now); err != nil {
		r.recordErrCounter.Add(ctx, 1)

		return op.Error(err, "recording audit entries for scope %s", scope)
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
// the caller's transaction, creating the row first if this is the scope's first
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
//
// The row is made to exist before it is locked, on every write rather than only
// after a read has missed. A locked read that finds nothing takes a gap lock on
// InnoDB, and two tenants recording their first entries at once each held the
// gap the other was about to insert into — a deadlock InnoDB settled by killing
// one of the two callers' transactions. Creating first means the locked read
// never misses; audit/internal/queries' createChainQuery carries why MySQL's
// create takes the row lock itself. The price is one statement per write, paid
// by the path that was already correct, and it is the price of not having the
// path that was not.
func (r *ChainRecorder) lockChainHead(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
) (*chainState, error) {
	if _, err := r.q.CreateAuditChain(ctx, q, auditdb.CreateAuditChainParams{Scope: scope}); err != nil {
		return nil, platformerrors.Wrapf(err, "creating audit chain for scope %s", scope)
	}

	state, err := r.readChainHead(ctx, q, scope)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "locking audit chain for scope %s", scope)
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

// adoptScope settles which chain an entry is appended to, and writes the answer
// onto the entry — which is the caller's own value, the way every other field
// Record assigns is.
//
// The scope the call named is what the statements bind, so an entry that names
// a different one is refused rather than corrected: the two disagreeing is a
// caller holding one tenant's entry and recording it into another, which is a
// stale value or a mix-up and is not a thing to guess at. Here the cost of
// guessing is higher than it is anywhere else in this module, because the scope
// is the chain's partition as well as the row's label — a silent correction
// would move the entry into a different chain, and a silent adoption of the
// entry's own value would append to one the caller never named.
//
// An entry that names none adopts the argument. tenancy.Scope tells the zero
// value apart from Global(), so "unset" here is genuinely unset rather than the
// global scope spelled shortly — which is the same distinction Entry.Scope's
// own validation rests on.
//
// The adoption survives a Record that then fails, as every other field this
// package settles onto a caller's entry does — recordScope assigns the id, the
// position and the hash as it walks, so a batch that fails partway has already
// written some of them. What that costs is narrow and is the safe direction: a
// retry of the same call re-derives the position and the hash from the chain
// head it re-reads, and a retry that names a *different* scope is refused
// rather than silently re-aimed, which is what a caller who adopted one scope
// and then asked for another should be told.
func adoptScope(scope tenancy.Scope, entry *Entry) error {
	if entry.Scope != (tenancy.Scope{}) && entry.Scope != scope {
		return platformerrors.Wrapf(ErrScopeMismatch,
			"audit entry names %q, the write names %q", entry.Scope, scope)
	}

	entry.Scope = scope

	return nil
}

package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Query selects which entries a List returns.
//
// Every field is a conjunct: a Query with an actor and two resource types
// matches that actor's events on either type. The zero Query matches
// everything, which is the right default for an operator console and the wrong
// one for anything a tenant can reach — see Scope.
type Query struct {
	// Scope restricts to one tenancy boundary. It is a pointer because the
	// empty string is a real scope, the one platform-level events belong to,
	// so a plain string could not distinguish "only platform events" from "every
	// tenant's events" — and getting that backwards in a multi-tenant read path
	// is a cross-tenant disclosure rather than a wrong answer.
	Scope *string
	// ActorID restricts to one principal. Empty does not filter.
	ActorID string
	// ActorType restricts to one kind of principal. Empty does not filter.
	ActorType ActorType
	// ResourceID restricts to one instance. Empty does not filter. Pair it with
	// ResourceTypes: instance IDs are rarely unique across types.
	ResourceID string
	// ResourceTypes restricts to the named kinds of resource. Empty does not
	// filter.
	ResourceTypes []string
	// EventTypes restricts to the named events. Empty does not filter.
	EventTypes []EventType
}

// BreakReason says how a chain failed to verify.
type BreakReason string

const (
	// BreakContentAltered means an entry's stored hash is not the hash of the
	// entry as it now reads: some column was changed after it was written.
	BreakContentAltered BreakReason = "content_altered"
	// BreakLinkMismatch means an entry's recorded predecessor hash is not its
	// predecessor's hash. Something was inserted, reordered, or rewritten.
	BreakLinkMismatch BreakReason = "link_mismatch"
	// BreakMissingEntry means a position in the chain has no row and retention
	// did not prune it: an entry was deleted.
	BreakMissingEntry BreakReason = "missing_entry"
)

// Break is where and how a chain stopped verifying.
type Break struct {
	// EntryID is the entry the break was detected at. It is empty for
	// BreakMissingEntry, where the whole point is that there is no row.
	EntryID string
	// Reason is what kind of break it is.
	Reason BreakReason
	// Expected is the hash the chain implies at this position.
	Expected string
	// Actual is the hash actually recorded there.
	Actual string
	// Seq is the position in the scope's chain.
	Seq int64
}

// VerificationResult is what a Verify found.
type VerificationResult struct {
	// From and To bound the window that was checked, as given.
	From time.Time
	To   time.Time
	// FirstBreak is where verification stopped, or nil if it did not.
	//
	// Only the first is reported, because after a break every subsequent link is
	// evaluated against a predecessor that is already known to be wrong: the
	// list of breaks after the first says how long the chain is, not how much of
	// it was tampered with.
	FirstBreak *Break
	// Scope is the chain that was walked.
	Scope string
	// Checked is how many entries were walked.
	Checked int
}

// Intact reports whether the verified range held together. It is a method
// rather than a field so that it cannot be set to disagree with FirstBreak.
func (r *VerificationResult) Intact() bool {
	return r != nil && r.FirstBreak == nil
}

// Reader reads the audit log.
//
// It is a separate interface from Recorder because the two have genuinely
// different dependencies: writing takes the caller's executor and holds no
// database handle at all, while reading owns its own and runs against the read
// replica.
type Reader interface {
	// Get returns one entry by ID. It returns an error wrapping ErrEntryNotFound
	// when there is no such entry.
	Get(ctx context.Context, id string) (*Entry, error)
	// List pages through the entries matching q.
	List(ctx context.Context, q *Query, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Entry], error)
	// Verify walks one scope's hash chain over a time range and reports the
	// first break, or that there was none.
	Verify(ctx context.Context, scope string, from, to time.Time) (*VerificationResult, error)
}

var _ Reader = (*SQLReader)(nil)

// SQLReader is the SQL Reader. It is exported, and returned by NewReader, so a
// caller can depend on the reader it built rather than on the Reader seam.
type SQLReader struct {
	client database.Client
	o11y   observability.Observer
	tables *tables

	verificationsCounter metrics.Int64Counter
	breaksCounter        metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read r.o11y.Logger() for the logger this reader actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	dialect         dialect.Dialect
	prefix          string
}

// NewReader builds a Reader over the database holding the audit tables. The
// dialect comes from the client, so the two cannot disagree.
func NewReader(client database.Client, opts ...ReaderOption) (*SQLReader, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "audit dialect %q", d)
	}

	r := &SQLReader{
		client:  client,
		dialect: d,
		prefix:  DefaultTablePrefix,
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

	mp := metrics.EnsureMetricsProvider(r.metricsProvider)

	var err error
	if r.verificationsCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_verifications", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating verifications counter")
	}
	if r.breaksCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_chain_breaks", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating chain breaks counter")
	}

	return r, nil
}

// Get returns one entry.
func (r *SQLReader) Get(ctx context.Context, id string) (*Entry, error) {
	ctx, op := r.o11y.Begin(ctx, observability.WithValue(entryIDKey, id))
	defer op.End()

	if id == "" {
		return nil, op.Error(platformerrors.ErrInvalidIDProvided, "getting audit entry")
	}

	query, args := r.tables.buildSelectEntryByID(r.dialect, id)

	stored, err := scanEntry(r.client.Reader().QueryRowContext(ctx, query, args...))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, op.Error(platformerrors.Wrapf(ErrEntryNotFound, "audit entry %q", id), "getting audit entry")
		}

		return nil, op.Error(err, "getting audit entry %q", id)
	}

	return &stored.entry, nil
}

// List pages through matching entries, newest first when the filter says so.
func (r *SQLReader) List(
	ctx context.Context,
	q *Query,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Entry], error) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	if filter == nil {
		filter = filtering.DefaultQueryFilter()
	}

	tracing.AttachQueryFilterToSpan(op.Span(), filter)
	q.attachTo(op)

	limit := filtering.DefaultQueryFilterLimit
	if filter.MaxResponseSize != nil && *filter.MaxResponseSize > 0 {
		limit = int(*filter.MaxResponseSize)
	}

	query, args := r.tables.buildListEntries(r.dialect, q, filter, limit)

	stored, err := scanEntries(ctx, r.client.Reader(), query, args)
	if err != nil {
		return nil, op.Error(err, "listing audit entries")
	}

	entries := make([]*Entry, 0, len(stored))
	for i := range stored {
		entries = append(entries, &stored[i].entry)
	}

	query, args = r.tables.buildCountEntries(r.dialect, q, filter)

	var total uint64
	if err = r.client.Reader().QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return nil, op.Error(err, "counting audit entries")
	}

	return filtering.NewQueryFilteredResult(
		entries, uint64(len(entries)), total,
		func(e *Entry) string { return e.ID },
		filter,
	), nil
}

// Verify walks a scope's chain over a time range.
//
// What a clean result proves, stated precisely because it is easy to overstate:
// every entry in the range hashes to what it claims, and each links to the one
// before it, so no entry was edited, removed, or reordered by anyone who could
// not also rewrite every entry after it. What it does not prove is that the
// whole table was not replaced wholesale by a consistent forgery — nothing
// self-contained can, and the answer to that is to publish the head hash
// somewhere this database's owner does not control. Hash returned by Record is
// what you would publish.
//
// A zero from or to leaves that end unbounded.
func (r *SQLReader) Verify(ctx context.Context, scope string, from, to time.Time) (*VerificationResult, error) {
	ctx, op := r.o11y.Begin(ctx, observability.WithValue(scopeKey, scope))
	defer op.End()

	query, args := r.tables.buildSelectChainRange(r.dialect, scope, from, to)

	stored, err := scanEntries(ctx, r.client.Reader(), query, args)
	if err != nil {
		return nil, op.Error(err, "reading audit chain for scope %q", scope)
	}

	result := &VerificationResult{Scope: scope, From: from, To: to, Checked: len(stored)}

	if len(stored) > 0 {
		var anchor *anchorState
		if anchor, err = r.anchorFor(ctx, scope, stored[0].entry.Seq); err != nil {
			return nil, op.Error(err, "anchoring audit chain for scope %q", scope)
		}

		result.FirstBreak = walkChain(stored, anchor)
	}

	r.verificationsCounter.Add(ctx, 1)

	op.Set(checkedKey, result.Checked).Set(intactKey, result.Intact())

	if !result.Intact() {
		r.breaksCounter.Add(ctx, 1)

		op.Set(breakReasonKey, string(result.FirstBreak.Reason)).
			Set(seqKey, result.FirstBreak.Seq).
			Set(entryIDKey, result.FirstBreak.EntryID)

		// Logged as well as returned. A break means somebody edited or removed a
		// row in the one table that exists to be unremovable, and a caller that
		// only checks Intact when it happens to run a verification would leave
		// that undiscovered until it did.
		op.Acknowledge(
			platformerrors.Wrapf(ErrChainBroken, "%s at position %d", result.FirstBreak.Reason, result.FirstBreak.Seq),
			"verifying audit chain for scope %q", scope,
		)
	}

	return result, nil
}

// anchorState is what the first entry of a verified range should link to.
type anchorState struct {
	// prevHash is the hash the first entry in range must record as its
	// predecessor.
	prevHash string
	// known is false when the predecessor position exists but its row does not,
	// which is a deletion rather than an anchor.
	known bool
}

// anchorFor resolves what the entry at firstSeq should be chained to.
//
// Three cases, and telling them apart is the whole reason retention writes a
// watermark. A range starting at the position just past where retention pruned
// links to the pruned watermark; a range starting at position zero of a scope
// that has never been pruned links to nothing, since that is the genesis entry;
// and any other range starts mid-chain and links to the entry before it. If
// that entry is simply absent, the chain has a hole retention did not make,
// which is a deletion and is reported as one.
func (r *SQLReader) anchorFor(ctx context.Context, scope string, firstSeq int64) (*anchorState, error) {
	prunedThroughSeq, prunedThroughHash, err := r.prunedThrough(ctx, scope)
	if err != nil {
		return nil, err
	}

	if firstSeq == prunedThroughSeq+1 {
		return &anchorState{prevHash: prunedThroughHash, known: true}, nil
	}

	query, args := r.tables.buildSelectEntryBySeq(r.dialect, scope, firstSeq-1)

	stored, err := scanEntry(r.client.Reader().QueryRowContext(ctx, query, args...))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &anchorState{}, nil
		}

		return nil, platformerrors.Wrapf(err, "reading audit entry at position %d", firstSeq-1)
	}

	return &anchorState{prevHash: stored.entry.Hash, known: true}, nil
}

// prunedThrough reads how far retention has pruned a scope. A scope with no
// chain row has never been written to, and so has never been pruned either.
func (r *SQLReader) prunedThrough(ctx context.Context, scope string) (seq int64, hash string, err error) {
	query, args := r.tables.buildSelectChainHead(r.dialect, scope, false)

	var (
		headSeq  int64
		headHash string
	)

	if err = r.client.Reader().QueryRowContext(ctx, query, args...).
		Scan(&headSeq, &headHash, &seq, &hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return -1, "", nil
		}

		return 0, "", platformerrors.Wrapf(err, "reading audit chain for scope %q", scope)
	}

	return seq, hash, nil
}

// walkChain checks each entry against its own content and against the entry
// before it, returning the first break or nil.
func walkChain(stored []storedEntry, anchor *anchorState) *Break {
	if !anchor.known {
		return &Break{Reason: BreakMissingEntry, Seq: stored[0].entry.Seq - 1}
	}

	expectedPrev := anchor.prevHash
	expectedSeq := stored[0].entry.Seq

	for i := range stored {
		entry := &stored[i].entry

		// Checked before the content, because a gap explains a link mismatch
		// and reporting the mismatch instead would name the wrong entry as the
		// problem.
		if entry.Seq != expectedSeq {
			return &Break{Reason: BreakMissingEntry, Seq: expectedSeq}
		}

		if entry.PrevHash != expectedPrev {
			return &Break{
				Reason:   BreakLinkMismatch,
				EntryID:  entry.ID,
				Seq:      entry.Seq,
				Expected: expectedPrev,
				Actual:   entry.PrevHash,
			}
		}

		// Recomputed over the stored blobs rather than a re-encoding of the
		// decoded maps; see canonicalImage for why that distinction decides
		// whether verification is sound.
		computed, err := chainHash(entry.PrevHash, canonicalImage(entry, stored[i].rawChanges, stored[i].rawMetadata))
		if err != nil || computed != entry.Hash {
			return &Break{
				Reason:   BreakContentAltered,
				EntryID:  entry.ID,
				Seq:      entry.Seq,
				Expected: computed,
				Actual:   entry.Hash,
			}
		}

		expectedPrev = entry.Hash
		expectedSeq = entry.Seq + 1
	}

	return nil
}

// attachTo records the query's selectors on the operation, so a slow or
// surprising List is legible from the trace alone.
func (q *Query) attachTo(op observability.Operation) {
	if q == nil {
		return
	}

	if q.Scope != nil {
		op.Set(scopeKey, *q.Scope)
	}
	if q.ActorID != "" {
		op.Set(actorIDKey, q.ActorID)
	}
	if q.ActorType != "" {
		op.Set(actorTypeKey, string(q.ActorType))
	}
	if q.ResourceID != "" {
		op.Set(resourceIDKey, q.ResourceID)
	}
	if len(q.ResourceTypes) > 0 {
		op.Set(resourceTypeKey, q.ResourceTypes)
	}
	if len(q.EventTypes) > 0 {
		types := make([]string, 0, len(q.EventTypes))
		for _, et := range q.EventTypes {
			types = append(types, string(et))
		}
		op.Set(eventTypeKey, types)
	}
}

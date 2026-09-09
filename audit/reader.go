package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v14/audit/internal/auditdb"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
)

// Query selects which entries a List returns.
//
// Every field is a conjunct: a Query with an actor and a resource type matches
// that actor's events on that type. The zero Query matches everything, which is
// the right default for an operator console and the wrong one for anything a
// tenant can reach — see Scope.
//
// # Why each selector is one value
//
// Two of these used to be sets — a list of resource types, a list of event
// types — and the port onto the checked corpus is where they stopped being. A
// paged read narrowed by a bound set is a statement two of the three dialects
// this package serves cannot hold: a list carries every predicate three times,
// once in the WHERE and once in each of the two count subqueries, and only an
// array-typed argument can be bound three times. MySQL and SQLite expand a set
// into bare markers instead, and the expansion is substituted at the first
// marker while the other two stand — a page whose arguments no longer line up
// with its placeholders.
//
// A fixed number of them is the other portable answer and it needs a closed
// domain to be one, which neither of these has: EventType and the resource type
// are deliberately open strings, so any arity would be a silent truncation of
// whatever a caller asked for. See database/querygen's ErrPositionalSetInList,
// where the shape and its two alternatives are argued once for every store here.
//
// So a Query names one of each, and a caller wanting the union of two runs two
// reads. What it gains is that the narrowing it did ask for is a statement sqlc
// checked against the schema, on every dialect.
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
	// ResourceType: instance IDs are rarely unique across types.
	ResourceID string
	// ResourceType restricts to one kind of resource. Empty does not filter.
	ResourceType string
	// EventType restricts to one kind of event. Empty does not filter.
	EventType EventType
}

// selectors is a Query rendered as the arguments the paged statements bind:
// one nullable value per narrowing, where nil narrows nothing.
type selectors struct {
	scope        *string
	actorID      *string
	actorType    *string
	resourceID   *string
	resourceType *string
	eventType    *string
}

// selectors renders the query's narrowings, each of which an absent value
// leaves alone.
//
// The scope is the one that reads its absence off a pointer rather than off the
// empty string, for the reason its own field gives: the empty scope is a scope.
func (q *Query) selectors() selectors {
	if q == nil {
		return selectors{}
	}

	return selectors{
		scope:        q.Scope,
		actorID:      optional(q.ActorID),
		actorType:    optional(string(q.ActorType)),
		resourceID:   optional(q.ResourceID),
		resourceType: optional(q.ResourceType),
		eventType:    optional(string(q.EventType)),
	}
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

	// q is the generated querier, instantiated for the client's dialect at the
	// configured prefix. It takes the executor per call, so a read against the
	// replica is a different argument rather than a different querier.
	q auditdb.Querier

	verificationsCounter metrics.Int64Counter
	breaksCounter        metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read r.o11y.Logger() for the logger this reader actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
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
		client: client,
		prefix: DefaultTablePrefix,
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
	q, err := newQuerier(d, r.prefix)
	if err != nil {
		return nil, err
	}

	r.q = q

	r.o11y = observability.NewObserver(serviceName, r.logger, r.tracerProvider)

	mp := metrics.EnsureMetricsProvider(r.metricsProvider)

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

	row, err := r.q.GetAuditLogEntry(ctx, r.client.Reader(), auditdb.GetAuditLogEntryParams{ID: id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, op.Error(platformerrors.Wrapf(ErrEntryNotFound, "audit entry %q", id), "getting audit entry")
		}

		return nil, op.Error(err, "getting audit entry %q", id)
	}

	stored, err := entryFromRow(&row)
	if err != nil {
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

	filter = pageFilter(filter)

	rows, err := r.listRows(ctx, q, filter)
	if err != nil {
		return nil, op.Error(err, "listing audit entries")
	}

	// The counts ride on the rows, from the one snapshot the page was read
	// from, so an empty page has none to read — which filtering.Drain reports
	// as unknown rather than as zero.
	if len(rows) > 0 {
		op.Set(entryCountKey, len(rows))
	}

	return filtering.Drain(rows, pageValue, pageCounts,
		func(e *Entry) string { return e.ID }, filter), nil
}

// listRows runs whichever direction the filter asks for and converts the page.
func (r *SQLReader) listRows(ctx context.Context, q *Query, filter *filtering.QueryFilter) ([]pageRow, error) {
	narrowings := q.selectors()

	params := auditdb.ListAuditLogEntriesParams{
		ScopeFilter:        narrowings.scope,
		ActorIDFilter:      narrowings.actorID,
		ActorTypeFilter:    narrowings.actorType,
		ResourceIDFilter:   narrowings.resourceID,
		ResourceTypeFilter: narrowings.resourceType,
		EventTypeFilter:    narrowings.eventType,

		// The window maps onto recorded_at, so the createdBefore and
		// createdAfter query parameters an HTTP caller already knows how to
		// send mean what they should here.
		CreatedAfter:  utcPtr(filter.CreatedAfter),
		CreatedBefore: utcPtr(filter.CreatedBefore),

		PageCursor:  filter.Cursor,
		ResultLimit: int64(*filter.MaxResponseSize),
	}

	got, err := sortedRows(filter,
		func() ([]auditdb.ListAuditLogEntriesRow, error) {
			return r.q.ListAuditLogEntries(ctx, r.client.Reader(), params)
		},
		func() ([]auditdb.ListAuditLogEntriesDescendingRow, error) {
			return r.q.ListAuditLogEntriesDescending(ctx, r.client.Reader(),
				auditdb.ListAuditLogEntriesDescendingParams(params))
		},
		func(row auditdb.ListAuditLogEntriesDescendingRow) auditdb.ListAuditLogEntriesRow {
			return auditdb.ListAuditLogEntriesRow(row)
		})
	if err != nil {
		return nil, err
	}

	return convertRows(got, entryPageRow)
}

// pageFilter is the filter a paged read is answered under: the caller's, with
// the page-size ceiling every other paged read in this module applies.
//
// It works on a copy. The clamp has to be applied to what the query binds and
// to what the result reports, and doing that by writing through the caller's
// pointer would hand them back a filter they did not pass.
func pageFilter(filter *filtering.QueryFilter) *filtering.QueryFilter {
	bounded := *filter

	size := uint16(filtering.DefaultQueryFilterLimit)
	if bounded.MaxResponseSize != nil {
		size = filtering.ClampResponseSize(uint64(*bounded.MaxResponseSize))
	}

	bounded.MaxResponseSize = &size

	return &bounded
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
// A zero from or to leaves that end unbounded, and a bound one is exclusive at
// both ends — the same reading the filter window a List takes has, since the
// two ask the same question of the same column and an entry that a Verify
// covered but a List over the same window did not would be a hole nobody could
// account for.
func (r *SQLReader) Verify(ctx context.Context, scope string, from, to time.Time) (*VerificationResult, error) {
	ctx, op := r.o11y.Begin(ctx, observability.WithValue(scopeKey, scope))
	defer op.End()

	rows, err := r.q.ListAuditChainEntries(ctx, r.client.Reader(), auditdb.ListAuditChainEntriesParams{
		Scope:          scope,
		RecordedAfter:  boundOrNil(from),
		RecordedBefore: boundOrNil(to),
	})
	if err != nil {
		return nil, op.Error(err, "reading audit chain for scope %q", scope)
	}

	stored, err := convertRows(rows, func(row *auditdb.ListAuditChainEntriesRow) (storedEntry, error) {
		converted, convErr := entryFromChainRow(row)
		if convErr != nil {
			return storedEntry{}, convErr
		}

		return *converted, nil
	})
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

	row, err := r.q.GetAuditLogEntryBySeq(ctx, r.client.Reader(),
		auditdb.GetAuditLogEntryBySeqParams{Scope: scope, Seq: firstSeq - 1})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &anchorState{}, nil
		}

		return nil, platformerrors.Wrapf(err, "reading audit entry at position %d", firstSeq-1)
	}

	stored, err := entryFromSeqRow(&row)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "reading audit entry at position %d", firstSeq-1)
	}

	return &anchorState{prevHash: stored.entry.Hash, known: true}, nil
}

// prunedThrough reads how far retention has pruned a scope. A scope with no
// chain row has never been written to, and so has never been pruned either.
func (r *SQLReader) prunedThrough(ctx context.Context, scope string) (seq int64, hash string, err error) {
	// The unlocked read, where the recorder takes the locked one. A verifier
	// holds nothing: it is reading what a chain has already committed, and a
	// row lock here would make a report block a write.
	row, err := r.q.GetAuditChain(ctx, r.client.Reader(), auditdb.GetAuditChainParams{Scope: scope})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return -1, "", nil
		}

		return 0, "", platformerrors.Wrapf(err, "reading audit chain for scope %q", scope)
	}

	return row.PrunedThroughSeq, row.PrunedThroughHash, nil
}

// boundOrNil renders one end of a verification's range: an unset time leaves
// that end unbounded, which the statement spells as an absent argument rather
// than as a sentinel the caller had to know.
func boundOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}

	utc := t.UTC()

	return &utc
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
	if q.ResourceType != "" {
		op.Set(resourceTypeKey, q.ResourceType)
	}
	if q.EventType != "" {
		op.Set(eventTypeKey, string(q.EventType))
	}
}

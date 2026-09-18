package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v14/audit/internal/auditdb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"
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
	// Scope restricts to one tenancy boundary. Nil narrows nothing: every
	// tenant's events, which is what an operator console asks for and what
	// nothing a tenant can reach should. A scope names one, and tenancy.Global
	// names the chain platform-level events are recorded in.
	//
	// It is a pointer to a Scope rather than a Scope because a Scope carries two
	// of these three readings and not the third. Scope.known separates the
	// global scope from a caller who never decided; it does not separate either
	// of those from "do not narrow at all", and getting that distinction
	// backwards in a multi-tenant read path is a cross-tenant disclosure rather
	// than a wrong answer.
	//
	// A non-nil pointer at the zero Scope is therefore not "every tenant" but a
	// caller whose own lookup came back empty. List refuses it with
	// tenancy.ErrNoScope rather than widening the read to cover it.
	Scope *tenancy.Scope
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
// empty string, for the reason its own field gives: the empty identifier is a
// scope. It renders to the identifier the column holds, which validate has
// already refused to derive from a scope that names nobody.
func (q *Query) selectors() selectors {
	if q == nil {
		return selectors{}
	}

	return selectors{
		scope:        scopeFilter(q.Scope),
		actorID:      optional(q.ActorID),
		actorType:    optional(string(q.ActorType)),
		resourceID:   optional(q.ResourceID),
		resourceType: optional(q.ResourceType),
		eventType:    optional(string(q.EventType)),
	}
}

// validate reports whether the query's narrowings can be bound. It is nil-safe,
// because a nil Query is the query that narrows nothing.
//
// Only the scope has anything to check. Every other selector reads its absence
// off the empty string, which is a value no caller can arrive at by losing one;
// the scope reads its absence off the pointer, so a Scope that names nobody has
// reached this field on purpose and cannot be told from one that was dropped.
func (q *Query) validate() error {
	if q == nil || q.Scope == nil {
		return nil
	}

	return q.Scope.Validate()
}

// scopeFilter renders the scope narrowing as the identifier the column holds,
// or nil for a query that does not narrow on it.
//
// The narrowing is a *string where the column is a tenancy.Scope, and the two
// are deliberately different types: the predicate compares this argument
// against the column in one arm and against NULL in the other, and that second
// arm resolves to a type of its own on MySQL. See SelectorArgSuffix in
// audit/internal/queries, where the split is argued for all six selectors.
func scopeFilter(scope *tenancy.Scope) *string {
	if scope == nil {
		return nil
	}

	owner := scope.Owner()

	return &owner
}

// ChainStart is the position a verification walks from when it is not
// continuing an earlier one: the position before a chain's first entry.
//
// It is -1 rather than 0 because 0 is a real position — the one a scope's first
// entry takes — and a walk starting past it would skip the genesis entry, which
// is the one link a forged chain has to reproduce. The value is the schema's
// own reading rather than a number invented for this argument:
// audit_log_chains defaults head_seq and pruned_through_seq to -1, meaning a
// chain that has issued nothing and lost nothing.
//
// Pass VerificationResult.LastSeq in its place to continue a verification that
// did not complete.
const ChainStart int64 = -1

const (
	// DefaultVerificationPageSize is how many entries one read of a
	// verification walk materializes.
	//
	// It is smaller than the paged list's default because the rows are not the
	// same size: a verification projects every column, change-set and metadata
	// blobs included, and re-hashes each row over the bytes as stored. Five
	// hundred of those is a page a process can hold without knowing how large a
	// consumer's entries are, which is the thing this package cannot know.
	DefaultVerificationPageSize = 500

	// DefaultVerificationCeiling is the most entries one Verify call walks
	// before it stops and reports where.
	//
	// A ceiling rather than no ceiling, because the surface that most wants
	// verification is the remote one, and there both ends of the window are
	// optional: a request naming neither asks for a scope's whole history, and
	// the default retention window puts that at seven years. Paging bounds what
	// such a call holds; the ceiling bounds how long it runs, and
	// VerificationResult.Complete is what keeps the difference between "this
	// chain is intact" and "this chain is intact as far as I looked" spellable
	// rather than silent.
	//
	// It is not a cap on what can be verified. A caller walks a longer chain by
	// calling again from VerificationResult.LastSeq, which is the same chain
	// checked through the seam — see chainWalk.
	DefaultVerificationCeiling = 100_000
)

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
	Scope tenancy.Scope
	// Checked is how many entries were walked. A walk that stopped at a break
	// counts the entries it examined, the breaking one included, and not the
	// rows behind it that it never reached.
	Checked int64
	// LastSeq is the position of the last entry this call verified, and what a
	// call continuing this one passes as its afterSeq.
	//
	// A call that verified nothing — an empty scope, an empty window, a break
	// on the first entry it read — reports the position it started past, so
	// resuming from it asks the same question again rather than starting over.
	LastSeq int64
	// Complete reports whether the walk reached the end of the window.
	//
	// It is false where the walk stopped early: at the reader's verification
	// ceiling, or at a break. Resume where it is false and Intact is true, by
	// calling Verify again over the same window with LastSeq as its afterSeq.
	// Where Intact is false there is nothing to resume — every link past a
	// break is evaluated against a predecessor already known to be wrong.
	//
	// It is not a second spelling of Intact, which is why both exist. A chain
	// can be intact as far as one call looked and still hold entries nobody has
	// checked; that is the state a ceiling leaves behind, and a scheduled
	// verification that could not tell it from a clean bill would report a log
	// as evidence on the strength of its first hundred thousand entries.
	Complete bool
}

// Intact reports whether the verified range held together. It is a method
// rather than a field so that it cannot be set to disagree with FirstBreak.
func (r *VerificationResult) Intact() bool {
	return r != nil && r.FirstBreak == nil
}

// Reader reads the audit log.
//
// It is a separate interface from Recorder because the two answer different
// questions of the same tables — one appends into a chain, three read it back —
// and not because they take different dependencies. Every method here takes the
// caller's executor, exactly as Record takes the caller's transaction, so a
// caller who recorded an entry inside a transaction can read it back inside the
// same one. That is the module's read shape and audit used to be its one
// exception: the reads bound the reader's own Reader() handle, which is a
// connection that cannot see the row the caller just wrote.
//
// The wider type is deliberate. A database.Tx satisfies
// database.SQLQueryExecutor, so one method serves both an operator console
// holding Client.Reader() and a recorder's caller still inside their
// transaction, and the second sees that transaction's uncommitted entries.
type Reader interface {
	// Get returns one entry by ID, optionally confined to a scope. It returns
	// an error wrapping ErrEntryNotFound when there is no such entry in that
	// scope.
	//
	// The scope is a *tenancy.Scope carrying Query.Scope's three readings, for
	// Query.Scope's reason: nil narrows nothing, which is the operator
	// console's read; a scope names one chain, and tenancy.Global names the one
	// platform-level events are recorded in; and a non-nil pointer at the zero
	// Scope is a caller whose own lookup came back empty, refused with
	// tenancy.ErrNoScope rather than widened to every tenant.
	Get(ctx context.Context, q database.SQLQueryExecutor, scope *tenancy.Scope, id string) (*Entry, error)
	// List pages through the entries matching query.
	List(ctx context.Context, q database.SQLQueryExecutor, query *Query, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Entry], error)
	// Verify walks one scope's hash chain over a time range, from afterSeq
	// onwards, and reports the first break or that there was none.
	//
	// The scope is a tenancy.Scope rather than the string it names, so a call
	// that lost its scope fails to compile rather than walking the global
	// chain. It is not the *tenancy.Scope Get takes, because a verification
	// walks one chain: "every tenant" is not a chain, and the third reading has
	// nothing to mean here. Pass ChainStart as afterSeq to walk from the
	// beginning of the range, or a previous result's LastSeq to continue it.
	// See the method on SQLReader for what an unset scope does and for what
	// bounds one call.
	Verify(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, from, to time.Time, afterSeq int64) (*VerificationResult, error)
}

var _ Reader = (*SQLReader)(nil)

// SQLReader is the SQL Reader. It is exported, and returned by NewReader, so a
// caller can depend on the reader it built rather than on the Reader seam.
//
// It holds no database.Client. The one NewReader takes is read for its dialect,
// which is what instantiates the querier below, and then dropped — every read
// runs on the executor its caller supplies, so there is no statement this
// reader issues on a connection of its own. See the Reader interface for why
// that is the whole point rather than a detail.
type SQLReader struct {
	o11y observability.Observer

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

	// How much of a chain one Verify call reads at a time and how much of one
	// it reads in total. See DefaultVerificationPageSize and
	// DefaultVerificationCeiling for what each bounds and why they are two
	// numbers rather than one.
	verificationPageSize int64
	verificationCeiling  int64
}

// NewReader builds a Reader over the audit tables. The dialect comes from the
// client, so the two cannot disagree.
//
// The client is taken for its dialect and for nothing else, and the reader
// keeps no reference to it: every read is handed an executor, so there is no
// Reader() call left in this file and no read that runs outside the caller's
// own transaction when they are in one.
func NewReader(client database.Client, opts ...ReaderOption) (*SQLReader, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "audit dialect %q", d)
	}

	r := &SQLReader{
		prefix:               DefaultTablePrefix,
		verificationPageSize: DefaultVerificationPageSize,
		verificationCeiling:  DefaultVerificationCeiling,
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

// Get returns one entry, optionally confined to a scope.
//
// # The scope, and its three readings
//
// It is a *tenancy.Scope and it carries exactly what Query.Scope carries, for
// the same reason. Nil narrows nothing and answers across every tenant, which
// is the operator console's read and the one a request-scoped caller must never
// make. A scope confines the read to one chain, and tenancy.Global is a scope
// like any other — the chain platform-level events are recorded in. A non-nil
// pointer at the zero Scope is neither of those: it is a caller whose own
// lookup came back empty, and it is refused with tenancy.ErrNoScope rather than
// widened into the read that answers everything.
//
// A plain tenancy.Scope would collapse the first reading into the second, since
// Scope tells the global scope from an undecided one and tells neither from "do
// not narrow at all" — and in a multi-tenant read path that distinction is a
// cross-tenant disclosure rather than a wrong answer.
//
// An entry that exists but sits outside a named scope is ErrEntryNotFound, the
// same answer an id that was never written gets. Telling the two apart would
// make this method an oracle for which entry ids exist in another tenant's log,
// which is the one thing an audit log must not become — so it is answered here,
// on the method every caller reaches, rather than by each surface comparing the
// scope back after an unconfined read.
func (r *SQLReader) Get(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope *tenancy.Scope,
	id string,
) (*Entry, error) {
	ctx, op := r.o11y.Begin(ctx, observability.WithValue(entryIDKey, id))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "getting audit entry")
	}

	if id == "" {
		return nil, op.Error(platformerrors.ErrInvalidIDProvided, "getting audit entry")
	}

	// Attached before it is checked, so a read that named a scope it had lost
	// is refused with the scope it asked for legible in the trace. The prose
	// spelling is deliberate: "<global>" reads as a decision where the empty
	// identifier reads as a field nobody filled in.
	if scope != nil {
		op.Set(scopeKey, scope.String())

		if err := scope.Validate(); err != nil {
			return nil, op.Error(err, "getting audit entry")
		}
	}

	row, err := r.q.GetAuditLogEntry(ctx, q, auditdb.GetAuditLogEntryParams{
		ID:          id,
		ScopeFilter: scopeFilter(scope),
	})
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
	q database.SQLQueryExecutor,
	query *Query,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Entry], error) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing audit entries")
	}

	if filter == nil {
		filter = filtering.DefaultQueryFilter()
	}

	tracing.AttachQueryFilterToSpan(op.Span(), filter)
	query.attachTo(op)

	// Checked after the query is on the span and before anything is bound, so a
	// read that named a scope it had lost is refused with the query it asked
	// legible in the trace.
	if err := query.validate(); err != nil {
		return nil, op.Error(err, "listing audit entries")
	}

	filter = pageFilter(filter)

	rows, err := r.listRows(ctx, q, query, filter)
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
func (r *SQLReader) listRows(
	ctx context.Context,
	q database.SQLQueryExecutor,
	query *Query,
	filter *filtering.QueryFilter,
) ([]pageRow, error) {
	narrowings := query.selectors()

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
			return r.q.ListAuditLogEntries(ctx, q, params)
		},
		func() ([]auditdb.ListAuditLogEntriesDescendingRow, error) {
			return r.q.ListAuditLogEntriesDescending(ctx, q,
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

// Verify walks a scope's chain over a time range, in pages, from afterSeq
// onwards.
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
//
// # Where one call starts, and where it stops
//
// afterSeq is the position the walk starts past: ChainStart for the beginning
// of the range, or an earlier result's LastSeq to continue where it left off.
// The link across that seam is checked like any other — the walk reads the
// entry at afterSeq to learn what its successor must record, rather than
// trusting the first row that comes back — so a resumed verification has no
// hole at the position its caller chose. Within one call the same property
// holds across every page boundary, and holds it more cheaply: the walk carries
// the predecessor's hash forward rather than re-anchoring per page. See
// chainWalk.
//
// It stops at the end of the range, at the first break, or at the reader's
// verification ceiling, and the result says which: Complete is true only for
// the first. So a caller walking a long chain loops while the result is intact
// and incomplete, passing LastSeq back in, and a caller walking a short one
// never notices the ceiling exists. Neither holds more than one page of entries
// at a time, which is what this method is for: an entry carries its change-set
// and metadata blobs, a scope's history is as long as the retention window
// allows, and the range is optional at both ends on the wire.
//
// # The scope, and why it is not a string
//
// It is a tenancy.Scope, so a caller who lost track of which chain they are
// walking fails to compile rather than walking the global one. That is the
// module's rule for a scope anywhere, and this method was the exception until
// v14: it took the owner identifier as a plain string, in which the empty
// string is simultaneously the platform chain and a caller who had nothing to
// pass — and a verification that silently walked the wrong chain reports a
// clean result for a log nobody checked.
//
// An unset scope is tenancy.ErrNoScope, reported here rather than at the driver
// so that the error names the call rather than the statement. The scope is
// bound as itself either way — the column holds one — so the refusal is the
// driver's too, and validating first only decides which of the two says so.
// tenancy.Global is a scope like any other here and walks the platform chain,
// which is what an entry recorded for no tenant records into.
func (r *SQLReader) Verify(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	from, to time.Time,
	afterSeq int64,
) (*VerificationResult, error) {
	ctx, op := r.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(afterSeqKey, afterSeq))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "verifying an audit chain")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "verifying an audit chain")
	}

	result := &VerificationResult{Scope: scope, From: from, To: to, LastSeq: afterSeq}
	walk := chainWalk{lastSeq: afterSeq}

	for {
		// Zero means the ceiling is reached. The loop leaves Complete false and
		// LastSeq where it is, which together are the resumption point.
		size := r.verificationPage(result.Checked)
		if size == 0 {
			break
		}

		stored, err := r.chainPage(ctx, q, scope, from, to, walk.lastSeq, size)
		if err != nil {
			return nil, op.Error(err, "reading audit chain for scope %s", scope)
		}

		if len(stored) == 0 {
			result.Complete = true

			break
		}

		// Anchored once, on the first page that returned anything: what the
		// range's first entry links to is a question about the entry before the
		// range, and every page after the first is anchored by the page before
		// it.
		if !walk.anchored {
			anchor, anchorErr := r.anchorFor(ctx, q, scope, stored[0].entry.Seq)
			if anchorErr != nil {
				return nil, op.Error(anchorErr, "anchoring audit chain for scope %s", scope)
			}

			walk.anchorAt(anchor, stored[0].entry.Seq)
		}

		result.FirstBreak = walk.page(stored)
		result.Checked, result.LastSeq = walk.checked, walk.lastSeq

		if result.FirstBreak != nil {
			break
		}

		// A short page is the end of the range. A full one may or may not be,
		// and the next read is what settles it — cheaper than a count, and a
		// count taken before the walk would be a count of rows a concurrent
		// append could add to.
		if int64(len(stored)) < size {
			result.Complete = true

			break
		}
	}

	r.verificationsCounter.Add(ctx, 1)

	op.Set(checkedKey, result.Checked).
		Set(lastSeqKey, result.LastSeq).
		Set(completeKey, result.Complete).
		Set(intactKey, result.Intact())

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
			"verifying audit chain for scope %s", scope,
		)
	}

	return result, nil
}

// verificationPage is how many entries the next read may take: the configured
// page, narrowed by whatever is left of the ceiling, and zero once nothing is.
//
// The narrowing matters rather than being tidiness. A ceiling enforced by
// discarding rows after they arrive is a ceiling on what the walk reports and
// not on what it reads, and what it reads is the cost this method exists to
// bound.
func (r *SQLReader) verificationPage(checked int64) int64 {
	if r.verificationCeiling <= 0 {
		return r.verificationPageSize
	}

	return min(r.verificationPageSize, max(r.verificationCeiling-checked, 0))
}

// chainPage reads one page of a scope's chain in position order, decoded into
// the form the walk hashes over.
func (r *SQLReader) chainPage(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	from, to time.Time,
	afterSeq, size int64,
) ([]storedEntry, error) {
	rows, err := r.q.ListAuditChainEntries(ctx, q, auditdb.ListAuditChainEntriesParams{
		Scope:          scope,
		RecordedAfter:  boundOrNil(from),
		RecordedBefore: boundOrNil(to),
		AfterSeq:       afterSeq,
		ResultLimit:    size,
	})
	if err != nil {
		return nil, err
	}

	return convertRows(rows, func(row *auditdb.ListAuditChainEntriesRow) (storedEntry, error) {
		converted, convErr := entryFromChainRow(row)
		if convErr != nil {
			return storedEntry{}, convErr
		}

		return *converted, nil
	})
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
func (r *SQLReader) anchorFor(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	firstSeq int64,
) (*anchorState, error) {
	prunedThroughSeq, prunedThroughHash, err := r.prunedThrough(ctx, q, scope)
	if err != nil {
		return nil, err
	}

	if firstSeq == prunedThroughSeq+1 {
		return &anchorState{prevHash: prunedThroughHash, known: true}, nil
	}

	row, err := r.q.GetAuditLogEntryBySeq(ctx, q,
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
func (r *SQLReader) prunedThrough(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
) (seq int64, hash string, err error) {
	// The unlocked read, where the recorder takes the locked one. A verifier
	// holds nothing: it is reading what a chain has already committed, and a
	// row lock here would make a report block a write.
	row, err := r.q.GetAuditChain(ctx, q, auditdb.GetAuditChainParams{Scope: scope})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return -1, "", nil
		}

		return 0, "", platformerrors.Wrapf(err, "reading audit chain for scope %s", scope)
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

// chainWalk is a verification's position in a chain: what the next entry must
// link to, where it must sit, and how much has been checked getting there.
//
// It is a value carried from one page to the next rather than two locals inside
// one loop, and that is the whole of what makes a paged verification mean what
// an unpaged one meant. The link between the last entry of one page and the
// first of the next is checked like every other link, because the walk never
// forgets what it was expecting; a walk that re-anchored per page would accept
// any hash at every page boundary, which is to say it would verify a chain with
// a hole in it every few hundred entries, at positions an attacker can compute.
type chainWalk struct {
	// expectedPrev is the hash the next entry must record as its predecessor.
	expectedPrev string
	// expectedSeq is the position the next entry must sit at.
	expectedSeq int64
	// lastSeq is the position of the last entry that verified, which is both
	// the cursor the next page reads past and what the result reports. It
	// starts at the position the walk was told to start past, so a walk that
	// verifies nothing resumes from where it began.
	lastSeq int64
	// checked counts the entries examined, the breaking one included.
	checked int64
	// anchored says whether the first page has fixed what the walk expects.
	anchored bool
	// known is false when the position before the walk's first entry exists but
	// its row does not, which is a deletion rather than an anchor.
	known bool
}

// anchorAt fixes what the walk's first entry must link to, from what anchorFor
// resolved for that position.
func (w *chainWalk) anchorAt(anchor *anchorState, firstSeq int64) {
	w.expectedPrev = anchor.prevHash
	w.expectedSeq = firstSeq
	w.known = anchor.known
	w.anchored = true
}

// page checks each entry of one page against its own content and against the
// entry before it, advancing the walk and returning the first break or nil.
func (w *chainWalk) page(stored []storedEntry) *Break {
	if !w.known {
		return &Break{Reason: BreakMissingEntry, Seq: stored[0].entry.Seq - 1}
	}

	for i := range stored {
		entry := &stored[i].entry

		w.checked++

		// Checked before the content, because a gap explains a link mismatch
		// and reporting the mismatch instead would name the wrong entry as the
		// problem.
		if entry.Seq != w.expectedSeq {
			return &Break{Reason: BreakMissingEntry, Seq: w.expectedSeq}
		}

		if entry.PrevHash != w.expectedPrev {
			return &Break{
				Reason:   BreakLinkMismatch,
				EntryID:  entry.ID,
				Seq:      entry.Seq,
				Expected: w.expectedPrev,
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

		w.expectedPrev = entry.Hash
		w.expectedSeq = entry.Seq + 1
		w.lastSeq = entry.Seq
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
		// The scope reads as prose here, not as the identifier the column holds:
		// a span attribute is read by a person, and "<global>" is legible where
		// an empty string is a field that looks unset.
		op.Set(scopeKey, q.Scope.String())
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

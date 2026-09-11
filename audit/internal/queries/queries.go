package queries

import (
	"fmt"
	"slices"
	"strings"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// The two tables audit owns, at their canonical spelling — what the emitted
// .sql names, and what audit/migrations renders at the consumer's prefix.
const (
	// EntriesTable holds the log itself: one row per recorded event, append-only
	// and hash-chained within a scope.
	EntriesTable = "audit_log_entries"
	// ChainsTable holds one row per scope: that chain's head, and how far
	// retention has pruned it. It is keyed on the scope rather than on an id,
	// which is why every statement over it is one of querygen's keyed forms.
	ChainsTable = "audit_log_chains"
)

// TableNames is every table audit owns, in the order the DDL creates them.
//
// The querygen registry is fed by the table existing rather than by something
// choosing to emit its queries, and a consumer reading that registry back to
// truncate a database between integration tests is asking which tables this
// component has rows in. Both of these do.
var TableNames = []string{EntriesTable, ChainsTable}

// The entry table's columns, named rather than spelled at each statement that
// uses one.
//
// They are constants because most of them appear in several statements — the
// scope in six predicates, the position in the prune's key, its order and two
// of its bounds — and a typo in one of those renders SQL that sqlc rejects,
// which is the good case, or that names a different column, which is not.
const (
	// SeqColumn is the entry's position in its scope's chain. It is what the
	// chain is defined by and what every read of the log in chain order sorts
	// on: two entries can share a recorded_at, so ordering by time would
	// sometimes hand a verification walk a pair in the wrong order.
	SeqColumn = "seq"
	// ScopeColumn is the tenancy boundary the entry belongs to, and the chain's
	// partition. The empty string is a real scope — the one platform-level
	// events are recorded in — which is what makes every predicate over it a
	// narrowing a caller may leave unset rather than one an empty value relaxes.
	ScopeColumn = "scope"
	// RecordedAtColumn is when the event happened, assigned by the caller and
	// covered by the entry's hash.
	//
	// It is not created_at and cannot be renamed to it. The convention's
	// created_at is database-owned — excluded from every insert this module
	// emits — and a database-assigned stamp here would store a value the digest
	// does not cover, so every entry would read back as tampered. What that
	// costs is the derived filter window, which the reads name instead: see
	// [querygen.Generator.WindowConditions].
	RecordedAtColumn = "recorded_at"
	// EventTypeColumn names what happened.
	EventTypeColumn = "event_type"
	// ResourceTypeColumn names the kind of thing acted on.
	ResourceTypeColumn = "resource_type"
	// ResourceIDColumn identifies the instance acted on.
	ResourceIDColumn = "resource_id"
	// ActorIDColumn identifies the principal responsible.
	ActorIDColumn = "actor_id"
	// ActorTypeColumn says what kind of principal that was.
	ActorTypeColumn = "actor_type"
	// ActorIPColumn is the address the action arrived from.
	ActorIPColumn = "actor_ip"
	// ChangeSetColumn holds the encoded per-field before/after, and is NULL for
	// an entry that carries none.
	ChangeSetColumn = "change_set"
	// MetadataColumn holds the encoded free-form context, and is NULL for an
	// entry that carries none.
	MetadataColumn = "metadata"
	// PrevHashColumn is the digest of the entry before this one in its scope.
	PrevHashColumn = "prev_hash"
	// HashColumn is this entry's own digest, over PrevHashColumn and the entry's
	// canonical image.
	HashColumn = "hash"
)

// The chain table's columns.
const (
	// HeadSeqColumn is the highest position the scope has issued, and -1 for a
	// chain that has been created and never written to.
	HeadSeqColumn = "head_seq"
	// HeadHashColumn is the digest at that position, and empty at -1.
	HeadHashColumn = "head_hash"
	// PrunedThroughSeqColumn is the highest position retention has removed.
	PrunedThroughSeqColumn = "pruned_through_seq"
	// PrunedThroughHashColumn is the digest that position held, which is what
	// the oldest surviving entry's link is checked against once the row it
	// named is gone.
	PrunedThroughHashColumn = "pruned_through_hash"
)

// EntryColumns is the entries table's shape, in the order every emitted SELECT
// projects it — which is also the order the generated row structs carry, and so
// the order audit's conversions restate.
//
// It is the whole row. The table carries none of the convention's three
// timestamps, each absent for its own reason — see audit/migrations — so this
// list justifies no filter window, no archived predicate and no stamp, and the
// statements rendered from it have none.
var EntryColumns = []string{
	querygen.IDColumn,
	SeqColumn,
	ScopeColumn,
	RecordedAtColumn,
	EventTypeColumn,
	ResourceTypeColumn,
	ResourceIDColumn,
	ActorIDColumn,
	ActorTypeColumn,
	ActorIPColumn,
	ChangeSetColumn,
	MetadataColumn,
	PrevHashColumn,
	HashColumn,
}

// EntryNullableColumns names the columns an insert may set to NULL: the two
// encoded blobs, absent for an entry recording neither a change set nor
// metadata. Everything else in the row is NOT NULL in the shipped DDL.
var EntryNullableColumns = []string{ChangeSetColumn, MetadataColumn}

// KeyedEntryColumns is the entries table's shape for a read keyed on something
// other than the row's own id.
//
// The id is left out and named in [querygen.Read.Projection] instead, which is
// the idiom a table carrying an id it does not key on uses: the id predicate is
// rendered from the presence of the column, so a read keyed on (scope, seq)
// that handed over the full list would key on the id as well and answer
// nothing.
var KeyedEntryColumns = withoutID(EntryColumns)

// ChainColumns is the whole chain row, in the order the DDL declares it.
//
// Nothing renders a statement from this list — the statements take
// [ChainStateColumns] — and it is here for the cross-check against the shipped
// DDL, which is the one place a column added to the schema and not to this
// package stops being invisible.
var ChainColumns = []string{
	ScopeColumn,
	HeadSeqColumn,
	HeadHashColumn,
	PrunedThroughSeqColumn,
	PrunedThroughHashColumn,
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	querygen.ArchivedAtColumn,
}

// ChainStateColumns is the shape the chain's statements are rendered from.
//
// It is the row less two columns, and each omission decides something.
// archived_at is out because no statement here writes it — suppressing a
// scope's tamper-evidence is not an operation this package offers — and a
// column list carrying it would give every read and every write a predicate
// excluding rows nothing can create. created_at is out because the database
// owns it: the genesis row supplies the scope and nothing else, and the column's
// DEFAULT is what stamps when the chain began.
//
// last_updated_at is in, and that is what makes both writes stamp it from the
// server's clock rather than from a clock a caller passes. The column is
// bookkeeping — nothing compares it, and nothing hashes it — so the reason to
// bind an application clock here does not apply, and the reason not to does: two
// clocks writing one column is two answers to when the row last moved.
var ChainStateColumns = []string{
	ScopeColumn,
	HeadSeqColumn,
	HeadHashColumn,
	PrunedThroughSeqColumn,
	PrunedThroughHashColumn,
	querygen.LastUpdatedAtColumn,
}

// ChainStateProjection is what the two chain reads return: the head a writer
// chains onto, and the watermark a verifier anchors across.
//
// Both statements project it, because they are one question asked under two
// locks — see [LockChainQuery].
var ChainStateProjection = []string{
	HeadSeqColumn,
	HeadHashColumn,
	PrunedThroughSeqColumn,
	PrunedThroughHashColumn,
}

// ChainInsertColumns is what the genesis row supplies, which is the scope.
//
// Every other column of a new chain has a DEFAULT that says what it means: the
// head and the prune watermark are -1 and empty, which is "this chain has
// issued nothing and lost nothing", and created_at is the server's clock. A
// caller binding those would be binding four constants, and a constant bound
// per call is a constant that can be bound wrongly.
var ChainInsertColumns = []string{ScopeColumn}

// ChainHeadColumns is what advancing a chain assigns: the position the last
// entry took and the digest it carried.
var ChainHeadColumns = []string{HeadSeqColumn, HeadHashColumn}

// ChainPruneColumns is what a retention pass assigns: how far it removed, and
// the digest of the last entry it removed.
var ChainPruneColumns = []string{PrunedThroughSeqColumn, PrunedThroughHashColumn}

// The arguments the emitted statements bind, beyond the ones named for their
// own column and the filter arguments querygen spells.
const (
	// HorizonArg is the instant a retention pass runs to: an entry recorded at
	// or before it is one this sweep may remove. Three statements compare
	// against it — the scope listing, the backlog count, and the bounds a
	// scope's pass is computed from — and they have to agree, or a row sits in
	// the backlog that no sweep will ever take.
	HorizonArg = "horizon"
	// BoundaryArg is the highest position a scope's pass may remove, computed
	// from the budget it has left and the first entry that must survive.
	BoundaryArg = "boundary"
	// ThroughSeqArg is the position the DELETE removes through, which is the
	// position of the row the boundary actually resolved to.
	ThroughSeqArg = "through_seq"
	// ScopesArg is the set of whole scopes an erasure removes, bound as one
	// argument.
	ScopesArg = "scopes"
	// SubjectIDArg is the data subject an erasure counts the surviving mentions
	// of. It is one argument compared against two columns, because a subject
	// appearing as the actor and a subject appearing as the resource are the
	// same person named twice.
	SubjectIDArg = "subject_id"

	// RecordedAfterArg and RecordedBeforeArg bound the range a verification
	// walks. They are not the filter window's arguments, because they are not a
	// filtering.QueryFilter: Verify takes two times of its own, and naming them
	// created_after would be naming a field the caller never filled in.
	RecordedAfterArg  = "recorded_after"
	RecordedBeforeArg = "recorded_before"
)

// The aliases the aggregate read projects. They are spelled here because the
// store reads the generated fields named from them.
const (
	// OldestSeqAlias is the lowest position a scope still holds.
	OldestSeqAlias = "oldest_seq"
	// FirstKeptSeqAlias is the lowest position a scope holds that was recorded
	// after the horizon — the first entry that must survive this pass.
	FirstKeptSeqAlias = "first_kept_seq"
)

// The names the emitted statements carry. A query name is a Go method name on
// the generated querier, so these are what audit's recorder, reader, prune
// target and eraser call.
const (
	ChainQuery              = "GetAuditChain"
	LockChainQuery          = "LockAuditChain"
	CreateChainQuery        = "CreateAuditChain"
	AdvanceChainHeadQuery   = "AdvanceAuditChainHead"
	RecordChainPruneQuery   = "RecordAuditChainPrune"
	InsertEntryQuery        = "InsertAuditLogEntry"
	GetEntryQuery           = "GetAuditLogEntry"
	GetEntryBySeqQuery      = "GetAuditLogEntryBySeq"
	ListEntriesQuery        = "ListAuditLogEntries"
	ListChainEntriesQuery   = "ListAuditChainEntries"
	PrunableScopesQuery     = "ListPrunableAuditScopes"
	PrunableScopesFromQuery = "ListPrunableAuditScopesAfter"
	BacklogQuery            = "CountPrunableAuditEntries"
	PruneBoundsQuery        = "GetAuditPruneBounds"
	PruneTargetQuery        = "GetAuditPruneTarget"
	PruneEntriesQuery       = "PruneAuditLogEntries"
	EraseEntriesQuery       = "DeleteAuditLogEntriesInScopes"
	EraseChainsQuery        = "DeleteAuditChainsInScopes"
	SubjectMentionsQuery    = "CountAuditLogEntriesForSubject"
)

// Render returns the canonical sqlc input for d: every statement audit
// executes, and the three dataprivacy/auditerasure executes against audit's
// tables, in one file's worth of text.
//
// The eraser's three are here rather than in a corpus of their own because it
// owns no table: its two deletes and its count address the tables this package
// ships the migrations for, and a second corpus over somebody else's schema
// would be a second place a column rename has to be noticed.
//
// It is what audit/internal/queriesgen writes to the .sql beside this file and
// what CI regenerates to check the committed copy still matches. That .sql is
// sqlc-gen-unison's input, so what the stores execute is this text exactly: the
// generated auditdb package carries it per dialect, with the consumer's table
// prefix substituted once at construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	querygen.RegisterTable(TableNames...)

	rendered := chainStatements(g)
	rendered = append(rendered, entryStatements(g)...)
	rendered = append(rendered, retentionStatements(g)...)
	rendered = append(rendered, erasureStatements(g)...)

	return querygen.RenderFile(rendered)
}

// FileName is the canonical .sql this package renders for d.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}

// chainStatements renders the five statements over the per-scope chain row.
//
// Four of the five are querygen's keyed forms with the scope where an id would
// go, which is what a natural key costs a corpus: the pair of writes and the
// unlocked read name the column and nothing else changes. The fifth is that
// read again under a row lock, which is the one thing about this table no
// generator here can say.
func chainStatements(g *querygen.Generator) []*querygen.Query {
	return []*querygen.Query{
		g.ReadQuery(ChainQuery, ChainsTable, ChainStateColumns,
			querygen.Read{Projection: ChainStateProjection}, scopeMatch()),

		lockChainQuery(g),

		// The row already there wins, unchanged, and the count says so. Two
		// transactions recording into a scope for the first time would
		// otherwise both insert a genesis row and the loser would fail on the
		// primary key — taking a caller's business transaction down with it, on
		// nothing worse than being second.
		g.InsertIgnoreQuery(CreateChainQuery, ChainsTable,
			ChainInsertColumns, nil, scopeMatch()),

		g.UpdateQuery(AdvanceChainHeadQuery, ChainsTable,
			ChainStateColumns, ChainHeadColumns, nil, scopeMatch()),

		g.UpdateQuery(RecordChainPruneQuery, ChainsTable,
			ChainStateColumns, ChainPruneColumns, nil, scopeMatch()),
	}
}

// lockChainQuery renders the chain read a writer takes, which is the read above
// holding the row for the rest of the caller's transaction.
//
// The lock is the point of the statement and the read is incidental. Two
// transactions recording into one scope would otherwise both read the same head
// and both compute the same next position; the unique index refuses the second,
// which takes down a business transaction whose only mistake was arriving
// second. Holding the row makes that writer wait and then read the head the
// first one committed — and a head cached in the process could not do it, since
// what is wanted is not the value but the wait.
//
// It is a second named statement rather than a flag on the first because a
// clause is statement text on every server here, the same reason a paged list
// is two statements rather than one that takes a direction. Postgres and MySQL
// both take the row lock; SQLite has neither the clause nor the concurrency it
// exists for, admitting one writer at a time by construction, so its arm is the
// unlocked read and that is correct there rather than missing.
func lockChainQuery(g *querygen.Generator) *querygen.Query {
	read := g.ReadQuery(LockChainQuery, ChainsTable, ChainStateColumns,
		querygen.Read{Projection: ChainStateProjection}, scopeMatch())

	if suffix := rowLockSuffix(g); suffix != "" {
		read.Content = strings.TrimSuffix(read.Content, ";") + suffix + ";"
	}

	return read
}

// rowLockSuffix is the clause that holds a read row for the rest of the
// transaction, on the dialects that have one.
//
// It selects the same two dialects as dialect.Dialect.SupportsSkipLocked and is
// written out separately anyway: that method answers whether competing workers
// can skip past locked rows, which is a different question that only
// coincidentally has the same answer today.
func rowLockSuffix(g *querygen.Generator) string {
	switch g.Dialect() {
	case dialect.Postgres, dialect.MySQL:
		return "\nFOR UPDATE"
	default:
		return ""
	}
}

// entryStatements renders the four statements over the log itself: the write,
// the two single-entry reads, and the paged list in both directions.
func entryStatements(g *querygen.Generator) []*querygen.Query {
	rendered := []*querygen.Query{
		// One statement per entry, where this package used to assemble a
		// multi-row VALUES list capped at seventy rows by SQLite's parameter
		// ceiling. The multi-row form's shape is the caller's cardinality, so
		// it has no static text for sqlc to check — and the cap that made it
		// safe was arithmetic over a column count nothing verified.
		g.InsertQuery(InsertEntryQuery, EntriesTable, EntryColumns, EntryNullableColumns),

		g.GetQuery(GetEntryQuery, EntriesTable, EntryColumns),

		// Keyed on the position rather than the id, which is how a verification
		// anchors a range beginning mid-chain: the entry before the first one in
		// range is what the first one's link is checked against.
		g.ReadQuery(GetEntryBySeqQuery, EntriesTable, KeyedEntryColumns,
			querygen.Read{Projection: EntryColumns}, scopeMatch(), querygen.Match{Column: SeqColumn}),

		chainRangeQuery(g),
	}

	return append(rendered, listings(g)...)
}

// listings renders the paged read in both directions.
//
// A paged list is two statements because a direction is which way the ORDER BY
// runs and which way the cursor comparison points — statement text on all three
// servers, with no expression that takes a bound value and orders by it. What
// the store does with a filter's SortBy is choose between them.
//
// It is written out here rather than emitted by
// [querygen.Generator.ListQueries] for one reason, and it is the window. That
// shape derives the created_after/created_before pair from the presence of
// created_at, and this table has no created_at to derive it from: what a reader
// filters on is recorded_at, which the caller assigns and the hash covers. So
// the window is named rather than derived, through the fragment that spells the
// sentinel an unset bound coalesces to — see
// [querygen.Generator.WindowConditions].
//
// Everything else about it is querygen's own listing, assembled from the
// fragments it exports: the six selectors as optional narrowings, the keyset
// predicate, the two counts riding on the rows, and the page-size clause each
// dialect spells its own way.
func listings(g *querygen.Generator) []*querygen.Query {
	narrowings := g.MatchConditions(EntriesTable, selectors()...)

	// The window rides in the page and in the count of what the filter matched,
	// and not in the count of everything in scope. That is what the two counts
	// mean: filtered_count answers "how many rows match this filter" and
	// total_count answers "how many are there to filter", so a total that
	// shrank as a caller narrowed the window would be a progress bar measuring
	// its own progress.
	filtered := slices.Concat(narrowings, entryWindow(g, querygen.CreatedAfterArg, querygen.CreatedBeforeArg))

	rendered := make([]*querygen.Query, 0, 2)

	for _, direction := range []querygen.Direction{querygen.Ascending, querygen.Descending} {
		name := ListEntriesQuery
		if direction == querygen.Descending {
			name = querygen.DescendingName(name)
		}

		rendered = append(rendered, &querygen.Query{
			Annotation: querygen.QueryAnnotation{Name: name, Type: querygen.ManyType},
			Content: fmt.Sprintf("SELECT\n\t%s,\n\t%s,\n\t%s\nFROM %s\nWHERE %s\n%s;",
				strings.Join(querygen.QualifyAll(EntriesTable, EntryColumns), ",\n\t"),
				g.FilterCountSelect(EntriesTable, EntryColumns, nil, filtered...),
				g.TotalCountSelect(EntriesTable, EntryColumns, nil, narrowings...),
				EntriesTable,
				g.FilterConditions(EntriesTable, EntryColumns, direction, filtered...),
				g.CursorLimitClause(EntriesTable, direction),
			),
		})
	}

	return rendered
}

// SelectorArgSuffix is what a selector's argument name adds to its column's.
//
// A narrowing is not the column's value, and here the two cannot even share a
// name. The predicate compares an argument against the column and, in its other
// arm, against NULL — and that second arm is a bare cast, which MySQL's
// analyzer resolves to a string type of its own while the other two resolve the
// column's. Under one name the row's scope and the filter over it would be one
// argument the three engines disagree about the type of; under two they are two
// arguments, one of which is declared in audit's unison.yaml and one of which
// is the column.
//
// It is also the more honest reading. The row holds a scope; the caller supplies
// "this scope, or every scope", which is a different thing that happens to be
// compared against it.
const SelectorArgSuffix = "_filter"

// SelectorColumns are the columns audit.Query narrows on, in the order the
// generated params carry them.
var SelectorColumns = []string{
	ScopeColumn,
	ActorIDColumn,
	ActorTypeColumn,
	ResourceIDColumn,
	ResourceTypeColumn,
	EventTypeColumn,
}

// SelectorArg is the argument one selector binds through.
func SelectorArg(column string) string { return column + SelectorArgSuffix }

// selectors is the audit.Query expressed as predicates: six columns a caller
// may narrow on, each of which an absent argument leaves alone.
//
// They are optional narrowings rather than enumerated statements because the
// six are independent — sixty-four statements, a hundred and twenty-eight once
// each is emitted in both directions — and they are narrowings rather than
// optional arguments because an absent one must not narrow to the sentinel. The
// scope is the case that decides it: the empty string is the chain
// platform-level events are recorded in, so reading an absent scope as "the
// empty one" would answer a console asking for everything with the platform's
// own events, and reading it as "no filter" is the only reading that leaves
// "only the platform's" expressible.
func selectors() []querygen.Match {
	matches := make([]querygen.Match, 0, len(SelectorColumns))
	for _, column := range SelectorColumns {
		matches = append(matches, querygen.Match{
			Column:  column,
			Arg:     SelectorArg(column),
			Against: querygen.OptionalNarrowing,
		})
	}

	return matches
}

// chainRangeQuery renders a verification's walk: one scope's entries within a
// time window, in chain order.
//
// Ordered by seq rather than recorded_at, and the two are not interchangeable.
// The chain is defined by position, and two entries can share a timestamp — the
// clock has microsecond resolution and a transaction can record several entries
// at once — so ordering by time would sometimes hand the walk a pair in the
// wrong order and report an intact chain as broken.
//
// It takes no page size, which is the one read here that does not. A
// verification that stopped at a page boundary would report the chain intact as
// far as it looked, which is the answer nobody asked for; the range is the
// caller's bound, and the window is how they set it.
func chainRangeQuery(g *querygen.Generator) *querygen.Query {
	predicates := slices.Concat(
		g.MatchConditions(EntriesTable, scopeMatch()),
		entryWindow(g, RecordedAfterArg, RecordedBeforeArg),
	)

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: ListChainEntriesQuery, Type: querygen.ManyType},
		Content: fmt.Sprintf("SELECT\n\t%s\nFROM %s\nWHERE %s\nORDER BY %s;",
			strings.Join(querygen.QualifyAll(EntriesTable, EntryColumns), ",\n\t"),
			EntriesTable,
			strings.Join(predicates, "\n\tAND "),
			querygen.Qualify(EntriesTable, SeqColumn),
		),
	}
}

// entryWindow is the time window over recorded_at, under argument names the
// statement it lands in chooses.
//
// One rendering, two namings. A paged list's window comes from a
// filtering.QueryFilter and binds the names that filter binds everywhere else
// in this module; a verification's comes from two parameters of its own and
// binds names a reader of that method recognizes. What must not differ is the
// predicate, since both answer the same question about the same column.
func entryWindow(g *querygen.Generator, afterArg, beforeArg string) []string {
	return g.WindowConditions(querygen.Qualify(EntriesTable, RecordedAtColumn), afterArg, beforeArg)
}

// retentionStatements renders the five reads and the one write a retention pass
// runs.
//
// Three of the six are written out, and each is a shape querygen has no
// spelling for rather than one it declined to render: a DISTINCT over a column,
// a count bounded by a subquery, and a pair of aggregates one of which carries
// its own predicate. The other three are its keyed forms and its prune.
func retentionStatements(g *querygen.Generator) []*querygen.Query {
	return []*querygen.Query{
		prunableScopesQuery(g, false),
		prunableScopesQuery(g, true),
		backlogQuery(g),
		pruneBoundsQuery(g),

		// The highest position at or below the boundary rather than the
		// boundary itself, so a chain with a hole in it — which is to say one
		// that has already been tampered with — still yields a real row rather
		// than nothing. Its hash becomes the scope's new prune watermark.
		//
		// The order is descending, which is what puts this statement here
		// rather than in querygen's keyed forms: [querygen.Read.Order] sorts
		// ascending, because the read it exists for is "the first row this key
		// admits" and there is no second reading of first.
		pruneTargetQuery(g),

		// The DELETE the three reads above feed, capped. A scope nobody has
		// swept for a month holds a month of entries past its horizon, and the
		// statement that clears them in one pass holds locks for minutes and
		// times out somewhere in the middle — after which the next attempt
		// starts from the beginning.
		//
		// Keyed on (scope, seq) rather than on the id: that pair is the unique
		// index the chain's structure rests on, and a prefix of a chain is what
		// this statement removes.
		g.PruneQuery(PruneEntriesQuery, EntriesTable,
			querygen.Prune{
				Key:   []string{ScopeColumn, SeqColumn},
				Order: []querygen.Order{{Column: SeqColumn}},
			},
			scopeMatch(),
			querygen.Match{Column: SeqColumn, Against: querygen.AtMostArgument, Arg: ThroughSeqArg}),
	}
}

// prunableScopesQuery renders the retention sweep's first question: which
// scopes hold anything old enough to prune.
//
// It pages by scope rather than capping how many a sweep may see, because the
// count it returns is what tells the sweep whether it has run out of work — a
// page short of the limit means there is nothing behind it. The cursor is the
// last scope of the previous page, which is a keyset and not an offset, so a
// scope written behind the cursor while the batch runs cannot displace one that
// has not been visited yet.
//
// The first page and the pages after it are two named statements rather than
// one carrying an optional cursor, and the empty scope is why. Every other
// keyset walk in this module compares against an id, and no id is empty, so an
// absent cursor coalesces to the empty string and admits every row. Here the
// empty string is a scope — the one platform-level events are recorded in — so
// that reading would place the first page just past it, and the log's own
// events would be the ones no sweep ever visited. Enumerating the two is what
// leaves both checked.
func prunableScopesQuery(g *querygen.Generator, after bool) *querygen.Query {
	name := PrunableScopesQuery
	matches := []querygen.Match{horizonMatch()}

	if after {
		name = PrunableScopesFromQuery
		matches = append(matches,
			querygen.Match{
				Column:  ScopeColumn,
				Against: querygen.AtMostArgument,
				Arg:     querygen.CursorArg,
				Exclude: true,
			})
	}

	scope := querygen.Qualify(EntriesTable, ScopeColumn)

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: name, Type: querygen.ManyType},
		Content: fmt.Sprintf("SELECT DISTINCT %s\nFROM %s\nWHERE %s\nORDER BY %s\n%s;",
			scope,
			EntriesTable,
			strings.Join(g.MatchConditions(EntriesTable, matches...), "\n\tAND "),
			scope,
			g.LimitClause(),
		),
	}
}

// backlogQuery renders the retention backlog reading: how many entries are at
// or before the horizon, saturating at a ceiling the caller binds.
//
// Bounded by a subquery rather than counted outright, so the cost of the
// reading does not grow with the size of the problem it reports — which would
// make it most expensive exactly when somebody most needs it. The alias is not
// decoration: Postgres and MySQL both require a derived table to have one.
//
// The ceiling binds under the page size's own name, because "how many rows may
// this statement touch" is one question however the statement got there — and
// on MySQL that name is attached to a bare marker, so the clause has to be the
// last bound value in the statement. It is.
func backlogQuery(g *querygen.Generator) *querygen.Query {
	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: BacklogQuery, Type: querygen.OneType},
		Content: fmt.Sprintf("SELECT COUNT(*)\nFROM (\n\tSELECT 1\n\tFROM %s\n\tWHERE %s\n\t%s\n) AS audit_prune_backlog;",
			EntriesTable,
			strings.Join(g.MatchConditions(EntriesTable, horizonMatch()), "\n\t\tAND "),
			g.LimitClause(),
		),
	}
}

// pruneBoundsQuery renders the two positions that decide what a pass may remove
// from one scope: the oldest entry it still holds, and the oldest entry that
// must survive the horizon.
//
// Both come from one statement over one index range rather than two, and the
// CASE expression is what allows it — a second aggregate cannot carry its own
// WHERE clause, but it can be fed a NULL for every row the predicate excludes.
// That is an expression rather than a comparison, which is what keeps this
// statement written out: querygen's closed set of comparands exists to refuse an
// expression language, and an aggregate over a conditional is squarely one.
//
// The predicate is strictly after the horizon, because an entry recorded
// exactly at it is one this sweep may remove — the same at-or-before reading
// the scope listing and the backlog count use.
//
// The second value is the reason a pass is expressed in positions at all rather
// than deleting by timestamp directly. recorded_at comes from the recording
// process's clock, so across several processes it is not perfectly ordered with
// respect to seq; deleting every row older than the horizon could therefore
// punch a hole in the middle of a chain, which is indistinguishable from the
// tampering this package exists to detect. Pruning strictly below the first
// entry that must survive keeps the survivors a contiguous suffix, always.
func pruneBoundsQuery(g *querygen.Generator) *querygen.Query {
	seq := querygen.Qualify(EntriesTable, SeqColumn)

	kept := g.MatchConditions(EntriesTable,
		querygen.Match{Column: RecordedAtColumn, Against: querygen.AtMostArgument, Arg: HorizonArg, Exclude: true})

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: PruneBoundsQuery, Type: querygen.OneType},
		Content: fmt.Sprintf("SELECT\n\tMIN(%s) AS %s,\n\tMIN(CASE WHEN %s THEN %s END) AS %s\nFROM %s\nWHERE %s;",
			seq, OldestSeqAlias,
			strings.Join(kept, " AND "), seq, FirstKeptSeqAlias,
			EntriesTable,
			strings.Join(g.MatchConditions(EntriesTable, scopeMatch()), "\n\tAND "),
		),
	}
}

// pruneTargetQuery renders the read of the last entry a pass will remove.
func pruneTargetQuery(g *querygen.Generator) *querygen.Query {
	predicates := g.MatchConditions(EntriesTable,
		scopeMatch(),
		querygen.Match{Column: SeqColumn, Against: querygen.AtMostArgument, Arg: BoundaryArg})

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: PruneTargetQuery, Type: querygen.OneType},
		Content: fmt.Sprintf("SELECT\n\t%s,\n\t%s\nFROM %s\nWHERE %s\nORDER BY %s DESC\nLIMIT 1;",
			querygen.Qualify(EntriesTable, SeqColumn),
			querygen.Qualify(EntriesTable, HashColumn),
			EntriesTable,
			strings.Join(predicates, "\n\tAND "),
			querygen.Qualify(EntriesTable, SeqColumn),
		),
	}
}

// erasureStatements renders the three statements dataprivacy/auditerasure runs
// against these tables.
//
// They live in this corpus because that package owns no table. Its subject
// matter is the audit log's schema, so a corpus of its own would be a second
// place a column rename here has to be noticed — and the eraser binds the
// querier it is handed to the erasure transaction, so the statements execute
// under the same guarantee everything else here does.
//
// Two deletes and a count, and the division between them is the hash chain's.
// Whole scopes belonging to the subject go, chain row and all, because a chain
// that disappears entirely leaves no gap in any surviving chain. Entries
// elsewhere in which the subject appears only as the actor or the resource
// cannot go — removing one would make every later verification of that scope
// report tampering — so they are counted, and the number is what the subject is
// told.
func erasureStatements(g *querygen.Generator) []*querygen.Query {
	return []*querygen.Query{
		scopeErasureQuery(g, EraseEntriesQuery, EntriesTable, querygen.ExecRowsType),
		scopeErasureQuery(g, EraseChainsQuery, ChainsTable, querygen.ExecType),
		subjectMentionsQuery(g),
	}
}

// scopeErasureQuery renders the delete of every row in a bound set of scopes.
//
// It is written out rather than emitted because [querygen.Generator.DeleteQuery]
// keys on equalities and this one keys on a set — a subject may own several
// scopes, and one statement per scope inside an erasure transaction is a round
// trip per tenant the subject ever belonged to.
//
// The set is the last bound value in the statement, which is a requirement
// rather than a layout choice: an expanded set is a run of bare markers, SQLite
// numbers a bare marker one past the highest it has seen, and an argument bound
// after one collides with an element of the set. There is nothing else bound
// here, so the requirement is met by there being nothing to get wrong.
//
// The entries' count is the answer a subject is given, so that statement reports
// its rows; the chains' is not, since a scope with no entries and no chain row
// is the same erasure as one that had both.
func scopeErasureQuery(g *querygen.Generator, name, table string, annotation querygen.QueryType) *querygen.Query {
	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: name, Type: annotation},
		Content: fmt.Sprintf("DELETE FROM %s\nWHERE %s;",
			table,
			g.SetCondition(ScopeColumn, ScopesArg),
		),
	}
}

// subjectMentionsQuery renders the count of entries the chain will not let go
// of: the ones where the subject acted inside somebody else's scope, or was the
// thing acted on.
//
// The two columns are a disjunction rather than two statements, because the
// number the subject is owed is of entries and not of mentions — an entry where
// they were both the actor and the resource is one entry, and two counts added
// together would report it twice.
//
// It is counted rather than sampled: the number is what goes in front of the
// subject, and "some" is not an answer.
func subjectMentionsQuery(g *querygen.Generator) *querygen.Query {
	mentions := g.MatchConditions(EntriesTable,
		querygen.Match{Column: ActorIDColumn, Arg: SubjectIDArg},
		querygen.Match{Column: ResourceIDColumn, Arg: SubjectIDArg})

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: SubjectMentionsQuery, Type: querygen.OneType},
		Content: fmt.Sprintf("SELECT COUNT(*)\nFROM %s\nWHERE %s;",
			EntriesTable,
			strings.Join(mentions, "\n\tOR "),
		),
	}
}

// scopeMatch is the predicate that addresses one chain, and one scope's worth
// of entries.
//
// It is a function rather than a package-level value because every caller
// appends to what it returns, and a shared slice appended to is a slice whose
// backing array two statements can come to share.
func scopeMatch() querygen.Match {
	return querygen.Match{Column: ScopeColumn}
}

// horizonMatch is the retention predicate: recorded at or before the instant
// this pass runs to.
func horizonMatch() querygen.Match {
	return querygen.Match{Column: RecordedAtColumn, Against: querygen.AtMostArgument, Arg: HorizonArg}
}

// withoutID returns the column list less the id, for the reads keyed on
// something else.
func withoutID(columns []string) []string {
	kept := make([]string, 0, len(columns))

	for _, column := range columns {
		if column != querygen.IDColumn {
			kept = append(kept, column)
		}
	}

	return kept
}

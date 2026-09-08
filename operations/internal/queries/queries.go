package queries

import (
	"fmt"
	"strings"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// OperationsTable is the one table this package owns, at its canonical
// unprefixed spelling — what the emitted .sql names, and what a consumer's own
// prefix is rendered onto.
const OperationsTable = "operations"

// TableNames is every table operations owns, in the order the DDL creates them.
//
// One, and it is still a list. The registry a consumer reads back to truncate a
// database has to be fed by the table existing rather than by something
// choosing to emit its queries — see querygen's own comment on the trap — and a
// list of one is the shape that survives a second table arriving.
var TableNames = []string{OperationsTable}

// The columns this package's own statements name, spelled here so the corpus
// and the store cannot come to disagree about them.
const (
	// StateColumn is the operation's position in its lifecycle, and the column
	// every transition below guards on. It is the closed set operations.State
	// enumerates, which is what makes the listing's state filter a bound set
	// rather than an optional narrowing — see [Render].
	StateColumn = "state"
	// OwnerColumn is the opaque owner an API listing is scoped by. Compared
	// only for equality; this package never parses it.
	OwnerColumn = "owner"
	// KindColumn names the registered work an operation is an instance of.
	KindColumn = "kind"
	// RequestColumn holds the encoded input the Runner was started with. It is
	// the one nullable member of the insert: "no request" and "an empty
	// request" are different statements about the operation, and a Runner that
	// branches on one should not be handed the other.
	RequestColumn = "request"
)

// The sqlc arguments the listing binds beyond filtering's own. They are named
// here because the store binds them and the statements read them, and a name
// spelled in two places is a name that can differ in one.
const (
	// StatesArg is the set of states a listing is narrowed to. It is never
	// empty: a caller asking for every state binds every state, which is
	// expressible because the domain is closed.
	StatesArg = "states"
)

// Columns is every column an operation read projects, in the order it projects
// them.
//
// It is the order the generated row types carry, so it is also the order the
// store's conversion reads. A column added here reaches the projections, the
// generated types and the conversion together; a column added to a projection
// alone is not something there is a projection to add it to.
//
// archived_at and claimed_until are absent, and each absence is load-bearing.
// Nothing in this package archives an operation — a terminal row is reaped
// rather than hidden, which is what the retention sweep is for — so a column
// list carrying archived_at would have querygen render an archived predicate on
// every read and an include_archived toggle on the listing, over a column no
// statement here ever writes. claimed_until is the lease, which is this
// package's own bookkeeping rather than an answer a client is owed: it decides
// who may claim an operation and it is never shown to anybody.
var Columns = []string{
	querygen.IDColumn,
	KindColumn,
	StateColumn,
	OwnerColumn,
	RequestColumn,
	"units_total",
	"units_done",
	"progress_unit",
	"progress_count",
	"count_label",
	"progress_message",
	"result_uri",
	"result_detail",
	"error_code",
	"error_message",
	"error_retryable",
	"revision",
	"attempts",
	"cancel_requested",
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	"started_at",
	"finished_at",
}

// InsertColumns is what the create supplies values for.
//
// Everything else the row starts with is the schema's DEFAULT rather than a
// value this process sends: the revision starts at one, created_at is the
// server's clock, and claimed_until starts at the epoch that means "never
// leased". Binding any of them would put the writer's idea of the time into a
// column the recovery sweep compares against the server's.
func InsertColumns() []string {
	return []string{
		querygen.IDColumn,
		KindColumn,
		StateColumn,
		OwnerColumn,
		RequestColumn,
		"count_label",
	}
}

// Render returns the canonical sqlc input for one dialect.
//
// It takes the dialect and serves one, which is not a contradiction: the roster
// is a property of unison.yaml and of the schema operations/migrations ships,
// and this signature is what a second dialect arriving would be a schema
// question rather than a rewrite. What it will not do is answer for a dialect
// this package has no schema for — the transitions below are written in
// Postgres and would be handed back unchanged, which is the one failure a
// generator can have that produces a plausible file.
//
// It panics rather than returning an error, in the manner of the generator it
// renders through: the argument is a constant in a generator binary. The panic
// value is an error wrapping dialect.ErrUnsupported.
func Render(d dialect.Dialect) string {
	if err := dialect.RequirePostgres("operations queries", d); err != nil {
		panic(err)
	}

	g := querygen.For(d)

	querygen.RegisterTable(TableNames...)

	rendered := []*querygen.Query{
		g.GetQuery("GetOperation", OperationsTable, Columns),
		g.SetReadQuery("GetOperations", OperationsTable, Columns,
			querygen.Read{}, querygen.SetKey{Column: querygen.IDColumn}),
	}

	rendered = append(rendered, listQueries(g)...)
	rendered = append(rendered, transitions()...)

	return querygen.RenderFile(rendered)
}

// listQueries is the paged read behind "what has this account got running", in
// both directions.
//
// Its three narrowings are two shapes rather than one, and which shape a column
// gets is decided by whether its domain is closed. Owner and kind are open sets
// — an owner is whatever the application says it is, a kind is whatever was
// registered — so each is an optional narrowing a caller may leave off, and
// leaving it off compares against nothing. State is the closed set
// operations.State enumerates, so the filter is a bound set and "every state"
// is expressible as a value: the store binds all five rather than binding
// nothing, which is what keeps the empty set meaning what it says everywhere
// else in the module.
func listQueries(g *querygen.Generator) []*querygen.Query {
	return g.SetListQueries("ListOperations", OperationsTable, Columns,
		querygen.SetKey{Column: StateColumn, Arg: StatesArg},
		querygen.Match{Column: OwnerColumn, Against: querygen.OptionalNarrowing},
		querygen.Match{Column: KindColumn, Against: querygen.OptionalNarrowing},
	)
}

// transitions is every statement that moves an operation, plus the two reads
// the fleet's own machinery runs.
//
// They are written out rather than rendered from a shape, and that is the line
// this package draws rather than a corner cut. querygen assigns bound values: a
// column and the argument it takes, with last_updated_at stamped by convention.
// Not one of the writes below is that statement. Every one of them assigns an
// expression — the revision counter that a watcher decides freshness by, the
// lease horizon a duration is turned into server-side, the monotonic floor that
// keeps a straggler flush from walking a client's progress backwards, the
// conditional cancellation that resolves in the same statement that requests it
// — and a generator that could express those would be a generator with an
// expression language in it. querygen's own comment on its closed comparand set
// is the same ruling read from the other end: a statement outside the set is one
// checked by a person rather than one the generator learns to guess at.
//
// What they do not give up is the guarantee. Each is a complete statement in the
// committed corpus, checked by sqlc against this package's own schema with no
// database running, and executed through the querier sqlc-gen-unison generates
// from it — so a renamed column is a failed generate here exactly as it is for
// the reads above.
//
// Postgres, and only Postgres. See this package's doc.
func transitions() []*querygen.Query {
	return []*querygen.Query{
		{Annotation: querygen.QueryAnnotation{Name: "CreateOperation", Type: querygen.OneType},
			Content: createOperation()},
		{Annotation: querygen.QueryAnnotation{Name: "BeginOperation", Type: querygen.OneType},
			Content: beginOperation()},
		{Annotation: querygen.QueryAnnotation{Name: "RecordOperationProgress", Type: querygen.OneType},
			Content: recordOperationProgress},
		{Annotation: querygen.QueryAnnotation{Name: "FinishOperation", Type: querygen.ExecRowsType},
			Content: finishOperation(false)},
		{Annotation: querygen.QueryAnnotation{Name: "FinishOperationWithEveryUnitDone", Type: querygen.ExecRowsType},
			Content: finishOperation(true)},
		{Annotation: querygen.QueryAnnotation{Name: "ReleaseOperation", Type: querygen.ExecRowsType},
			Content: releaseOperation},
		{Annotation: querygen.QueryAnnotation{Name: "RequestOperationCancel", Type: querygen.ExecRowsType},
			Content: requestOperationCancel},
		{Annotation: querygen.QueryAnnotation{Name: "ListStrandedOperations", Type: querygen.ManyType},
			Content: listStrandedOperations()},
		{Annotation: querygen.QueryAnnotation{Name: "ReapOperations", Type: querygen.ExecRowsType},
			Content: reapOperations},
	}
}

// createOperation records a new operation, and yields nothing when the id is
// already taken.
//
// ON CONFLICT DO NOTHING rather than letting the primary key raise. A raised
// unique violation aborts the surrounding transaction, so a caller writing
// under an id they derived would lose every write they had made alongside it;
// DO NOTHING returns no rows instead and leaves the transaction healthy, which
// is what makes the idempotency seam usable from where it is most wanted.
//
// It RETURNs the row it wrote, which is not an optimization but a requirement.
// The insert may be inside a transaction the caller has not committed, so a
// separate read would go out on another connection and find nothing — and the
// timestamps and the revision a caller is handed have to be the server's rather
// than a hopeful reconstruction of them.
func createOperation() string {
	return fmt.Sprintf(`INSERT INTO %s (
	%s
) VALUES (
	%s
)
ON CONFLICT (%s) DO NOTHING
RETURNING
	%s`,
		OperationsTable,
		strings.Join(InsertColumns(), ",\n\t"),
		strings.Join(insertBindings(), ",\n\t"),
		querygen.IDColumn,
		returning(),
	)
}

// beginOperation is the transition a worker makes when it picks an operation
// up: pending or lapsed-running becomes running, under a fresh lease.
//
// This UPDATE is the package's real mutual exclusion, and it is worth being
// precise about why it rather than the work queue's lease. The queue leases the
// key — it says who was handed the dispatch — and its lease cannot be extended
// while long work runs. This row's lease can be, by every progress flush, which
// is the only way a lease can track work whose length is not known in advance.
// So the queue's expiry costs a wasted claim, and this predicate is what makes
// that waste rather than a second execution.
//
// attempts is written from the queue's own count rather than incremented here,
// so there is one attempt counter in the system and it is the one the claim
// incremented server-side.
//
// started_at is set only on the first pass. A reclaimed operation has been
// running since the first worker picked it up, and moving the timestamp would
// erase the fact that this is the second attempt at something that started ten
// minutes ago.
func beginOperation() string {
	return fmt.Sprintf(`UPDATE %s SET
	state = sqlc.arg(running_state),
	attempts = sqlc.arg(attempts),
	started_at = COALESCE(started_at, %s),
	claimed_until = %s,
	revision = revision + 1,
	last_updated_at = %s
WHERE id = sqlc.arg(id)
	AND state = ANY(sqlc.arg(active_states)::text[])
	AND claimed_until <= %s
RETURNING
	%s`,
		OperationsTable,
		querygen.NowExpression,
		leaseHorizon,
		querygen.NowExpression,
		querygen.NowExpression,
		returning(),
	)
}

// recordOperationProgress is a progress flush, which is three statements' worth
// of work in one.
//
// It records where the Runner has got to, extends the lease that says this
// worker still has the operation, and returns whether a cancellation has been
// requested. Fusing them is not a micro-optimization: it means a Runner that
// reports progress is, by that fact alone, holding its lease and observing
// cancellations, with no second round trip and nothing for a Runner author to
// remember to call.
//
// units_total is COALESCEd rather than assigned, so a flush carrying no total
// cannot clear one the Runner already declared — a denominator that appeared and
// then vanished would have a client's progress bar turn back into a spinner
// mid-operation. units_done and progress_count take the greater of the stored
// value and the new one, because both counters are monotonic by contract and
// the case that would otherwise break them is a straggler flush from a worker
// whose lease lapsed landing after the new worker's, walking a client's number
// backwards for no reason the client could ever explain.
//
// The guard is state = running rather than the active set: a flush from a Runner
// whose operation has already been finished by somebody else must not resurrect
// its progress, and one arriving before the claim has no lease to extend.
var recordOperationProgress = fmt.Sprintf(`UPDATE %s SET
	units_total = COALESCE(sqlc.narg(units_total), units_total),
	units_done = GREATEST(units_done, sqlc.arg(units_done)),
	progress_unit = sqlc.arg(progress_unit),
	progress_count = GREATEST(progress_count, sqlc.arg(progress_count)),
	progress_message = sqlc.arg(progress_message),
	claimed_until = %s,
	revision = revision + 1,
	last_updated_at = %s
WHERE id = sqlc.arg(id)
	AND state = sqlc.arg(running_state)
RETURNING cancel_requested, revision`,
	OperationsTable, leaseHorizon, querygen.NowExpression)

// finishOperation is the terminal write, in its two forms.
//
// The lease is dropped outright: nothing will claim the operation again, and a
// claimed_until left in the future is a row every recovery sweep still has to
// consider. The guard is the active set, so a worker whose lease lapsed
// mid-operation cannot finish an operation somebody else has already finished —
// it matches no rows and is told so, which is the difference between a duplicate
// and a silently overwritten result.
//
// everyUnitDone is the second statement rather than a conditional assignment,
// because a SET list assembled per call is the dynamic SQL this whole tier
// exists to replace. It raises units_done to the declared total, for the success
// that finished every unit without reporting the last one: a completed operation
// reading "8 of 9" is the single most confusing thing a progress surface can
// show.
func finishOperation(everyUnitDone bool) string {
	unitsDone := ""
	if everyUnitDone {
		unitsDone = "\n\tunits_done = COALESCE(units_total, units_done),"
	}

	return fmt.Sprintf(`UPDATE %s SET
	state = sqlc.arg(state),
	result_uri = sqlc.arg(result_uri),
	result_detail = sqlc.narg(result_detail),
	error_code = sqlc.arg(error_code),
	error_message = sqlc.arg(error_message),
	error_retryable = sqlc.arg(error_retryable),%s
	progress_unit = '',
	finished_at = %s,
	claimed_until = %s,
	revision = revision + 1,
	last_updated_at = %s
WHERE id = sqlc.arg(id)
	AND state = ANY(sqlc.arg(active_states)::text[])`,
		OperationsTable, unitsDone, querygen.NowExpression, epoch, querygen.NowExpression)
}

// releaseOperation hands a running operation back for another attempt: the
// state returns to pending, the lease drops, and the failure that caused it is
// recorded so a client polling in the gap sees why the operation is taking a
// second run rather than a blank pause.
//
// error_code and error_message are set on a row that has not failed, which is
// deliberate. They are the last error, not the final one; the terminal write
// overwrites them, and a succeeded operation carries no Error because the store
// only builds one for a failed state.
var releaseOperation = fmt.Sprintf(`UPDATE %s SET
	state = sqlc.arg(pending_state),
	error_code = sqlc.arg(error_code),
	error_message = sqlc.arg(error_message),
	error_retryable = TRUE,
	progress_unit = '',
	claimed_until = %s,
	revision = revision + 1,
	last_updated_at = %s
WHERE id = sqlc.arg(id)
	AND state = sqlc.arg(running_state)`,
	OperationsTable, epoch, querygen.NowExpression)

// requestOperationCancel flags an operation for cancellation.
//
// A pending operation is cancelled outright in the same statement: nothing has
// started, so there is nothing to ask and nobody to ask it of. A running one has
// the flag set and keeps running until its Runner notices — which is the only
// correct answer, because only the Runner knows what a half-finished unit of its
// work has left behind.
//
// A terminal operation fails the guard and is left alone, so cancelling is
// idempotent and a double click is not an error.
//
// The CASE is what makes the two outcomes one statement. Split into a read and
// a write, the decision would be taken against a state the row may have left by
// the time the write lands.
var requestOperationCancel = fmt.Sprintf(`UPDATE %s SET
	cancel_requested = TRUE,
	state = CASE WHEN state = sqlc.arg(pending_state) THEN sqlc.arg(cancelled_state) ELSE state END,
	finished_at = CASE WHEN state = sqlc.arg(pending_state) THEN %s ELSE finished_at END,
	claimed_until = CASE WHEN state = sqlc.arg(pending_state) THEN %s ELSE claimed_until END,
	revision = revision + 1,
	last_updated_at = %s
WHERE id = sqlc.arg(id)
	AND state = ANY(sqlc.arg(active_states)::text[])`,
	OperationsTable, querygen.NowExpression, epoch, querygen.NowExpression)

// listStrandedOperations is the recovery sweep's read: operations that are
// active but that nothing is going to pick up.
//
// Two shapes qualify, and they are the same fact seen from either side of a
// start's two writes. A pending operation older than the grace period is one
// whose enqueue never landed — the process died between recording it and
// offering it. A running operation whose lease lapsed is one whose worker died
// and whose queue item went with it.
//
// The pending arm reads created_at rather than last_updated_at. How long an
// operation has sat unclaimed is measured from when it was recorded, and a
// cancellation request — the one write that touches a pending row without
// starting it — must not restart that clock.
//
// The grace period is what keeps this from re-enqueueing every operation the
// fleet is starting right now, which is the moment it would hurt most.
//
// It is the fleet's own machinery servicing itself rather than a consumer read,
// which is why it carries no owner predicate: a recovery sweep that recovered
// one owner's operations would leave every other owner's stranded.
func listStrandedOperations() string {
	pending := querygen.Qualify(OperationsTable, StateColumn) + " = sqlc.arg(pending_state)"
	running := querygen.Qualify(OperationsTable, StateColumn) + " = sqlc.arg(running_state)"

	return fmt.Sprintf(`SELECT
	%s
FROM %[2]s
WHERE (%[3]s AND %[2]s.created_at <= %[5]s)
	OR (%[4]s AND %[2]s.claimed_until <= %[5]s)
ORDER BY %[2]s.created_at ASC
LIMIT sqlc.arg(stranded_limit)::int`,
		projection(),
		OperationsTable,
		pending,
		running,
		graceHorizon,
	)
}

// reapOperations is the retention delete.
//
// It deletes terminal rows only, and it deletes them by primary key through a
// CTE that orders and locks them explicitly. That ordering is the same lock
// discipline the work queue's documentation opens with: with one total order,
// contention between a reap and a concurrent write degrades into a queue;
// without it, two writers that overlap in opposite orders deadlock the moment
// they meet.
//
// SKIP LOCKED is what makes a second reaper harmless rather than a second
// reaper waiting: a row another pass has already claimed is somebody else's to
// delete, and there is nothing to be gained by blocking for it.
var reapOperations = fmt.Sprintf(`WITH doomed AS (
	SELECT %[1]s.id
	FROM %[1]s
	WHERE %[1]s.state = ANY(sqlc.arg(terminal_states)::text[])
		AND %[1]s.finished_at IS NOT NULL
		AND %[1]s.finished_at <= %[2]s
	ORDER BY %[1]s.id ASC
	LIMIT sqlc.arg(reap_limit)::int
	FOR UPDATE SKIP LOCKED
)
DELETE FROM %[1]s
WHERE %[1]s.id IN (SELECT doomed.id FROM doomed)`,
	OperationsTable, retentionHorizon)

// The clock expressions every statement above agrees about.
//
// The database's now() is the only clock. Every timestamp that governs
// scheduling — the lease, the recovery cutoff, the retention window — is written
// and compared server-side, and no timestamp is ever bound from a caller's
// process. Durations cross the seam instead, as microsecond counts turned into
// intervals, which is why the operations package has no clock option: a fleet
// coordinates through one table precisely because its processes never have to
// agree on the time.
//
// The exception that proves it is the timestamps read back out. Those are the
// server's own values, returned so a client can render them, and nothing in this
// schema ever compares one of them to a local clock.
const (
	// epoch is the never-leased sentinel claimed_until starts at and returns
	// to. It is NOT NULL and starts here rather than being nullable, so the
	// claimability predicate is a single comparison — claimed_until at or before
	// now covers both "never claimed" and "lease lapsed" — instead of a
	// comparison plus a NULL branch every future writer would have to remember.
	epoch = "TIMESTAMPTZ 'epoch'"
)

// The three horizons a duration crosses the seam as: the moment a claim taken
// now expires, the moment before which an unclaimed operation has waited too
// long, and the moment before which a finished one may be reaped.
var (
	leaseHorizon     = querygen.NowExpression + " + " + microseconds("lease_microseconds")
	graceHorizon     = querygen.NowExpression + " - " + microseconds("grace_microseconds")
	retentionHorizon = querygen.NowExpression + " - " + microseconds("retention_microseconds")
)

// microseconds renders a bound microsecond count as an interval.
//
// A duration crosses the seam as a number rather than as a timestamp so that the
// caller's clock never reaches the row. The cast is what gives sqlc a type for
// an argument whose only other use is multiplication by an interval.
func microseconds(argument string) string {
	return "(sqlc.arg(" + argument + ")::bigint * INTERVAL '1 microsecond')"
}

// insertBindings is one binding per inserted column, in the same order.
// [RequestColumn] is the one that binds nullably.
func insertBindings() []string {
	bindings := make([]string, 0, len(InsertColumns()))

	for _, column := range InsertColumns() {
		if column == RequestColumn {
			bindings = append(bindings, "sqlc.narg("+column+")")

			continue
		}

		bindings = append(bindings, "sqlc.arg("+column+")")
	}

	return bindings
}

// returning renders the projection the two writes that read their row back
// carry. It is [Columns] rather than a list of its own, so a column added to the
// table reaches the create's answer and the claim's answer together.
func returning() string {
	return strings.Join(Columns, ",\n\t")
}

// projection renders the columns a read lists, qualified by the table the way
// every generated read here qualifies them.
func projection() string {
	return strings.Join(querygen.QualifyAll(OperationsTable, Columns), ",\n\t")
}

// FileName is the canonical .sql file a dialect's queries are written to,
// beside this file.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}

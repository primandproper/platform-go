package queries

import (
	"slices"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// RequestsTable is the one table this package owns, at its canonical spelling —
// what the emitted .sql names, and what dataprivacy's own prefix rendering
// starts from.
const RequestsTable = "dataprivacy_requests"

// TableNames is every table dataprivacy owns, in the order the DDL creates
// them.
//
// One, and the list exists anyway. What feeds the querygen registry has to be
// the table existing rather than something choosing to emit its queries — see
// [querygen.RegisterTable] — and a list of one is the shape that stays right
// when a second table arrives.
var TableNames = []string{RequestsTable}

// The columns the statements below name. Spelled here rather than at each use,
// because the corpus and the store's own argument maps both read them and a
// second spelling could differ in one letter.
const (
	RequestTypeColumn    = "request_type"
	StatusColumn         = "status"
	OperationIDColumn    = "operation_id"
	SubjectIDColumn      = "subject_id"
	SubjectTypeColumn    = "subject_type"
	SubjectScopeColumn   = "subject_scope"
	DueAtColumn          = "due_at"
	ExpiresAtColumn      = "expires_at"
	CompletedAtColumn    = "completed_at"
	ArtifactRefColumn    = "artifact_ref"
	ArtifactBytesColumn  = "artifact_bytes"
	DeletedRowsColumn    = "deleted_rows"
	AnonymizedRowsColumn = "anonymized_rows"
	FailuresColumn       = "failures"
	RetainedColumn       = "retained"
	LastErrorColumn      = "last_error"
	KeyShreddedAtColumn  = "key_shredded_at"
)

// The arguments the guards and the sweeps bind, which are not the columns they
// compare.
//
// A guarded write names the status the row must still hold as well as the one
// it is moving to, and both halves are the status column — so under one
// argument name the statement would set the column to the value it was
// requiring it to already hold. See [querygen.Match.Arg].
//
// The three horizons are named for what they bound rather than for their
// column, because each is a caller's clock rather than the row's own value:
// "the artifacts that had expired by this instant", not "this row's expiry".
const (
	CurrentStatusArg   = "current_status"
	ExpiresBeforeArg   = "expires_before"
	DueBeforeArg       = "due_before"
	CompletedBeforeArg = "completed_before"
)

// Columns is the request table's full shape, in the order every read projects
// it.
//
// The convention triple sits where the DDL puts it, in the middle: created_at
// is when the request was submitted — the instant the statutory clock runs
// from — last_updated_at is NULL until the fulfiller first moves the row, and
// archived_at is written by nothing here. A served request is reaped on its
// retention window rather than hidden, and the column is present because a
// table querygen reads a shape from carries all three or none.
var Columns = []string{
	querygen.IDColumn,
	RequestTypeColumn,
	StatusColumn,
	OperationIDColumn,
	SubjectIDColumn,
	SubjectTypeColumn,
	SubjectScopeColumn,
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	querygen.ArchivedAtColumn,
	DueAtColumn,
	ExpiresAtColumn,
	CompletedAtColumn,
	ArtifactRefColumn,
	ArtifactBytesColumn,
	DeletedRowsColumn,
	AnonymizedRowsColumn,
	FailuresColumn,
	RetainedColumn,
	LastErrorColumn,
	KeyShreddedAtColumn,
}

// Nullable names the columns a write may set to NULL, which lives in the schema
// neither this package nor querygen reads.
//
// The three stamps are the interesting ones and they are nullable for the same
// reason: each records that something has happened, so its absence is how the
// row says it has not. An erasure with no confirmation window has no
// expires_at; a request that has not reached a terminal state has no
// completed_at; a request whose subject's key was never destroyed has no
// key_shredded_at. Storing a zero time in any of them would make "not yet" read
// as "in the year 1", which every horizon comparison below would treat as long
// overdue.
//
// failures and retained are deliberately absent from this list even though the
// columns are nullable. They are blobs, and a nil []byte already binds as NULL
// on all three dialects, so naming them here would only turn the generated
// parameter into a pointer to a slice.
var Nullable = []string{
	querygen.LastUpdatedAtColumn,
	querygen.ArchivedAtColumn,
	ExpiresAtColumn,
	CompletedAtColumn,
	LastErrorColumn,
	KeyShreddedAtColumn,
}

// InsertColumns returns the columns the create supplies values for: everything
// but the two the database maintains.
//
// created_at is not one of them, and this is the one table in the module where
// that is deliberate rather than an oversight. Elsewhere the column is the row's
// creation time and the database owns it, because a caller-supplied one is how a
// row ends up with a creation time that disagrees with its id. Here it is the
// instant a statutory response window starts running, and due_at is that instant
// plus the configured window, computed in Go at submission. Taking the two ends
// of one deadline from two clocks would make the record internally inconsistent
// in exactly the field a regulator asks about — so both come from the service's
// clock, and the schema's DEFAULT is the backstop for a row written without one.
//
// What made this a real choice rather than a preference is that sqlc-gen-unison
// renders a bound time.Time as the text SQLite stores timestamps in — the same
// shape CURRENT_TIMESTAMP writes — so a bound creation time and a
// database-written one sort identically under this column's lexicographic
// comparisons. Before that, only the database's own clock produced a value the
// created_at window could compare correctly.
func InsertColumns() []string {
	return ColumnsExcept(querygen.LastUpdatedAtColumn, querygen.ArchivedAtColumn)
}

// ColumnsExcept returns the table's shape without the named columns, in
// projection order.
//
// It is how a statement declines a predicate querygen derives from the column
// list. A read keyed on something other than the id hands over a list without
// the id; a sweep that must reach archived rows hands over one without
// archived_at. What a statement projects is a separate list, so leaving a
// column out here does not take it out of the answer.
func ColumnsExcept(excluded ...string) []string {
	kept := make([]string, 0, len(Columns))

	for _, column := range Columns {
		if !slices.Contains(excluded, column) {
			kept = append(kept, column)
		}
	}

	return kept
}

// FileName is the canonical .sql this package renders for a dialect.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}

// Render returns the canonical sqlc input for d: every statement the
// dataprivacy store executes, in one file's worth of text.
//
// It is what dataprivacy/internal/queriesgen writes to the .sql beside this
// file and what CI regenerates to check the committed copy still matches. That
// .sql is sqlc-gen-unison's input, so what the store executes is this text
// exactly: the generated dataprivacydb package carries it per dialect, with the
// consumer's table prefix substituted once at construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	querygen.RegisterTable(TableNames...)

	rendered := []*querygen.Query{
		createRequest(g),
		getRequest(g),
	}

	rendered = append(rendered, subjectLists(g)...)
	rendered = append(rendered, transitions(g)...)
	rendered = append(rendered, completions(g)...)
	rendered = append(rendered, sweeps(g)...)

	return querygen.RenderFile(rendered)
}

// createRequest is the request write.
//
// It is a plain INSERT rather than an upsert. A resubmission is a new request
// with its own statutory clock, and an upsert here would let one quietly
// overwrite the creation time that clock runs from — which is the single field
// in this table a regulator is most likely to ask about.
func createRequest(g *querygen.Generator) *querygen.Query {
	return g.InsertQuery("CreateRequest", RequestsTable, InsertColumns(), Nullable)
}

// getRequest is the single-request read, keyed on the id.
func getRequest(g *querygen.Generator) *querygen.Query {
	return g.GetQuery("GetRequest", RequestsTable, Columns)
}

// subjectLists is a subject's request history, in both directions and in both
// readings of a scope.
//
// The two readings are two statements rather than one predicate that changes
// shape, and the difference between them is the whole reason the pair exists. A
// request names the scope it was confined to, and a subject asking what has been
// asked in their name means all of it — every scope they appear in — while a
// caller who names a scope means that one. A single statement cannot say both:
// an equality on the column answers the scoped question, and there is no bound
// value that turns it into "any". Rendering both is what keeps the unscoped
// reading from being an omitted predicate somebody has to remember to leave off.
//
// Each is a pair, because a paged list is two statements: the same projection,
// the same predicates and the same counts, with the cursor comparison and the
// ORDER BY reversed. A corpus carrying only the ascending half is a store that
// answers sortBy=desc with an ascending page.
func subjectLists(g *querygen.Generator) []*querygen.Query {
	subject := querygen.Match{Column: SubjectIDColumn}

	scoped := g.ListQueries("ListRequestsForSubject", RequestsTable, Columns,
		subject, querygen.Match{Column: SubjectScopeColumn})

	anyScope := g.ListQueries("ListRequestsForSubjectInAnyScope", RequestsTable, Columns, subject)

	return append(scoped, anyScope...)
}

// transitions is the two guarded status moves this state machine makes, and the
// stamp that records a destroyed key.
//
// Each guard is in the predicate rather than in a read-then-write, and that is
// what makes a confirmation safe. A subject clicking confirm twice, or clicking
// it at the instant the lapse sweep cancels the request for having sat too long,
// is a race the database resolves here: the second writer matches no rows and is
// told so, instead of both succeeding and queueing the erasure twice.
//
// They are two statements rather than one because of one column. A confirmation
// records the operation now doing the work — it must not be able to commit the
// status without the pointer to it — and a cancellation must not assign that
// column at all, since blanking it would lose the pointer to an operation that
// is still running. A single statement assigning both would have to choose, and
// either choice is wrong for one of the two callers.
//
// expires_at is assigned by both, and cleared by both callers: every transition
// here leaves the confirmation window behind, either by satisfying it or by
// lapsing it, and a stale window would have the lapse sweep pick the row back
// up.
func transitions(g *querygen.Generator) []*querygen.Query {
	guard := querygen.Match{Column: StatusColumn, Arg: CurrentStatusArg}

	return []*querygen.Query{
		g.UpdateQuery("ConfirmRequest", RequestsTable, Columns,
			[]string{StatusColumn, OperationIDColumn, ExpiresAtColumn}, Nullable, guard),

		g.UpdateQuery("CancelRequest", RequestsTable, Columns,
			[]string{StatusColumn, CompletedAtColumn, ExpiresAtColumn}, Nullable, guard),

		// Guarded on the column still being NULL rather than on a status, so a
		// retried erasure — which re-shreds and is told the original destruction
		// time — cannot move the timestamp forward to the moment of the retry.
		// The one thing this column is for is saying when the key stopped
		// existing, and that instant happened once.
		g.UpdateQuery("MarkKeyShredded", RequestsTable, Columns,
			[]string{KeyShreddedAtColumn}, Nullable,
			querygen.Match{Column: KeyShreddedAtColumn, Against: querygen.NoValue}),
	}
}

// completions is the three writes that end a request, and the one that retires
// the artifact an export left behind.
//
// All four guard on the status the row must still hold. That is what makes a
// completion safe against a cancellation that landed while the runner was busy,
// and what makes a duplicate execution safe: two runners on the same request
// produce the same artifact at the same key, and the second one's completion
// matches no row.
//
// The erasure's expires_at is cleared rather than set. An erasure has no
// artifact to expire, and the column held its confirmation window — leaving that
// behind would have the lapse sweep cancel a request that has already run.
func completions(g *querygen.Generator) []*querygen.Query {
	guard := querygen.Match{Column: StatusColumn, Arg: CurrentStatusArg}

	return []*querygen.Query{
		g.UpdateQuery("CompleteExport", RequestsTable, Columns, []string{
			StatusColumn, CompletedAtColumn, ExpiresAtColumn,
			ArtifactRefColumn, ArtifactBytesColumn, FailuresColumn, LastErrorColumn,
		}, Nullable, guard),

		g.UpdateQuery("CompleteErasure", RequestsTable, Columns, []string{
			StatusColumn, CompletedAtColumn, ExpiresAtColumn,
			DeletedRowsColumn, AnonymizedRowsColumn, FailuresColumn, RetainedColumn,
			KeyShreddedAtColumn, LastErrorColumn,
		}, Nullable, guard),

		// The last failure and only the last one. The retry schedule, the attempt
		// budget and the lease belong to the operation, so what this row records
		// is the moment at which "nobody is going to get an answer" became true.
		g.UpdateQuery("FailRequest", RequestsTable, Columns, []string{
			StatusColumn, LastErrorColumn, CompletedAtColumn, ExpiresAtColumn,
		}, Nullable, guard),

		// The reference is cleared as the status changes, so a stale path cannot
		// outlive the object it named and be handed to a signer later.
		g.UpdateQuery("ExpireArtifact", RequestsTable, Columns, []string{
			StatusColumn, ArtifactRefColumn, ExpiresAtColumn,
		}, Nullable, guard),
	}
}

// sweeps is the three background passes over this table, plus the gauge that
// reports what they have not caught up with.
//
// # Why two of them are one statement and the third is not
//
// The expiry sweep selects rather than writes, because the object has to go
// before the row says it has. A bulk UPDATE marking rows expired would be one
// round trip and would leave every artifact in the bucket, which is precisely
// the outcome the expired state exists to prevent. The other two have nothing
// outside the database to clean up — an unconfirmed erasure has touched no
// domain and written no object, and a reaped record is a row — so each is one
// bounded write whose count is the answer.
//
// # Why none of them names a status set
//
// The lapse and the overdue gauge and the reap each used to carry a list of
// statuses: the ones a request can still move out of, and the ones retention may
// reap. Both lists say something the row already says. completed_at is written
// by every transition into a terminal state and by nothing else, and nothing
// moves out of one — so completed_at IS NOT NULL is exactly "terminal" and its
// complement is exactly "still owed to somebody".
//
// Stating it that way is not only shorter. A bound set of statuses inside a
// bounded write's subquery is an expansion on the two dialects with no array
// type, and the page-size argument is bound after it — which is the one
// arrangement [querygen.Generator.SetReadQuery] documents as silently wrong,
// because SQLite numbers a bare marker one past the highest it has seen and the
// limit would collide with an element of the set. The column that already
// carries the fact avoids the question.
func sweeps(g *querygen.Generator) []*querygen.Query {
	byExpiry := []querygen.Order{{Column: ExpiresAtColumn}, {Column: querygen.IDColumn}}

	expiring := g.SweepQuery("ListExpiringArtifacts", RequestsTable, Columns,
		querygen.Sweep{Order: byExpiry},
		querygen.Match{Column: StatusColumn},
		querygen.Match{Column: ArtifactRefColumn, Against: querygen.EmptyString, Exclude: true},
		querygen.Match{Column: ExpiresAtColumn, Against: querygen.NoValue, Exclude: true},
		querygen.Match{Column: ExpiresAtColumn, Against: querygen.AtMostArgument, Arg: ExpiresBeforeArg},
	)

	lapse := g.SweepUpdateQuery("LapseUnconfirmedRequests", RequestsTable, Columns,
		[]string{StatusColumn, CompletedAtColumn, ExpiresAtColumn}, Nullable, byExpiry,
		querygen.Match{Column: StatusColumn, Arg: CurrentStatusArg},
		querygen.Match{Column: ExpiresAtColumn, Against: querygen.NoValue, Exclude: true},
		querygen.Match{Column: ExpiresAtColumn, Against: querygen.AtMostArgument, Arg: ExpiresBeforeArg},
	)

	overdue := g.CountQuery("CountOverdueRequests", RequestsTable, Columns,
		querygen.Match{Column: RequestTypeColumn},
		querygen.Match{Column: CompletedAtColumn, Against: querygen.NoValue},
		querygen.Match{Column: DueAtColumn, Against: querygen.AtMostArgument, Arg: DueBeforeArg},
	)

	// A row whose artifact_ref is still set is never reaped, whatever its age.
	// The reference is the only record of where that object is, and deleting the
	// row first would leave a file containing everything known about a person
	// sitting in a bucket with nothing left pointing at it.
	//
	// Its column list omits archived_at, as every hard delete's does: an erasure
	// runs against a subject who was archived first, and a retention pass that
	// skipped the archived rows would leave behind exactly the records nobody is
	// looking at any more.
	reap := g.SweepDeleteQuery("ReapRequests", RequestsTable,
		ColumnsExcept(querygen.ArchivedAtColumn),
		[]querygen.Order{{Column: CompletedAtColumn}, {Column: querygen.IDColumn}},
		querygen.Match{Column: ArtifactRefColumn, Against: querygen.EmptyString},
		querygen.Match{Column: CompletedAtColumn, Against: querygen.NoValue, Exclude: true},
		querygen.Match{Column: CompletedAtColumn, Against: querygen.AtMostArgument, Arg: CompletedBeforeArg},
	)

	return []*querygen.Query{expiring, lapse, overdue, reap}
}

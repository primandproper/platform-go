package queries

import (
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// ReportsTable is the one table this package owns, at its canonical spelling —
// what the emitted .sql names, and what issuereports' own prefix rendering
// starts from.
//
// It is issue_reports rather than issuereports_reports. Every table in this
// module says which package created it, and this one does: there is no other
// package the name could belong to, and the doubled segment would buy nothing
// but a longer identifier for every index built on it.
const ReportsTable = "issue_reports"

// TableNames is every table issuereports owns.
//
// issuereports/migrations is where a consumer gets these names rendered at their
// prefix. This list is the canonical spelling, and migrations.Tables reads the
// DDL, so the two are cross-checked against each other in this package's tests
// rather than one being derived from the other.
var TableNames = []string{ReportsTable}

// The columns the statements below name outside the column list.
//
// ScopeColumn is the tenancy dimension every statement is keyed on. It is a
// column, not a convention: an unscoped read of this schema is not expressible,
// because there is no statement that omits it.
//
// ReporterColumn is who filed the report. It is not the scope and does not stand
// in for it, and it is the column the privacy path keys on: the details a
// reporter wrote are theirs, so an erasure names a person rather than a tenant.
const (
	ScopeColumn    = "scope"
	ReporterColumn = "reporter"
)

// The lifecycle columns, spelled here rather than at their call sites because
// more than one statement reasons about each.
//
// StatusColumn is where the report stands, and it appears three times in the
// transition: in the SET that moves it, in the guard that says which status the
// caller believed it was in, and in the status-narrowed list. CurrentStatusArg
// is what keeps the first two from being one argument — a SET and a WHERE
// sharing an argument name is a statement that assigns the column the value it
// is requiring the column to already hold, which is legal SQL that guards
// nothing.
const (
	StatusColumn     = "status"
	CurrentStatusArg = "current_status"
	ClosedAtColumn   = "closed_at"
	ResolutionColumn = "resolution"
)

// The columns naming what a report is about. Both are spelled here because two
// statements key on them: the subject-type list, which takes the first alone,
// and the subject list, which takes both.
const (
	SubjectTypeColumn = "subject_type"
	SubjectIDColumn   = "subject_id"
)

// The columns a reporter may revise after filing. Spelled here because they are
// the update's SET list, and leaving one out is how a store grows a field
// nobody can correct a typo in.
const (
	KindColumn    = "kind"
	DetailsColumn = "details"
)

// Reports is what somebody told you, and where that report stands.
//
// It gets none of querygen's standard set, and the reason is the same one
// notifications' inbox gives: StandardCRUD keys its single-row statements on the
// id and one ownership column, and it renders no guard. This table wants the
// scope on every statement and a status guard on the one write that moves the
// row through its lifecycle, so every statement below is a keyed variant naming
// what it actually keys on.
var Reports = Table{
	Name: ReportsTable,
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		ReporterColumn,
		KindColumn,
		DetailsColumn,
		SubjectTypeColumn,
		SubjectIDColumn,
		StatusColumn,
		ResolutionColumn,
		ClosedAtColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Nullable: []string{ClosedAtColumn},
}

// Render returns the canonical sqlc input for d: every statement the
// issuereports store runs, in one file's worth of text.
//
// It is what issuereports/internal/queriesgen writes to the .sql beside this
// file and what CI regenerates to check the committed copy still matches. That
// .sql is sqlc-gen-unison's input, so what the store executes is this text
// exactly: the generated issuereportsdb package carries it per dialect, with the
// consumer's table prefix substituted once at construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// Registered by the table existing rather than by anything choosing to emit
	// its queries. It takes no StandardCRUD — which is what would otherwise have
	// registered it — and a consumer reading the registry back to truncate a
	// database would then miss it.
	querygen.RegisterTable(TableNames...)

	rendered := append(writes(g), reads(g)...)

	return querygen.RenderFile(rendered)
}

// writes is the five statements that file a report and move it through its life:
// created, revised, transitioned, archived, and — on the privacy path — erased.
//
// Four of the five are keyed on the scope as well as on whatever else they name,
// and the fifth is the insert, which keys on nothing because an INSERT does not.
func writes(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}

	// The guard the lifecycle rests on. TransitionReport assigns status and
	// requires the row to still hold the status its caller read, so the two ends
	// of that comparison are two arguments: current_status in the predicate,
	// status in the SET. Under one name the statement would set the column to
	// the value it was requiring it to already hold, and two triagers resolving
	// the same report would both be told they won.
	//
	// closed_at and resolution move with it rather than in a statement of their
	// own, because they are the same fact: a report is closed when it reaches a
	// terminal status, for the reason the note gives, and a reopen clears both.
	// A second write would leave a window in which the row is resolved and the
	// reason it was resolved has not been written yet.
	guard := querygen.Match{Column: StatusColumn, Arg: CurrentStatusArg}

	return []*querygen.Query{
		g.InsertQuery("CreateReport", ReportsTable, Reports.InsertColumns(), Reports.Nullable),

		// What the reporter may revise: the category, the text, and what the
		// report is about. Not the status — that moves through the guarded
		// transition — and not the reporter or the scope, which are who filed it
		// and where, and are not editable facts.
		g.UpdateQuery("UpdateReport", ReportsTable, Reports.Columns,
			[]string{KindColumn, DetailsColumn, SubjectTypeColumn, SubjectIDColumn}, nil,
			scope),

		g.UpdateQuery("TransitionReport", ReportsTable, Reports.Columns,
			[]string{StatusColumn, ResolutionColumn, ClosedAtColumn}, Reports.Nullable,
			scope, guard),

		g.ArchiveQuery("ArchiveReport", ReportsTable, Reports.Columns, scope),

		// The erasure. It is a hard delete rather than an archive, and it is the
		// one write here keyed on a person: the details are free text somebody
		// typed, so what a subject access request has to remove is every report
		// they filed rather than a row anyone would recognize by id. The column
		// list goes over without the id for that reason — querygen renders the id
		// predicate from the list it is handed — and the count is what the
		// erasure reports as deleted.
		g.DeleteQuery("DeleteReportsByReporter", ReportsTable,
			Reports.ColumnsExcept(querygen.IDColumn),
			scope, querygen.Match{Column: ReporterColumn}),
	}
}

// reads is the get, the read-back the create runs, and the four paged lists,
// each list in both directions because a paged list is two statements.
//
// The four lists are four statements rather than one with optional predicates,
// which is the reading notifications' unread list already takes: querygen's
// optional narrowing exists and would collapse them, but each of these
// predicates is an equality a caller either wants or does not, and four
// statements whose plans a server can see beats one whose predicates it has to
// discover per execution. What they cost is text nobody hand-writes.
func reads(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}

	rendered := []*querygen.Query{
		g.GetQuery("GetReport", ReportsTable, Reports.Columns, scope),

		// The read the create runs to learn the creation time the database
		// assigned. created_at is database-owned, so the insert does not carry
		// it, and without this the value a caller serializes straight back into
		// a response says 0001-01-01 for a row written a moment ago.
		g.ReadQuery("GetReportCreatedAt", ReportsTable,
			[]string{querygen.IDColumn},
			querygen.Read{Projection: []string{querygen.CreatedAtColumn}},
			scope),
	}

	rendered = append(rendered,
		g.ListQueries("ListReports", ReportsTable, Reports.Columns, scope)...)

	// The triage queue: everything in one status, in one scope. This is the read
	// the status column exists for, and the reason it is a column rather than a
	// derived reading of closed_at — "open" and "acknowledged" are both unclosed,
	// and a queue that could not tell them apart would be a queue nobody had
	// started working.
	rendered = append(rendered,
		g.ListQueries("ListReportsByStatus", ReportsTable, Reports.Columns,
			scope, querygen.Match{Column: StatusColumn})...)

	// One person's own reports. It is also the collector's read: a subject access
	// request asks what is held about somebody, and what is held here is what
	// they filed.
	rendered = append(rendered,
		g.ListQueries("ListReportsByReporter", ReportsTable, Reports.Columns,
			scope, querygen.Match{Column: ReporterColumn})...)

	// Every report about one kind of thing, and every report about one of them.
	// The second is the first with one more predicate, and both are here because
	// both are questions somebody asks: "is this feature generating complaints"
	// and "what has been said about this record".
	rendered = append(rendered,
		g.ListQueries("ListReportsBySubjectType", ReportsTable, Reports.Columns,
			scope, querygen.Match{Column: SubjectTypeColumn})...)

	return append(rendered,
		g.ListQueries("ListReportsForSubject", ReportsTable, Reports.Columns,
			scope,
			querygen.Match{Column: SubjectTypeColumn},
			querygen.Match{Column: SubjectIDColumn})...)
}

// FileName is the file one dialect's rendered queries are committed to.
//
// The _generated suffix is in the path rather than only in the header comment,
// because a path is what a reviewer sees in a diff, what CI's glob selects, and
// what a reader scanning this directory reads first — and these are the files
// whose answer to "this line is wrong" is to edit something else.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}

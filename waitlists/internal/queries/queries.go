package queries

import (
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// The tables this package owns, at their canonical spelling — what the emitted
// .sql names, and what the waitlist store's own prefix rendering starts from.
const (
	ListsTable   = "waitlists"
	SignupsTable = "waitlist_signups"
)

// TableNames is every table waitlists owns, in the order the DDL creates them.
//
// Both of them, even though only one gets a standard set: a table nothing
// generates a standard query for is still a table with rows in it. That is the
// distinction the querygen registry is built around, and this is the list
// [Render] feeds it.
//
// waitlists/migrations is where a consumer gets these names rendered at their
// prefix. This list is the canonical spelling, and migrations.Tables reads the
// DDL, so the two are cross-checked against each other in this package's tests
// rather than one being derived from the other.
var TableNames = []string{
	ListsTable,
	SignupsTable,
}

// ScopeColumn is the tenancy dimension both tables carry and every statement
// over either is keyed on. It is a column, not a convention: an unscoped read of
// this schema is not expressible, because there is no statement that omits it.
//
// The signups table carries its own copy rather than reaching the list's through
// the reference. A scope predicate that had to join to find its column is a
// predicate a read can omit, and every read here is one that must not.
const ScopeColumn = "scope"

// The list columns the keyed statements and the store both name. Exported
// because two spellings of one column is the drift this package exists to
// prevent.
const (
	ListNameColumn        = "name"
	ListDescriptionColumn = "description"
	ListClosesAtColumn    = "closes_at"
)

// The signup columns, same reason.
const (
	SignupListColumn          = "waitlist_id"
	SignupContactColumn       = "contact"
	SignupContactDigestColumn = "contact_digest"
	SignupSubjectTypeColumn   = "subject_type"
	SignupSubjectIDColumn     = "subject_id"
	SignupNotesColumn         = "notes"
	SignupStatusColumn        = "status"
	SignupStatusChangedColumn = "status_changed_at"
)

// OpenAsOfArg is the instant "still taking signups" is decided against.
//
// It is bound rather than read off the server's clock, which is the choice
// querygen.AtMostArgument documents: closes_at is stamped by the application
// from whatever clock the store was handed, so comparing it against
// CURRENT_TIMESTAMP would be two clocks deciding one row — and under a test
// clock that only moves when a test moves it, the two are years apart.
const OpenAsOfArg = "open_as_of"

// ExpectedStatusArg is the status a transition requires the row to already
// hold, or — inverted — the one it requires the row not to hold.
//
// It is a second argument rather than the status column's own, because the same
// statement both reads the column in its predicate and assigns it in its SET.
// Under one name the write would set the column to the value it was requiring it
// to already have, which is legal SQL that guards nothing.
const ExpectedStatusArg = "expected_status"

// The subject an erasure is about, as the two arguments its predicate binds.
//
// They are second names for the subject columns for the reason ExpectedStatusArg
// is a second name for the status column: the erasure both requires the row to
// name this subject and assigns those columns the empty string, and under one
// name per column the statement would blank the subject it was matching on —
// legal SQL that matches nothing.
const (
	ErasedSubjectTypeArg = "erased_subject_type"
	ErasedSubjectIDArg   = "erased_subject_id"
)

// Lists is the catalog: what a waitlist is called, what it is for, and when it
// stops taking signups.
//
// It gets the standard set. Its rows are addressed by their own id within a
// scope, which is exactly what StandardCRUD emits, and the two reads that are
// not in that set — the page of lists still open, and the read-back of the
// creation time — are keyed variants below.
//
// The exists query is omitted: nothing asks whether a list is there without
// also wanting to know when it closes, since that is the question every signup
// begins with.
var Lists = Table{
	Name:     ListsTable,
	Singular: "List",
	Plural:   "Lists",
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		ListNameColumn,
		ListDescriptionColumn,
		ListClosesAtColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Updatable: []string{
		ListNameColumn,
		ListDescriptionColumn,
		ListClosesAtColumn,
	},
	Omitted: []querygen.StandardQuery{querygen.ExistsQuery},
}

// Signups is one person's place on one list.
//
// It gets no standard queries and declares no Updatable, because not one of its
// statements is the standard shape. Every single-row statement keys on the list
// as well as on the row's own id — a signup read without the list it is for is a
// read that has forgotten half of what addresses it — and each of the three
// writes assigns a different set of columns for a different reason: a note edit
// touches notes, a transition touches the status pair, and a withdrawal blanks
// everything that identifies a person. A single Updatable list would be the
// union of the three, which is the set no statement wants.
//
// Nullable is status_changed_at alone. It is NULL for a signup that has not
// moved since it was written, which is the ordinary state of everybody still
// waiting — see waitlists/migrations for why it is not last_updated_at.
var Signups = Table{
	Name:     SignupsTable,
	Singular: "Signup",
	Plural:   "Signups",
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		SignupListColumn,
		SignupContactColumn,
		SignupContactDigestColumn,
		SignupSubjectTypeColumn,
		SignupSubjectIDColumn,
		SignupNotesColumn,
		SignupStatusColumn,
		SignupStatusChangedColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Nullable: []string{SignupStatusChangedColumn},
}

// Emitted is the tables the canonical .sql covers with the standard set, in the
// order they appear in it.
//
// One of the two. Signups is deliberately absent and still contributes twelve
// statements. The list is what gets a set, not what gets a statement.
var Emitted = []*Table{&Lists}

// Render returns the canonical sqlc input for d: the list table's standard
// queries and every keyed statement the store runs beside them, in one file's
// worth of text.
//
// It is what waitlists/internal/queriesgen writes to the .sql beside this file
// and what CI regenerates to check the committed copy still matches. That .sql
// is sqlc-gen-unison's input, so what the store executes is this text exactly:
// the generated waitlistsdb package carries it per dialect, with the consumer's
// table prefix substituted once at construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// Every table waitlists owns, not the one the loop below emits for.
	// StandardCRUD registers what it emits, which leaves the signups out — and
	// that is a table with rows in it, so a consumer reading the registry back
	// to truncate a database would miss one of two.
	querygen.RegisterTable(TableNames...)

	var rendered []*querygen.Query
	for _, table := range Emitted {
		rendered = append(rendered, g.StandardCRUD(table.Name, table.Columns, table.Options()...)...)
	}

	rendered = append(rendered, createdAtReads(g)...)
	rendered = append(rendered, openListReads(g)...)
	rendered = append(rendered, signupReads(g)...)
	rendered = append(rendered, signupWrites(g)...)

	return querygen.RenderFile(rendered)
}

// createdAtReads is the read-back of the one column a create does not carry: the
// creation time the database assigned it.
//
// created_at is database-owned — it is not in any create's column list, and the
// schema gives it a DEFAULT — so the value the caller handed over still holds
// the zero time when the INSERT returns, and the store reads it back inside the
// same transaction.
//
// Each keys on the id alone. The scope is absent because this is not a read a
// caller reaches: it is the create's read-back of the row it has just written,
// by the id it minted for it, and the row is not visible to anything else until
// the transaction commits. The column list is the id and nothing else, which is
// also what leaves the archived predicate off a row that cannot be archived yet.
func createdAtReads(g *querygen.Generator) []*querygen.Query {
	rendered := make([]*querygen.Query, 0, 2)

	for _, table := range []*Table{&Lists, &Signups} {
		rendered = append(rendered, g.ReadQuery(
			"Get"+table.Singular+"CreatedAt", table.Name,
			[]string{querygen.IDColumn},
			querygen.Read{Projection: []string{querygen.CreatedAtColumn}},
		))
	}

	return rendered
}

// openListReads is the catalog page narrowed to the lists still taking signups.
//
// It is the standard list with one more comparison, which is the whole reason
// closes_at is NOT NULL: the predicate is `closes_at > sqlc.arg(open_as_of)`
// rather than a disjunction over a column that might be NULL, so the index the
// schema ships serves it and the page walks by keyset like any other.
//
// The comparand is querygen.AtMostArgument inverted. Uninverted it is the closed
// half — everything at or past the horizon — and Exclude is its complement, so
// "open" and "closed" are one predicate with one bool between them rather than
// two spellings that can come to disagree about the boundary.
func openListReads(g *querygen.Generator) []*querygen.Query {
	return g.ListQueries("ListOpenLists", ListsTable, Lists.Columns,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{
			Column:  ListClosesAtColumn,
			Against: querygen.AtMostArgument,
			Arg:     OpenAsOfArg,
			Exclude: true,
		})
}

// signupReads is the four reads over the table that has no standard queries at
// all: one signup by id, one by the digest of the contact who owns it, the page
// of a list's signups, and the page of one subject's.
//
// The single-row read keys on the list as well as on the id, which is why it is
// rendered from the full column list — that renders the id and archived
// predicates — with the list and the scope as matches.
//
// The digest read is the one that must see archived rows, and it says so by
// being rendered from a column list carrying neither the id nor archived_at.
// That is not an optimization: the uniqueness on (scope, waitlist_id,
// contact_digest) covers archived and withdrawn rows, so a check that skipped
// them would report the contact free and hand the write to the index — a driver
// error where the caller wanted ErrContactWithdrawn, on the one obligation this
// table carries that somebody else enforces on us. The projection is still the
// whole row, so the store decides what an archived hit means rather than being
// unable to see one.
func signupReads(g *querygen.Generator) []*querygen.Query {
	var (
		scope       = querygen.Match{Column: ScopeColumn}
		list        = querygen.Match{Column: SignupListColumn}
		digest      = querygen.Match{Column: SignupContactDigestColumn}
		subjectType = querygen.Match{Column: SignupSubjectTypeColumn}
		subjectID   = querygen.Match{Column: SignupSubjectIDColumn}
	)

	rendered := []*querygen.Query{
		g.ReadQuery("GetSignup", SignupsTable, Signups.Columns,
			querygen.Read{Projection: Signups.Columns},
			scope, list),

		g.ReadQuery("GetSignupByContactDigest", SignupsTable,
			Signups.ColumnsExcept(querygen.IDColumn, querygen.ArchivedAtColumn),
			querygen.Read{Projection: Signups.Columns},
			scope, list, digest),
	}

	rendered = append(rendered,
		g.ListQueries("ListSignups", SignupsTable, Signups.Columns, scope, list)...)

	return append(rendered,
		g.ListQueries("ListSignupsForSubject", SignupsTable, Signups.Columns,
			scope, subjectType, subjectID)...)
}

// signupWrites is the insert, the three updates, the archive, and the erasure.
//
// The three updates are three statements rather than one because they assign
// three different sets for three different reasons, and folding them together
// would mean a note edit binding a status and a withdrawal binding notes.
//
// UpdateSignupNotes is the administrator's annotation and touches nothing else.
//
// TransitionSignup is the lifecycle, and it is guarded on the status the row is
// required to already hold. The guard is what makes a transition happen once:
// two requests inviting the same signup both find it waiting, and only one of
// their updates reports a row. Deciding on the read instead leaves a window as
// wide as whatever the caller does next, which for an invitation is an email.
//
// WithdrawSignup is the erasure. It blanks the contact, the notes and the
// subject reference and keeps the digest, which is what lets a later signup from
// the same address be recognized as somebody who asked to be left alone without
// the table holding their address. Its guard is inverted — the row must not
// already be withdrawn — so a replayed withdrawal reports no rows rather than
// restamping the moment somebody left.
//
// WithdrawSignupsForSubject is the same erasure over every signup one principal
// holds in the scope, which is the write a data privacy erasure runs. It differs
// from WithdrawSignup in three ways, each deliberate. It keys on the subject
// rather than on a list and an id, because an erasure is about a person and a
// person may be on several lists. It is rendered from a column list carrying
// neither the id nor archived_at, so it reaches archived rows: an archived
// signup still holds the address it was made with, and an erasure that left it
// would be an erasure that reported completion over a row still naming somebody.
// And it carries no status guard, because it needs none — a withdrawn row has
// had its subject blanked already, so a predicate naming a subject cannot match
// one. The subject columns are matched under second names, since the SET list
// assigns them; see ErasedSubjectTypeArg.
func signupWrites(g *querygen.Generator) []*querygen.Query {
	var (
		scope = querygen.Match{Column: ScopeColumn}
		list  = querygen.Match{Column: SignupListColumn}

		holding = querygen.Match{Column: SignupStatusColumn, Arg: ExpectedStatusArg}
		notYet  = querygen.Match{Column: SignupStatusColumn, Arg: ExpectedStatusArg, Exclude: true}

		erasedType = querygen.Match{Column: SignupSubjectTypeColumn, Arg: ErasedSubjectTypeArg}
		erasedID   = querygen.Match{Column: SignupSubjectIDColumn, Arg: ErasedSubjectIDArg}

		// What a withdrawal blanks: everything on the row that identifies a
		// person, and the status pair that records the withdrawal. The digest
		// is not among them, on purpose — it is what the suppression outlives
		// the address by.
		erased = []string{
			SignupContactColumn,
			SignupSubjectTypeColumn,
			SignupSubjectIDColumn,
			SignupNotesColumn,
			SignupStatusColumn,
			SignupStatusChangedColumn,
		}
	)

	return []*querygen.Query{
		g.InsertQuery("InsertSignup", SignupsTable, Signups.InsertColumns(), Signups.Nullable),

		g.UpdateQuery("UpdateSignupNotes", SignupsTable, Signups.Columns,
			[]string{SignupNotesColumn}, nil,
			scope, list),

		g.UpdateQuery("TransitionSignup", SignupsTable, Signups.Columns,
			[]string{SignupStatusColumn, SignupStatusChangedColumn}, Signups.Nullable,
			scope, list, holding),

		g.UpdateQuery("WithdrawSignup", SignupsTable, Signups.Columns,
			erased, Signups.Nullable,
			scope, list, notYet),

		g.ArchiveQuery("ArchiveSignup", SignupsTable, Signups.Columns, scope, list),

		g.UpdateQuery("WithdrawSignupsForSubject", SignupsTable,
			Signups.ColumnsExcept(querygen.IDColumn, querygen.ArchivedAtColumn),
			erased, Signups.Nullable,
			scope, erasedType, erasedID),
	}
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

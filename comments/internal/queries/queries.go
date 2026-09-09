package queries

import (
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// CommentsTable is the one table this package owns, at its canonical spelling —
// what the emitted .sql names, and what comments' own prefix rendering starts
// from.
//
// It is comments rather than comments_comments. Every table in this module says
// which package created it, and this one does: there is no other package the
// name could belong to, and the doubled segment would buy nothing but a longer
// identifier for every index built on it.
const CommentsTable = "comments"

// TableNames is every table comments owns.
//
// comments/migrations is where a consumer gets these names rendered at their
// prefix. This list is the canonical spelling, and migrations.Tables reads the
// DDL, so the two are cross-checked against each other in this package's tests
// rather than one being derived from the other.
var TableNames = []string{CommentsTable}

// The columns the statements below name outside the column list.
//
// ScopeColumn is the tenancy dimension every statement is keyed on. It is a
// column, not a convention: an unscoped read of this schema is not expressible,
// because there is no statement that omits it.
//
// AuthorColumn is who wrote the comment. It is not the scope and does not stand
// in for it, and it is the column the privacy path keys on: the words somebody
// wrote are theirs, so an erasure names a person rather than a tenant.
const (
	ScopeColumn  = "scope"
	AuthorColumn = "author"
)

// The columns naming what a comment is about. Both are spelled here because two
// statements key on them: the moderation list, which takes the first alone, and
// the discussion list and the target sweep, which take both.
//
// They are not a foreign key and cannot be — the tables they name are the
// consumer's — which is what the target catalog and the sweep exist to stand in
// for. See the comments package documentation.
const (
	TargetTypeColumn = "target_type"
	TargetIDColumn   = "target_id"
)

// ParentIDColumn is the comment a reply replies to, and the empty string is a
// root.
//
// The empty string rather than NULL is what makes the discussion's two reads one
// statement: `parent_id = ”` selects the roots and `parent_id = $root` selects
// that root's replies, and both are the same text with a different bound value.
// Under NULL the first would be `IS NULL`, which is statement text, and the two
// reads would be two statements free to drift apart.
const ParentIDColumn = "parent_id"

// BodyColumn is what the person said, and it is spelled here because it is the
// edit's whole SET list. What a comment's author may revise is the words; the
// target, the parent, and the author are what the comment is and who wrote it,
// and none of them is an editable fact.
const BodyColumn = "body"

// Comments is something somebody said, about something the application owns,
// possibly in reply to something else somebody said.
//
// It gets none of querygen's standard set, and the reason is the same one
// issuereports' report table gives: StandardCRUD keys its single-row statements
// on the id and one ownership column, and this table wants the scope on every
// statement — reads included — plus three keyed lists and two keyed deletes that
// name columns the standard set has no place for.
var Comments = Table{
	Name: CommentsTable,
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		TargetTypeColumn,
		TargetIDColumn,
		ParentIDColumn,
		AuthorColumn,
		BodyColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
}

// Render returns the canonical sqlc input for d: every statement the comments
// store runs, in one file's worth of text.
//
// It is what comments/internal/queriesgen writes to the .sql beside this file
// and what CI regenerates to check the committed copy still matches. That .sql
// is sqlc-gen-unison's input, so what the store executes is this text exactly:
// the generated commentsdb package carries it per dialect, with the consumer's
// table prefix substituted once at construction.
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

// writes is the five statements that record a comment and end it: written,
// edited, archived, swept with its target, and — on the privacy path — erased
// with its author.
//
// Four of the five are keyed on the scope as well as on whatever else they name,
// and the fifth is the insert, which keys on nothing because an INSERT does not.
func writes(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}

	return []*querygen.Query{
		g.InsertQuery("CreateComment", CommentsTable, Comments.InsertColumns(), Comments.Nullable),

		// The edit assigns the body and nothing else. Not the target, which is
		// what the comment is about and was checked against the consumer's
		// catalog when it was written; not the parent, because moving a reply
		// between roots rewrites a conversation nobody had; and not the author,
		// which is who said it.
		g.UpdateQuery("UpdateComment", CommentsTable, Comments.Columns,
			[]string{BodyColumn}, nil,
			scope),

		g.ArchiveQuery("ArchiveComment", CommentsTable, Comments.Columns, scope),

		// The sweep. A comment's target lives in a table this package has never
		// seen, so nothing here can cascade from that table's delete; the
		// consumer calls this from the transaction that removes the target, and
		// what it removes is every comment about it, replies and archived rows
		// alike. The column list goes over without the id — querygen renders the
		// id predicate from the list it is handed — and the count is what the
		// sweep reports as removed.
		g.DeleteQuery("DeleteCommentsForTarget", CommentsTable,
			Comments.ColumnsExcept(querygen.IDColumn),
			scope,
			querygen.Match{Column: TargetTypeColumn},
			querygen.Match{Column: TargetIDColumn}),

		// The erasure. It is a hard delete rather than an archive, and it keys on
		// a person: the body is free text somebody typed, so what a subject
		// access request has to remove is everything they wrote rather than a row
		// anyone would recognize by id.
		g.DeleteQuery("DeleteCommentsByAuthor", CommentsTable,
			Comments.ColumnsExcept(querygen.IDColumn),
			scope, querygen.Match{Column: AuthorColumn}),
	}
}

// reads is the get, the read-back the create runs, and the three paged lists,
// each list in both directions because a paged list is two statements.
//
// There are three rather than four, and the missing one is the point: the
// discussion's roots and one root's replies are the same statement. Both are
// "the comments on this target whose parent is X", where X is the empty string
// for the roots — which is why parent_id holds the empty string rather than
// NULL. Two statements would have been two plans, two indexes, and two places
// for the projection to drift.
func reads(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}

	rendered := []*querygen.Query{
		g.GetQuery("GetComment", CommentsTable, Comments.Columns, scope),

		// The read the create runs to learn the creation time the database
		// assigned. created_at is database-owned, so the insert does not carry
		// it, and without this the value a caller serializes straight back into
		// a response says 0001-01-01 for a row written a moment ago.
		g.ReadQuery("GetCommentCreatedAt", CommentsTable,
			[]string{querygen.IDColumn},
			querygen.Read{Projection: []string{querygen.CreatedAtColumn}},
			scope),
	}

	// One level of a discussion: the target's roots, or one root's replies.
	rendered = append(rendered,
		g.ListQueries("ListComments", CommentsTable, Comments.Columns,
			scope,
			querygen.Match{Column: TargetTypeColumn},
			querygen.Match{Column: TargetIDColumn},
			querygen.Match{Column: ParentIDColumn})...)

	// Everything anybody has said about one kind of thing. It is the moderation
	// read, and it is the read an operator withdrawing a target type runs first,
	// to see what withdrawing it would strand.
	rendered = append(rendered,
		g.ListQueries("ListCommentsByTargetType", CommentsTable, Comments.Columns,
			scope, querygen.Match{Column: TargetTypeColumn})...)

	// One person's own comments. It is also the collector's read: a subject
	// access request asks what is held about somebody, and what is held here is
	// what they wrote.
	return append(rendered,
		g.ListQueries("ListCommentsByAuthor", CommentsTable, Comments.Columns,
			scope, querygen.Match{Column: AuthorColumn})...)
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

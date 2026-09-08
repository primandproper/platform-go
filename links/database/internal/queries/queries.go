package queries

import (
	"slices"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// LinksTable is the action link table at its canonical, unprefixed spelling —
// what the emitted .sql names, and what the store's own prefix rendering starts
// from.
//
// The action_links segment is the schema's own rather than the caller's, so a
// table says which package created it even in a database shared between
// applications. It is action_links rather than links because "links" in
// somebody else's database is a word, not a component.
const LinksTable = "action_links"

// TableNames is every table this package owns, which is one.
//
// It is a list rather than the constant above because the querygen registry
// takes one, and because a consumer reading that registry back to truncate a
// database between integration tests is asking "what tables does this component
// have rows in" rather than "what does it generate SQL for".
var TableNames = []string{LinksTable}

// The columns of an action link row, beyond the id every keyed statement here
// is derived from.
//
// Spelled here rather than at the store because both halves read them: the
// corpus below binds them, and the store's argument structs and scan targets
// name the fields the generated querier derived from them. Two spellings of one
// column is the drift this package exists to prevent.
const (
	// ActionColumn is which flow the link belongs to. It is stored rather than
	// inferred at redemption because it is the half of what a link is bound to
	// that stops one flow's link working in another's: a verification link that
	// redeems as a login is an account takeover.
	ActionColumn = "action"
	// SubjectColumn is who or what the link is for, as the caller spelled it.
	// It is an opaque identifier rather than a reference to any table — this
	// package does not know what a user is — so an application keeping its
	// users somewhere this table cannot name still gets to use action links.
	SubjectColumn = "subject"
	// MetadataColumn holds what the minter attached, encoded by the store's
	// codec. It is the one column a write may leave NULL, since most links
	// carry none.
	MetadataColumn = "metadata"
	// StateColumn is what has happened to the link, as links.State. It is what
	// separates a redeemed link from a revoked one, which is what lets a second
	// click be told which of the two it was.
	StateColumn = "state"
	// VersionColumn is the record shape the row was written with, a column
	// rather than part of the metadata blob so that it catches a changed shape
	// without being unreadable itself.
	VersionColumn = "version"
	// ExpiresAtColumn is when the link stops being redeemable. It appears in no
	// predicate here: liveness is decided in Go by links.Record.Usable against
	// the minter's clock, so that both stores answer the question the same way.
	ExpiresAtColumn = "expires_at"
	// ResolvedAtColumn is when the link was redeemed or revoked, and NULL while
	// it is still redeemable. It is the column the resolution assigns and the
	// column the resolution guards on, which is what makes single use a
	// property of the statement rather than of whoever read the row first.
	ResolvedAtColumn = "resolved_at"
	// PurgeAfterColumn is when the row may be deleted, which is past
	// expires_at by the minter's retention window. It is what the sweep is
	// keyed on, and it is deliberately not expires_at: a link keeps answering
	// "already used" for exactly as long as the minter said it should, and
	// collecting it at its own deadline instead would make the most common
	// reason a link fails indistinguishable from a link nobody ever minted.
	PurgeAfterColumn = "purge_after"
)

// PurgeBeforeArg is the sqlc argument the sweep binds its horizon through.
//
// It is named for the comparison rather than for the column, because the column
// is already an argument name in this corpus: the insert binds purge_after as
// the value it writes, and a sweep binding a ceiling under the same name would
// be one name for two different facts about a row.
const PurgeBeforeArg = "purge_before"

// Columns is the whole row, in the order the DDL declares it, the insert
// supplies it, and the read projects what it keeps of it.
//
// created_at is among them and is not database-owned, which is where this table
// parts company with the convention querygen.ForInsert encodes. A link's
// creation time is one end of the window its expiry and its purge deadline are
// both computed from, by the minter's clock, in the same breath — so it is the
// minter's to stamp rather than the server's, or a link's lifetime would be
// decided by two clocks.
var Columns = []string{
	querygen.IDColumn,
	ActionColumn,
	SubjectColumn,
	MetadataColumn,
	StateColumn,
	VersionColumn,
	querygen.CreatedAtColumn,
	ExpiresAtColumn,
	ResolvedAtColumn,
	PurgeAfterColumn,
}

// RecordColumns is what the read projects: the row less the id.
//
// The id is left out because the caller already has it — it is the digest they
// asked with, and the only name a link has. A projection that returned it would
// hand back the argument.
var RecordColumns = []string{
	ActionColumn,
	SubjectColumn,
	MetadataColumn,
	StateColumn,
	VersionColumn,
	querygen.CreatedAtColumn,
	ExpiresAtColumn,
	ResolvedAtColumn,
	PurgeAfterColumn,
}

// ResolveColumns is what a resolution assigns: the terminal state, the stamp
// that records it, and the deadline the row may be collected after.
//
// Two statements assign them — the resolution of one link and the revocation of
// a subject's — and they share the list rather than each naming its own, because
// they can race for one row and the row has to end up in one shape whichever
// wins.
//
// Nothing else is here, and every absence is structural rather than a habit. A
// resolution must not be able to move what a link was bound to, when it was
// minted, or when it stopped being redeemable — a link that changed action,
// subject, or deadline as it was spent would be one nobody could reason about
// afterwards — so those columns are left out of the statement rather than
// passed and ignored.
var ResolveColumns = []string{
	StateColumn,
	ResolvedAtColumn,
	PurgeAfterColumn,
}

// NullableColumns names the columns a write may set to NULL, which lives in the
// schema neither this package nor querygen reads.
//
// resolved_at is in it as well as metadata, because the insert writes a link
// nobody has resolved yet.
var NullableColumns = []string{MetadataColumn, ResolvedAtColumn}

// unkeyedColumns is the table's shape as the sweep sees it: every column but
// the id.
//
// querygen derives a statement's id predicate from the column list it is handed,
// so leaving the id out is how a statement says it keys on something else — on a
// deadline for the sweep, on the subject for the plural revoke. What it does not
// decide is which rows come back, and neither of those statements projects
// anything, so both are owed nothing by the omission.
var unkeyedColumns = slices.DeleteFunc(slices.Clone(Columns), func(column string) bool {
	return column == querygen.IDColumn
})

// The query names the generated querier's methods are built from. They are
// spelled here because the store names them too — through the generated params
// types — and because the drift gate beside this file asserts on this exact
// set.
const (
	InsertLinkQuery         = "InsertLink"
	GetLinkQuery            = "GetLink"
	ResolveLinkQuery        = "ResolveLink"
	RevokeSubjectLinksQuery = "RevokeSubjectLinks"
	SweepLinksQuery         = "SweepLinks"
)

// Render returns the canonical sqlc input for d: the five statements this store
// executes, in one file's worth of text.
//
// It is what links/database/internal/queriesgen writes to the .sql files beside
// this one, and what CI regenerates to check the committed copies still match.
// Those files are sqlc-gen-unison's input, so what the store executes is this
// text exactly — the generated linksdb package carries it per dialect, with the
// consumer's table prefix substituted once at construction.
//
// The order is the order a link goes through: written, read, resolved — either
// on its own or as one of a subject's — and finally collected once its purge
// deadline has passed.
//
// # A note on timestamps, because one dialect does something surprising
//
// Every instant this corpus binds is a UTC time.Time and stays one all the way
// down: the minter reads its clock as UTC, the store converts again before it
// binds, and the generated SQLite arm converts once more. Postgres and MySQL
// store these as real temporal types. SQLite has no date type at all, so a
// DATETIME column holds text, and the sweep's `purge_after <=
// sqlc.arg(purge_before)` there is a string comparison.
//
// That comparison is chronological rather than merely lexical because both
// sides are rendered "YYYY-MM-DD HH:MM:SS" in UTC — a fixed-width prefix, one
// zone. A value bound in any other zone would put that zone's wall clock in
// those leading characters and every comparison would be off by the offset,
// silently, and only for the deployments whose clock is not UTC.
//
// The rendering is whole seconds there, so an instant carrying a fraction is
// stored truncated down. For the sweep that direction is harmless — a row is
// collected up to a second early, and it was already unusable — and for
// expires_at it is the direction that fails closed, since liveness is decided
// in Go against the column as it was read: a link on that engine goes dead up
// to a second early rather than living a second past its deadline.
//
// # Why there is no liveness predicate
//
// The read does not filter on the deadline and the resolution does not guard on
// it. links.Record.Usable compares in Go, against the minter's clock, and that
// is a decision rather than an omission this corpus could tidy away: liveness
// is one comparison, made above the store, so no engine's idea of "now" can
// disagree with the one Inspect answered from.
//
// The sweep is the one statement that may compare a deadline in SQL, and it can
// afford to because it deletes rows that are dead by any reading. A guard here
// would be a second copy of the boundary a user hits at the last second of a
// link's life, free to disagree with Inspect about which second that was, and it
// would collapse "expired" and "already redeemed" into one affected-row count
// of zero.
//
// # Why there is no standard set
//
// querygen.Generator.StandardCRUD serves a table with a surrogate id, a paged
// list keyed on it, and the convention triple of timestamps. This table has
// none of that, and every absence is deliberate — see links/database/migrations.
// Its id is the digest of a credential rather than a surrogate; nothing lists
// these rows, because the only way to name one is to hold the token it was
// minted from; and an archived_at would keep rows nothing can read while making
// the sweep the one write unable to reach the rows it exists for.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// The table registers itself here rather than through StandardCRUD, which
	// is what registers a conventional table and which this one gets none of. A
	// consumer reading the registry back to truncate a database between
	// integration tests has to find this table whether or not the statements
	// over it happen to come from the standard set.
	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		insert(g),
		read(g),
		resolve(g),
		revokeForSubject(g),
		sweep(g),
	})
}

// insert is the mint write.
//
// It is a plain INSERT rather than an upsert or an insert-ignore, and the
// difference is what the key means. The id is the digest of the token, so a
// second row bearing one would mean the generator produced the same token
// twice. The primary key refuses that and this statement lets it: a mint
// failing loudly is the correct outcome of randomness that has stopped being
// random, where an ignore would hand the caller a URL that redeems somebody
// else's link.
//
// resolved_at is bound as a nullable rather than left out of the list. A link
// nobody has resolved yet is a row whose stamp is NULL, and the column is in
// the corpus's one shared column list because every other statement here reads
// or writes it — so the insert says NULL explicitly rather than being the one
// statement rendered from a list of its own.
func insert(g *querygen.Generator) *querygen.Query {
	return g.InsertQuery(InsertLinkQuery, LinksTable, Columns, NullableColumns)
}

// read is the lookup Inspect makes and the one the resolution begins with.
//
// It keys on the id and on nothing else — no deadline, no state — because both
// of those are the minter's to decide and a row hidden here would be a row the
// resolution still acted on.
func read(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery(GetLinkQuery, LinksTable, Columns, querygen.Read{Projection: RecordColumns})
}

// resolve is the write that spends or withdraws a link, and it is the statement
// that decides single use.
//
// The guard is resolved_at IS NULL rather than an equality against the state
// the read saw, because "has not happened yet" is not a value a caller holds.
// Two requests that both read the row active both reach this statement; the
// first one's update matches, the second one's finds resolved_at already set
// and reports no rows. The count is the answer, which is why the read cannot
// be, and why the statement is annotated :execrows.
//
// It binds nothing for the guard, so the assignment and the predicate need no
// second argument name between them: the value the guard compares against
// belongs to the statement, and there is nothing a caller could pass that would
// relax it.
//
// This is the whole reason this store needs no lock service. The server
// evaluates "this link, and it is still unresolved" at the instant the row
// changes, rather than a caller evaluating it a round trip earlier and hoping.
func resolve(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(ResolveLinkQuery, LinksTable, Columns, ResolveColumns, NullableColumns,
		querygen.Match{Column: ResolvedAtColumn, Against: querygen.NoValue},
	)
}

// revokeForSubject withdraws every link a subject still has outstanding, in one
// statement.
//
// It is the write links spent a release declining, on the grounds that the
// package held no index to answer it and building one would be a second, weaker
// copy of the application's audit log. That was true of a store keyed only by
// the token's digest and is not true of a table: subject is a column here, so
// the index is the schema, and the answer is an UPDATE rather than a log walk
// with a Revoke per result.
//
// It keys on the subject and nothing else. An operator revoking after a
// suspected compromise does not know what was minted — a narrower key would
// make them enumerate the actions first, which is the walk this statement
// exists to replace — and there is no scope predicate because revoking a
// person's links should cross that person's tenants rather than stop inside
// one. See links/database/migrations.
//
// The guard is the resolution's own, so this and ResolveLink cannot both move
// one row: whichever reaches it first sets resolved_at, and the other finds it
// set. That is why the two statements share ResolveColumns rather than each
// naming what it assigns — one row moved by two writers has to be one
// transition, and two column lists is how the two spellings of it start to
// disagree.
//
// It carries no liveness predicate, which is the corpus rule rather than an
// omission, and here the rule has a visible consequence worth stating. A link
// that expired without ever being resolved is matched and moved, so a bearer
// clicking it afterwards is told the link was revoked rather than that it
// expired, and its purge_after is re-stamped from the revocation instead of
// from the mint. Both readings are true and the second is the one the operator
// asked for: after a compromise, "somebody withdrew this" is the more useful of
// the two sentences, and a row that keeps saying so for the retention window
// following the revocation is the row an investigation wants to find.
func revokeForSubject(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(RevokeSubjectLinksQuery, LinksTable, unkeyedColumns, ResolveColumns, NullableColumns,
		querygen.Match{Column: SubjectColumn},
		querygen.Match{Column: ResolvedAtColumn, Against: querygen.NoValue},
	)
}

// sweep is the removal of every row past its purge deadline.
//
// Resolved rows go with them, at that deadline rather than at their resolution,
// and that is the whole retention policy: a spent link keeps answering "already
// used" for exactly as long as the minter said it should. Deleting on
// redemption instead would make the most common reason a link fails
// indistinguishable from a link nobody ever minted.
//
// The horizon is bound rather than read off the server's clock, and that is
// querygen.AtMostArgument's named case rather than a preference. purge_after is
// stamped by the minter's own clock — an expiry plus a retention window, from a
// clock the minter was handed — so comparing it against CURRENT_TIMESTAMP would
// be two clocks deciding one row, and under a test clock that only moves when a
// test moves it the two are years apart.
//
// It carries no cap. Link rows are small and the index on purge_after makes the
// delete proportional to what is actually dead rather than to the table, so this
// is querygen.Generator.DeleteQuery with a horizon rather than
// querygen.Generator.PruneQuery: there is no backlog for a bound to protect
// against, and a bounded pass would make Sweep's count a loop condition rather
// than an answer.
func sweep(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(SweepLinksQuery, LinksTable, unkeyedColumns,
		querygen.Match{Column: PurgeAfterColumn, Against: querygen.AtMostArgument, Arg: PurgeBeforeArg},
	)
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

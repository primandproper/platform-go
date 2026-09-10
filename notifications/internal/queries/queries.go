package queries

import (
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// The tables this package owns, at their canonical spelling — what the emitted
// .sql names, and what notifications' own prefix rendering starts from.
const (
	InboxTable   = "notifications_inbox"
	DevicesTable = "notifications_devices"
)

// TableNames is every table notifications owns, in the order the DDL creates
// them.
//
// notifications/migrations is where a consumer gets these names rendered at
// their prefix. This list is the canonical spelling, and migrations.Tables reads
// the DDL, so the two are cross-checked against each other in this package's
// tests rather than one being derived from the other.
var TableNames = []string{InboxTable, DevicesTable}

// ScopeColumn is the tenancy dimension both tables carry and every statement is
// keyed on. It is a column, not a convention: an unscoped read of this schema is
// not expressible, because there is no statement that omits it.
//
// PrincipalColumn is whose inbox and whose handset. It is not the scope and does
// not stand in for it — a scope is a directory of people and the principal is
// one of them — but every consumer read names both, because a notification
// addressed to somebody is not a row the rest of their tenant may read.
const (
	ScopeColumn     = "scope"
	PrincipalColumn = "principal"
)

// The inbox columns the statements below name outside the column list.
//
// ReadAtColumn is spelled here rather than at three call sites because three
// statements reason about it: the unread list keys on it being absent, the
// mark-read writes it, and the same write guards on its absence so that a
// replayed mark does not move the stamp forward.
const ReadAtColumn = "read_at"

// The device columns the registry's statements name outside the column list.
//
// PlatformColumn and TokenColumn together are the row's natural key — one live
// row per token, across every scope and principal — so they are the conflict
// target the registration upsert converges on and the key the provider feedback
// hook deletes by. LastSeenAtColumn is the one column a re-registration carries
// forward beside the owner.
const (
	PlatformColumn   = "platform"
	TokenColumn      = "token"
	LastSeenAtColumn = "last_seen_at"
)

// Inbox is what somebody was told, and whether they have read it.
//
// It gets none of querygen's standard set, and the reason is the principal.
// StandardCRUD keys its single-row statements on the id and one ownership
// column, and this table has two: a notification belongs to a scope and, within
// it, to one person. A get keyed on the scope alone would let any member of a
// tenant read any other member's inbox by id, which is not a filter somebody
// forgot but a statement that cannot express what the table means. So every
// statement below is a keyed variant naming both.
var Inbox = Table{
	Name: InboxTable,
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		PrincipalColumn,
		"topic",
		"title",
		"body",
		"link",
		ReadAtColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Nullable: []string{ReadAtColumn},
}

// Devices is what a push is addressed to.
//
// It carries created_at and no other convention column, which is the schema's
// decision rather than this file's: a token is revoked by its owner or
// invalidated by the provider, and either way the row goes. querygen reads that
// off the column list — no archived_at means no archive statement, no
// include_archived toggle and no archived predicate on any read — so the absence
// of the column is the absence of every statement that would have pretended a
// dead token was still there.
//
// The same reading applies to last_updated_at, which this table also lacks: the
// registration upsert's conflict branch assigns what a re-registration actually
// changes — the owner, the scope, and the moment the handset last announced
// itself — and there is no other write.
var Devices = Table{
	Name: DevicesTable,
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		PrincipalColumn,
		PlatformColumn,
		TokenColumn,
		LastSeenAtColumn,
		querygen.CreatedAtColumn,
	},
}

// Render returns the canonical sqlc input for d: every statement the
// notifications store runs, in one file's worth of text.
//
// It is what notifications/internal/queriesgen writes to the .sql beside this
// file and what CI regenerates to check the committed copy still matches. That
// .sql is sqlc-gen-unison's input, so what the store executes is this text
// exactly: the generated notificationsdb package carries it per dialect, with
// the consumer's table prefix substituted once at construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// Both tables, registered by the tables existing rather than by anything
	// choosing to emit their queries. Neither takes StandardCRUD — which is what
	// would otherwise have registered them — and a consumer reading the registry
	// back to truncate a database would then miss both.
	querygen.RegisterTable(TableNames...)

	var rendered []*querygen.Query

	rendered = append(rendered, inboxWrites(g)...)
	rendered = append(rendered, inboxReads(g)...)
	rendered = append(rendered, deviceWrites(g)...)
	rendered = append(rendered, deviceReads(g)...)

	return querygen.RenderFile(rendered)
}

// inboxWrites is the four statements that create a notification and move it
// through its life: created, read, read in bulk, archived.
//
// Three of the four are keyed on the scope and the principal as well as on
// whatever else they name, and the fourth is the insert, which keys on nothing
// because an INSERT does not. There is no update: a notification is not edited,
// so the columns a caller supplies are supplied once and the only mutable thing
// about the row is whether it has been read.
func inboxWrites(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}
	principal := querygen.Match{Column: PrincipalColumn}

	// The guard that makes marking read idempotent in the useful direction: a
	// second mark matches nothing, writes nothing, and reports the zero rows its
	// caller reads as "already read" rather than moving the stamp to now. It
	// binds no argument — IS NULL is the statement's, not the caller's — which
	// is also why it can sit beside a SET assigning the same column.
	unread := querygen.Match{Column: ReadAtColumn, Against: querygen.NoValue}

	// read_at is nullable to the insert and not to these writes, and the
	// difference is the whole point of passing the list rather than the table's:
	// a notification is created unread, so the create may bind NULL, while
	// marking one read binds a time and a statement that let it bind NULL would
	// be a mark-read that unreads.
	markRead := g.UpdateQuery("MarkNotificationRead", InboxTable,
		Inbox.Columns, []string{ReadAtColumn}, nil,
		scope, principal, unread)

	// The same write with the id predicate left off, which is what handing over
	// a column list without the id does. Its count is the answer a caller wants
	// — how many were sitting unread — and there is no cheaper way to learn it.
	markAllRead := g.UpdateQuery("MarkAllNotificationsRead", InboxTable,
		Inbox.ColumnsExcept(querygen.IDColumn), []string{ReadAtColumn}, nil,
		scope, principal, unread)

	return []*querygen.Query{
		g.InsertQuery("CreateNotification", InboxTable, Inbox.InsertColumns(), Inbox.Nullable),
		markRead,
		markAllRead,
		g.ArchiveQuery("ArchiveNotification", InboxTable, Inbox.Columns, scope, principal),
	}
}

// inboxReads is the get, the archived get, and the two paged lists, each list in
// both directions because a paged list is two statements.
//
// The unread list is a second pair rather than a flag on the first, because
// "unread" is IS NULL and there is no bound value a caller could leave unset to
// relax it. What it buys beside the page is the number: the filtered count rides
// on the rows of every list querygen renders, so the badge count a client asks
// for first is what the unread page already carries, and this schema needs no
// COUNT statement of its own.
//
// GetNotification answers three of the four inbox writes as well as the read a
// consumer calls. A row this transaction just filed is not archived and a row it
// just stamped read is still in the inbox, so the ordinary keyed read reaches
// both on the caller's transaction — which is why the creation-time read this
// corpus used to carry is gone. Projecting one column cost the same round trip
// as projecting the row, and answered with what the caller assembled plus a
// timestamp rather than with what the database holds.
//
// GetArchivedNotification is the exception, and it exists because archiving is
// the one inbox write whose result no other statement here can see. Every
// single-row read over this table filters archived_at IS NULL — which is what
// makes an archived notification absent from the inbox — so the read that
// describes what an archive did is the one read written not to find it.
//
// It is rendered from no column list at all, because querygen derives the
// archived predicate from the columns it is handed: a read that must see
// archived rows is one keyed entirely on its matches. What takes that
// predicate's place is its complement — archived_at IS NOT NULL — so the
// read-back asserts the thing it was called to confirm, and a guard that matched
// nothing cannot be read back as a success.
//
// It is not a consumer-facing read and is on neither Inbox nor Registry. It runs
// on the transaction that did the archiving, and the row it sees is visible to
// nothing outside that transaction until it commits.
func inboxReads(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}
	principal := querygen.Match{Column: PrincipalColumn}

	rendered := []*querygen.Query{
		g.GetQuery("GetNotification", InboxTable, Inbox.Columns, scope, principal),

		g.ReadQuery("GetArchivedNotification", InboxTable, nil,
			querygen.Read{Projection: Inbox.Columns},
			querygen.Match{Column: querygen.IDColumn}, scope, principal,
			querygen.Match{Column: querygen.ArchivedAtColumn, Against: querygen.NoValue, Exclude: true}),
	}

	rendered = append(rendered, g.ListQueries("ListNotifications", InboxTable, Inbox.Columns,
		scope, principal)...)

	return append(rendered, g.ListQueries("ListUnreadNotifications", InboxTable, Inbox.Columns,
		scope, principal, querygen.Match{Column: ReadAtColumn, Against: querygen.NoValue})...)
}

// deviceWrites is the registration and the two ways a token leaves the
// registry, and the difference between the two departures is who decided.
//
// RegisterDevice converges on the token rather than inserting, because the token
// is the handset and a handset re-registers on every app launch, on every token
// rotation, and after every sign-out and sign-in by somebody else. The conflict
// branch assigns the owner — principal and scope both — so a phone that changes
// hands moves rather than fanning out, which is the failure a key including the
// principal would produce: two rows, and the previous owner's notifications
// arriving on the new owner's lock screen.
//
// RevokeDevice is the owner's decision and is scoped and keyed on the principal
// like every other consumer read here. DeleteDeviceToken is the provider's, and
// it is the one statement in this schema with no scope in its predicate — see
// the store, which documents why the hook cannot have one.
func deviceWrites(g *querygen.Generator) []*querygen.Query {
	return []*querygen.Query{
		g.UpsertQuery("RegisterDevice", DevicesTable,
			Devices.Columns,
			Devices.InsertColumns(),
			[]string{ScopeColumn, PrincipalColumn, LastSeenAtColumn},
			Devices.Nullable,
			querygen.Match{Column: PlatformColumn},
			querygen.Match{Column: TokenColumn}),

		g.DeleteQuery("RevokeDevice", DevicesTable, Devices.Columns,
			querygen.Match{Column: ScopeColumn},
			querygen.Match{Column: PrincipalColumn}),

		g.DeleteQuery("DeleteDeviceToken", DevicesTable,
			Devices.ColumnsExcept(querygen.IDColumn),
			querygen.Match{Column: PlatformColumn},
			querygen.Match{Column: TokenColumn}),
	}
}

// deviceReads is the two single-row reads the registry's writes answer with,
// one person's devices paged, and the batched fan-out over a set of people.
//
// The batched one is the read the senders exist for: a notification addressed to
// thirty members of an account is thirty inbox rows and one query for every
// token to push to, rather than thirty round trips returning two rows each. Its
// set binds last, after the scope, which querygen requires rather than prefers —
// see querygen.Generator.SetReadQuery — and the empty batch is the store's to
// answer before the query runs.
func deviceReads(g *querygen.Generator) []*querygen.Query {
	// The read-back the registration runs. It keys on the natural key rather
	// than on the id, because the id is what the caller does not know: an
	// upsert that converged on an existing token kept that row's id and its
	// creation time, and the value the caller was holding names neither. So the
	// column list goes over without the id and the projection puts it back.
	//
	// GetDevice is what the revocation reads, and it runs *before* its write
	// rather than after. A revocation deletes the row, so there is no moment
	// after the statement at which anything can describe what it removed — which
	// is the difference between this table and the inbox, where an archived
	// notification is still there to be read by a statement written to see it.
	// The delete is keyed on the same three columns as this read and guards on
	// its own count, so a row that moved between the two is a refusal rather
	// than a value handed back for a deletion that did not happen.
	rendered := []*querygen.Query{
		g.ReadQuery("GetDeviceByToken", DevicesTable,
			Devices.ColumnsExcept(querygen.IDColumn),
			querygen.Read{Projection: Devices.Columns},
			querygen.Match{Column: ScopeColumn},
			querygen.Match{Column: PlatformColumn},
			querygen.Match{Column: TokenColumn}),

		g.GetQuery("GetDevice", DevicesTable, Devices.Columns,
			querygen.Match{Column: ScopeColumn},
			querygen.Match{Column: PrincipalColumn}),
	}

	rendered = append(rendered, g.ListQueries("ListDevices", DevicesTable, Devices.Columns,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: PrincipalColumn})...)

	return append(rendered, g.SetReadQuery("ListDevicesByPrincipals", DevicesTable,
		Devices.Columns,
		querygen.Read{Order: querygen.IDColumn},
		querygen.SetKey{Column: PrincipalColumn, Arg: "principals"},
		querygen.Match{Column: ScopeColumn}))
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

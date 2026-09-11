package queries

import (
	"slices"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// SessionsTable is the one table this package owns, at its canonical spelling
// — what the emitted .sql names, and what the backend's prefix rendering starts
// from.
//
// The sessions segment is the schema's own rather than the caller's, so a table
// says which package created it even in a database shared between
// applications; the consumer's namespace goes in front of it. See
// sessions/database/migrations.
const SessionsTable = "sessions"

// TableNames is every table this package owns, which is the one above.
//
// It is a list rather than a bare constant because the querygen registry takes
// one, and because the thing a registry has to survive is a table being added
// without anybody remembering to register it. The revocation surface is on this
// table rather than beside it — one table, one revocation — so the slice is
// still of one.
var TableNames = []string{SessionsTable}

// The columns of a session row, beyond the id every keyed statement here is
// derived from.
//
// Spelled here rather than at the store because both halves read them: the
// corpus below binds them, and the backend's argument structs and scan targets
// name the fields the generated querier derived from them. Two spellings of one
// column is the drift this package exists to prevent.
const (
	// ScopeColumn holds the tenancy scope the session belongs to. It leads
	// every predicate the enumeration and the revocations are keyed on, so a
	// list is one tenant's rows rather than one principal name's across all of
	// them.
	ScopeColumn = "scope"
	// PrincipalColumn holds the identifier of whoever holds the session, as
	// the caller spelled it. It is an opaque string rather than a reference to
	// any table: this package does not know what a user is, and coupling the
	// session store to an identity store would make one unusable without the
	// other.
	//
	// The empty string is a session attributed to nobody — established before
	// anyone signed in — and is deliberately not enumerable, since a list of
	// every anonymous session in a scope answers nobody's question.
	PrincipalColumn = "principal"
	// DeviceNameColumn is what the client called itself — "Jeffrey's laptop",
	// whatever a consumer chose to record.
	//
	// It and the three below are the metadata a security page renders beside
	// each session, so that a person can recognize the sessions that are theirs
	// and notice one that is not. All four are written once at creation and
	// never assigned again — see UpdateColumns — because they describe the
	// moment a session was established rather than the row's current state, and
	// a device name that moved with the session would describe neither.
	DeviceNameColumn = "device_name"
	// IPAddressColumn is the address the session was established from.
	IPAddressColumn = "ip_address"
	// UserAgentColumn is the client's self-description at establishment.
	UserAgentColumn = "user_agent"
	// LoginMethodColumn is how the principal proved themselves — a password, a
	// passkey, an OAuth provider's name. Its vocabulary is the consumer's.
	LoginMethodColumn = "login_method"
	// DataColumn holds the encoded payload, and is the one column a write may
	// leave NULL — a session established without a payload reads back as the
	// nil it went in as rather than as a zero value.
	DataColumn = "data"
	// LastSeenAtColumn is the liveness signal the store's policy compares
	// against. It is last_seen_at rather than the convention's
	// last_updated_at because it records a read rather than a mutation — see
	// sessions/database/migrations.
	LastSeenAtColumn = "last_seen_at"
	// ExpiresAtColumn is the deadline the sweeper collects rows past, and it
	// appears in no read predicate. Whether a session is live is decided above
	// this layer from created_at and last_seen_at, so that both backends answer
	// the question identically and clock skew between a writer and a reader
	// cannot hide a live session.
	ExpiresAtColumn = "expires_at"
	// VersionColumn is the payload's shape stamp, a column rather than part of
	// the blob so that it catches a changed payload shape without being
	// unreadable itself.
	VersionColumn = "version"
)

// SweepCutoffArg is the argument the sweep binds its deadline through.
//
// It is named for what it is rather than for the column it is compared against,
// because the two are not the same value: expires_at is a row's own deadline
// and this is the instant the sweep is asking about. See sweepDelete, and
// querygen.AtMostArgument for why the instant is bound at all rather than read off
// the server.
const SweepCutoffArg = "cutoff"

// KeptSessionArg is the argument the bulk revocation excludes one row through.
//
// It is optional, and that is what lets one statement serve both revocations. A
// user signing every other device out excludes the session they are asking
// from; a user signing all of them out excludes nothing, leaves the argument
// unset, and the predicate then excludes an identifier no row holds. See
// querygen.OptionalArgument, and heldDelete.
//
// It is named for the row it spares rather than for the id column it is
// compared against, because the two are not the same value — the same reason
// SweepCutoffArg is named for the instant rather than for expires_at.
const KeptSessionArg = "kept_session_id"

// Columns is every column, in the order the DDL declares them, the emitted
// INSERT supplies them, and every SELECT here projects them.
//
// created_at is among them and is not database-owned, which is where this table
// parts company with the module's convention: a session's creation time anchors
// an absolute timeout the store computes, so it is the store's to stamp rather
// than the server's. querygen.ForInsert would strip it, which is why the insert
// below is rendered from this list directly.
var Columns = []string{
	querygen.IDColumn,
	ScopeColumn,
	PrincipalColumn,
	DataColumn,
	DeviceNameColumn,
	IPAddressColumn,
	UserAgentColumn,
	LoginMethodColumn,
	querygen.CreatedAtColumn,
	LastSeenAtColumn,
	ExpiresAtColumn,
	VersionColumn,
}

// AttributionColumns is the pair every enumeration and every revocation is
// keyed on, in the order their predicates render.
//
// It is spelled once because three statements share it and a fourth would be
// wrong if it did not: a revocation keyed on the principal alone would reach
// across tenants, and one keyed on the scope alone would sign out everybody in
// it. The pair is the key, and this is the pair.
var AttributionColumns = []string{ScopeColumn, PrincipalColumn}

// attributionMatches renders AttributionColumns as the predicates a statement
// keys on.
func attributionMatches() []querygen.Match {
	matches := make([]querygen.Match, 0, len(AttributionColumns))
	for _, column := range AttributionColumns {
		matches = append(matches, querygen.Match{Column: column})
	}

	return matches
}

// RecordColumns is what the read projects, which is a session record and not a
// session row.
//
// The id is left out because the caller already has it — it is what they asked
// with — and expires_at because nothing above this layer reads it. Narrowing
// the projection is what keeps the column that exists for the sweeper's benefit
// out of the record the store hands back.
var RecordColumns = []string{
	ScopeColumn,
	PrincipalColumn,
	DataColumn,
	DeviceNameColumn,
	IPAddressColumn,
	UserAgentColumn,
	LoginMethodColumn,
	querygen.CreatedAtColumn,
	LastSeenAtColumn,
	VersionColumn,
}

// ListedColumns is what the enumeration projects: a record plus the identifier
// it is stored under, less the two columns the caller named to ask.
//
// The id is here where RecordColumns leaves it out, and that inversion is the
// whole difference between the two reads. A caller reading by identifier
// already holds it; a caller enumerating a principal's sessions is asking which
// identifiers those are, and the answer is unusable without them — it is what
// the revocation is then aimed at, and what IsCurrent is decided by.
var ListedColumns = []string{
	querygen.IDColumn,
	DataColumn,
	DeviceNameColumn,
	IPAddressColumn,
	UserAgentColumn,
	LoginMethodColumn,
	querygen.CreatedAtColumn,
	LastSeenAtColumn,
	VersionColumn,
}

// UpdateColumns is what the overwrite assigns.
//
// created_at is deliberately absent. It anchors the absolute timeout, and an
// update — a touch, a payload save, the write half of a renewal — must never
// move it, or the timeout it anchors would stop being absolute. Leaving it out
// of the statement makes that structural rather than a rule somebody has to
// remember, and leaving it out *here* is what makes it structural in every
// dialect at once.
// The attribution and the device metadata are absent for the same structural
// reason, one step further out: who holds a session and what it was established
// from are facts about its establishment. A touch or a payload save that could
// move them would be a session that changes hands without anybody deleting a
// row, and a renewal carries them across by writing a fresh row from the record
// it read rather than by assigning them here.
var UpdateColumns = []string{
	DataColumn,
	LastSeenAtColumn,
	ExpiresAtColumn,
	VersionColumn,
}

// NullableColumns names the columns a write may set to NULL, which lives in the
// schema neither this package nor querygen reads.
var NullableColumns = []string{DataColumn}

// unkeyedColumns is the table's shape as the two statements that act on a set
// see it: every column but the id.
//
// querygen derives a statement's id predicate from the column list it is handed,
// so leaving the id out is how a statement says it keys on something else — on a
// deadline for the sweep, on the attribution pair for the bulk revocation. What
// it does not decide is which rows come back, and a DELETE projects nothing, so
// this list costs either statement nothing else.
var unkeyedColumns = slices.DeleteFunc(slices.Clone(Columns), func(column string) bool {
	return column == querygen.IDColumn
})

// Render returns the canonical sqlc input for one dialect: every statement the
// backend executes, in the order below, as the bytes the committed .sql beside
// this file holds.
//
// The order is the order a session goes through: created, read, overwritten,
// asked about, removed, enumerated and revoked beside its siblings, and finally
// collected once its deadline has passed.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// The table registers itself here rather than through StandardCRUD,
	// which is what registers a conventional table and which this one gets
	// none of. A consumer reading the registry back to truncate a database
	// between integration tests has to find this table whether or not the
	// statements over it happen to come from the standard set.
	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		createInsert(g),
		recordRead(g),
		recordUpdate(g),
		existenceCheck(g),
		rowDelete(g),
		heldRead(g),
		heldRowDelete(g),
		heldDelete(g),
		sweepDelete(g),
	})
}

// createInsert renders the creation of a session row, ignoring a row that is
// already there.
//
// The conflict clause is what makes ErrIDConflict reportable without parsing a
// driver's error: a duplicate primary key leaves zero rows affected instead of
// raising a dialect-specific SQLSTATE. It is also what keeps a conflict inside
// Rename's transaction from aborting it — Postgres marks a transaction failed
// after a constraint violation, so a caller could not otherwise distinguish
// "that identifier exists" from "your transaction is now unusable".
func createInsert(g *querygen.Generator) *querygen.Query {
	return g.InsertIgnoreQuery("CreateSession", SessionsTable, Columns, NullableColumns,
		querygen.Match{Column: querygen.IDColumn})
}

// recordRead renders the read of one session record by identifier.
func recordRead(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery("GetSession", SessionsTable, Columns,
		querygen.Read{Projection: RecordColumns})
}

// recordUpdate renders the overwrite of an existing session row.
//
// The WHERE clause is the whole guarantee the database backend exists for: a
// row that has been deleted is not recreated, so a request that read a session
// immediately before it was signed out cannot write it back afterwards.
func recordUpdate(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery("UpdateSession", SessionsTable, Columns, UpdateColumns, NullableColumns)
}

// existenceCheck renders the question the overwrite falls back on.
//
// It is asked only when an UPDATE reported no rows affected, which MySQL also
// reports for an update that matched a row and changed nothing — two saves of
// an identical payload within the same microsecond, say. Without it the second
// would be answered "no such session" and sign a user out over a no-op.
func existenceCheck(g *querygen.Generator) *querygen.Query {
	return g.ExistsQuery("SessionExists", SessionsTable, Columns)
}

// rowDelete renders the removal of one session row.
//
// A hard delete rather than a stamp: this table carries no archived_at, because
// a swept table sized by live sessions is the whole point of the sweeper and a
// soft delete would either do nothing or defeat it.
func rowDelete(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery("DeleteSession", SessionsTable, Columns)
}

// heldRead renders the enumeration: every session one principal holds within
// one scope, newest first.
//
// It is unpaged. A person holds a handful of sessions and a security page shows
// all of them; a page of that list would be a "sign out everywhere" whose
// button acted on the rows the reader happened to be looking at. The unpaged
// form also takes no filter window, which is the other half of the same point —
// the set is defined by who holds it, not by when a caller says they are
// interested in.
//
// Newest first is the order the page reads in, and the tie-break is the id, so
// two sessions established in the same microsecond come back the same way
// twice. Expiry is not a predicate here for the reason it is not one on the
// keyed read: whether a session is live is the store's decision, made from the
// record's own anchors against the policy, and a row this read hid would be a
// row the by-identifier path still answered.
func heldRead(g *querygen.Generator) *querygen.Query {
	return g.JunctionListAllQuery("ListSessionsForPrincipal", SessionsTable, ListedColumns, nil,
		[]querygen.Order{
			{Column: querygen.CreatedAtColumn, Descending: true},
			{Column: querygen.IDColumn, Descending: true},
		},
		attributionMatches()...,
	)
}

// heldRowDelete renders the revocation of one named session, keyed on the pair
// that says whose it is as well as on the identifier.
//
// The attribution is in the predicate rather than checked in Go beforehand, and
// that is the whole statement. A read-then-delete decides on a row read
// earlier; more to the point, a revocation authorized in one round trip and
// executed in another is one an application can be talked out of by getting the
// two out of step. Here the server evaluates "this session, and it is yours" at
// the instant the row goes, and the count reports whether it was.
//
// A caller naming somebody else's session therefore gets a count of zero, which
// the backend reports as an absent session rather than as a refusal. That is
// deliberate: a distinct "not yours" would confirm the identifier names a live
// session belonging to someone.
func heldRowDelete(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery("DeleteSessionForPrincipal", SessionsTable, Columns, attributionMatches()...)
}

// heldDelete renders both bulk revocations: every session a principal holds
// within a scope, optionally sparing one.
//
// It is one statement rather than two because the two differ in a single
// predicate whose argument may be absent — see KeptSessionArg. Written as two,
// "sign out everywhere" and "sign out my other devices" would be two texts that
// could come to disagree about what "every session I hold" means, which is the
// disagreement a revocation cannot afford.
//
// The column list is the table's without the id, which is how a statement here
// says it keys on something other than a row: with the id in it, querygen would
// render an equality on the identifier and this would delete one session under
// a name that promises all of them. The excluded id arrives as a Match instead,
// where its polarity is written down.
func heldDelete(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery("DeleteSessionsForPrincipal", SessionsTable, unkeyedColumns,
		append(attributionMatches(), querygen.Match{
			Column:  querygen.IDColumn,
			Arg:     KeptSessionArg,
			Against: querygen.OptionalArgument,
			Exclude: true,
		})...,
	)
}

// sweepDelete renders the removal of every row whose deadline has passed.
//
// The deadline is compared against a bound instant rather than the server's
// clock, which is querygen.AtMostArgument and is the one decision in this file worth
// stopping at. expires_at is written by the backend as now-plus-a-TTL from the
// clock it was constructed with — the interface hands it a duration, not a
// deadline — so the server's CURRENT_TIMESTAMP is a second clock, and under a
// test clock that only moves when a test moves it the two are years apart.
// Binding the same clock's reading keeps both sides of the comparison inside
// one clock, which is what the server-clock comparand asks for rather than an
// exception to it.
func sweepDelete(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery("SweepSessions", SessionsTable, unkeyedColumns, querygen.Match{
		Column:  ExpiresAtColumn,
		Arg:     SweepCutoffArg,
		Against: querygen.AtMostArgument,
	})
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

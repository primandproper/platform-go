package queries

import (
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// DevicesTable is the sign-in device table at its canonical, unprefixed spelling
// — what the emitted .sql names, and what the store's own prefix rendering
// starts from.
//
// The signin_ segment is the schema's own, so a table says which package created
// it even in a database shared between applications.
const DevicesTable = "signin_devices"

// TableNames is every table this package owns, which is one.
//
// It is a list rather than the constant above because the querygen registry
// takes one, and because a consumer reading that registry back to truncate a
// database between integration tests is asking "what tables does this component
// have rows in" rather than "what does it generate SQL for".
var TableNames = []string{DevicesTable}

// The columns the statements below name, and the store binds by.
//
// Exported because both halves spell them: the arguments the generated params
// carry are named from these, and a column spelled twice is a column that can be
// spelled differently.
const (
	// ScopeColumn is whose directory the login is in. Every statement here but
	// the sweep filters on it, and it is bound as the tenancy.Scope itself
	// rather than as a string derived from one; see unison.yaml, where that type
	// override lives.
	ScopeColumn = "scope"
	// FamilyIDColumn is which login: signin's family identifier, which every
	// token a login's refreshes mint shares.
	FamilyIDColumn = "family_id"
	// UserIDColumn is whose login it is. It carries no REFERENCES — see the
	// migrations package — so it is an identifier this table cannot resolve
	// rather than a foreign key.
	UserIDColumn = "user_id"
	// IPAddressColumn is the address the login was last renewed from, as the
	// consumer's extractor read it.
	IPAddressColumn = "ip_address"
	// UserAgentColumn is the user agent the login was last renewed by.
	UserAgentColumn = "user_agent"
	// DeviceNameColumn is what the client calls the device holding the login.
	DeviceNameColumn = "device_name"
	// FirstSeenAtColumn is when the login was first recorded. The insert
	// writes it and the conflict branch leaves it alone.
	FirstSeenAtColumn = "first_seen_at"
	// LastSeenAtColumn is when the login was last renewed.
	LastSeenAtColumn = "last_seen_at"
	// ExpiresAtColumn is the latest the login could still be alive, and the
	// column the sweep is keyed on.
	ExpiresAtColumn = "expires_at"
)

// ExpiresBeforeArg is the horizon the sweep binds: every row whose deadline is
// at or before it goes.
//
// It is named rather than left to querygen's default, because the store binds
// it from its own clock — the same one that stamps the row — and the name is
// what its params struct spells.
const ExpiresBeforeArg = "expires_before"

// FamilyIDsArg is the set the annotator's read binds: the families one listing
// returned.
const FamilyIDsArg = "family_ids"

// Columns is the whole row, in the order the DDL declares it.
//
// It is what every statement is rendered from and what every read projects —
// nothing here is a secret, so there is no narrower projection to keep one out
// of. querygen derives a statement's id predicate from the list it is handed and
// this table has no id — the key is the scope and the login — so every predicate
// below is one the statement names explicitly.
var Columns = []string{
	ScopeColumn,
	FamilyIDColumn,
	UserIDColumn,
	IPAddressColumn,
	UserAgentColumn,
	DeviceNameColumn,
	FirstSeenAtColumn,
	LastSeenAtColumn,
	ExpiresAtColumn,
}

// InsertColumns is what the upsert's INSERT writes, which is every column: the
// store binds both stamps from its own clock rather than leaving one to a
// server default, so that no row holds times from two clocks.
var InsertColumns = Columns

// RenewColumns is what the upsert's conflict branch assigns: what the request
// that renewed the login said, when it renewed, and how long the login can now
// live.
//
// The scope and the login are the conflict target and are never assigned. The
// user is not assigned either: a family belongs to the person whose sign-in
// minted it, and every refresh in it is theirs, so a write that moved a row to
// somebody else would be a write that had confused two logins. first_seen_at is
// what makes the row a login rather than a token, so a renewal leaves it.
var RenewColumns = []string{
	IPAddressColumn,
	UserAgentColumn,
	DeviceNameColumn,
	LastSeenAtColumn,
	ExpiresAtColumn,
}

// The query names the generated querier's methods are built from. They are
// spelled here because the store names them too — through the generated params
// types — and because the drift gate beside this file asserts on this exact set.
const (
	UpsertDeviceQuery           = "UpsertSignInDevice"
	ListDevicesForFamiliesQuery = "ListSignInDevicesForFamilies"
	ListDevicesForUserQuery     = "ListSignInDevicesForUser"
	DeleteDevicesForUserQuery   = "DeleteSignInDevicesForUser"
	SweepDevicesQuery           = "SweepSignInDevices"
)

// UnscopedStatements names every statement here that does not filter on the
// scope, and says why on the entry. The render test pins the set, so the next
// statement that would read or write across tenants fails a test and is argued
// rather than slipped in.
var UnscopedStatements = map[string]string{
	SweepDevicesQuery: "the store's own machinery, collecting what has expired in every scope at once",
}

// Render returns the canonical sqlc input for d: the five statements this store
// executes, in one file's worth of text.
//
// It is what authentication/signin/devices/internal/queriesgen writes to the
// .sql files beside this one, and what CI regenerates to check the committed
// copies still match. Those files are sqlc-gen-unison's input, so what the store
// executes is this text exactly — the generated devicesdb package carries it per
// dialect, with the consumer's table prefix substituted once at construction.
//
// The order is the order a row goes through: written and renewed, read back for
// a listing, exported, and finally deleted — with its owner, or by the sweep.
//
// # Why there is no standard set
//
// [querygen.Generator.StandardCRUD] serves a table with a surrogate id, a paged
// list keyed on it, and the convention triple of timestamps. This table has none
// of that, and every absence is deliberate — see the migrations package. Its key
// is a login rather than a surrogate; the reads are bounded by a listing's page
// and by the sweep rather than paged; and an archived_at would keep a row nobody
// reads.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// The one table this package owns. StandardCRUD would have registered it, and
	// StandardCRUD cannot serve this table at all — so the registration is made by
	// the table existing rather than by something choosing to emit its standard
	// set, which is the distinction the registry is built around.
	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		upsert(g),
		listForFamilies(g),
		listForUser(g),
		deleteForUser(g),
		sweep(g),
	})
}

// upsert is the write every mint makes: a login's first token inserts its row,
// and every refresh after that converges on the same row and renews it.
//
// It converges rather than inserting a row per token because a refresh is the
// same login renewed, and a person shown their logins sees one entry for it. The
// conflict branch assigns [RenewColumns] and nothing else — see there for why the
// owner and the first stamp are left alone.
func upsert(g *querygen.Generator) *querygen.Query {
	return g.UpsertQuery(UpsertDeviceQuery, DevicesTable, Columns, InsertColumns, RenewColumns, nil,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: FamilyIDColumn},
	)
}

// listForFamilies is the annotator's read: what was recorded for one person's
// logins, among the families the listing it annotates is about to return.
//
// It names the person as well as the families. A listing only ever names
// families it read for that person, so the predicate changes no answer a correct
// caller gets — and it is what keeps a caller that passed somebody else's family
// from reading where that somebody signed in from.
//
// The set binds last, as querygen requires. Its cardinality is the listing's
// page, so it is bounded by whoever bounds that.
func listForFamilies(g *querygen.Generator) *querygen.Query {
	return g.SetReadQuery(ListDevicesForFamiliesQuery, DevicesTable, Columns,
		querygen.Read{},
		querygen.SetKey{Column: FamilyIDColumn, Arg: FamilyIDsArg},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: UserIDColumn},
	)
}

// listForUser is every row one person has, for the export a subject access
// request makes.
//
// It is unpaged, and the bound is structural: a row is one login, and the sweep
// deletes it once the login can no longer be alive — so what one person has is
// their live logins rather than their history. A nil junction on the unpaged
// list is the construct for a many-row read keyed on something other than an id.
func listForUser(g *querygen.Generator) *querygen.Query {
	return g.JunctionListAllQuery(ListDevicesForUserQuery, DevicesTable, Columns, nil,
		[]querygen.Order{{Column: FirstSeenAtColumn}, {Column: FamilyIDColumn}},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: UserIDColumn},
	)
}

// deleteForUser removes every row one person has, which is the whole of an
// erasure.
func deleteForUser(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(DeleteDevicesForUserQuery, DevicesTable, Columns,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: UserIDColumn},
	)
}

// sweep is the removal of every row whose login can no longer be alive.
//
// It spans every scope, which is the one statement here that does — see
// [UnscopedStatements] — and it carries no cap. Device rows are small and the
// index on expires_at makes the delete proportional to what is actually dead
// rather than to the table, so this is [querygen.Generator.DeleteQuery] with a
// horizon rather than [querygen.Generator.PruneQuery], exactly as refreshtokens'
// sweep is.
func sweep(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(SweepDevicesQuery, DevicesTable, Columns,
		querygen.Match{Column: ExpiresAtColumn, Against: querygen.AtMostArgument, Arg: ExpiresBeforeArg},
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

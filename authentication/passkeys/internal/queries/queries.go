package queries

import (
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// CredentialsTable is the one table this package owns, at its canonical,
// unprefixed spelling — what the emitted .sql names, and what the store's own
// prefix rendering starts from.
//
// It is webauthn_credentials rather than passkey_credentials, though the package
// above is authentication/passkeys. The package is named for what a product
// calls the thing; the table is named for the specification that decides its
// shape, and it sits beside webauthn_sessions in a consumer's schema, which is
// where a reader looking for it will look.
//
//nolint:gosec // G101: a table name, and the table stores public keys rather than secrets.
const CredentialsTable = "webauthn_credentials"

// TableNames is every table this package owns, in the order the DDL creates it.
// One entry today; the list is what [Render] feeds the querygen registry, which
// a consumer reads back to truncate a database.
var TableNames = []string{CredentialsTable}

// ScopeColumn is the tenancy dimension the table carries and every statement is
// keyed on. It is a column, not a convention: an unscoped read of this schema is
// not expressible, because there is no statement that omits it.
const ScopeColumn = "scope"

// The columns the reads and writes below name. Exported because the store spells
// them too — its argument structs key on them — and two spellings of one column
// is the drift this package exists to prevent.
const (
	// UserColumn is the consumer's own user id, not the WebAuthn user handle.
	// Resolving a handle to a user is the consumer's directory's job; see
	// authentication/passkeys.NewUserSource.
	UserColumn = "belongs_to_user"
	// CredentialIDColumn is the authenticator's credential ID, stored as the
	// bytes it is. It is what a login arrives holding, and what the live-rows
	// unique index covers.
	//
	//nolint:gosec // G101: a column name, and a credential ID is public — every assertion carries one in the clear.
	CredentialIDColumn = "credential_id"
	// PublicKeyColumn is the COSE public key a later assertion is verified
	// against.
	PublicKeyColumn = "public_key"
	// TransportsColumn is the authenticator's transport hints, encoded into one
	// column by the store — see authentication/passkeys.
	TransportsColumn = "transports"
	// FriendlyNameColumn is whatever the person called this passkey.
	FriendlyNameColumn = "friendly_name"
	// SignCountColumn is the authenticator's counter as of the last assertion
	// this row verified, which is the value clone detection compares against.
	SignCountColumn = "sign_count"
	// LastUsedAtColumn is when the ceremony that moved the counter happened. It
	// is beside last_updated_at rather than instead of it because the two are
	// different facts from different clocks: this one is the caller's reading of
	// when the passkey signed somebody in, and the conventional one is the
	// server's reading of when the row changed.
	LastUsedAtColumn = "last_used_at"
)

// CredentialColumns is the table's full shape, in the order the emitted SELECTs
// project it, which is also the order the store's conversions are written in.
var CredentialColumns = []string{
	querygen.IDColumn,
	ScopeColumn,
	UserColumn,
	CredentialIDColumn,
	PublicKeyColumn,
	TransportsColumn,
	FriendlyNameColumn,
	SignCountColumn,
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	LastUsedAtColumn,
	querygen.ArchivedAtColumn,
}

// The query names the generated querier's methods are built from. They are
// spelled here because the store names them too — through the generated params
// types — and because the drift gate beside this file asserts on this exact set.
//
//nolint:gosec // G101: statement names. gosec reads "Credential" as a secret; every one of these is a method on the generated querier.
const (
	CreateCredentialQuery            = "CreateCredential"
	GetCredentialQuery               = "GetCredential"
	GetCredentialByCredentialIDQuery = "GetCredentialByCredentialID"
	GetArchivedCredentialQuery       = "GetArchivedCredential"
	ListCredentialsForUserQuery      = "ListCredentialsForUser"
	RecordCredentialUseQuery         = "RecordCredentialUse"
	ArchiveCredentialForUserQuery    = "ArchiveCredentialForUser"
)

// InsertColumns is what the create supplies values for: everything but the
// columns the database owns, and last_used_at.
//
// last_used_at is excluded deliberately. A passkey that has just been registered
// has verified nothing, and a creation that stamped the column would make "has
// this credential ever signed anybody in" unanswerable from the row — which is
// the question a deployment pruning passkeys nobody uses is asking. The column
// is NULL until [RecordCredentialUseQuery] writes it.
func InsertColumns() []string {
	return querygen.ForInsert(CredentialColumns, LastUsedAtColumn)
}

// UseColumns is what the sign-count write assigns: the counter and the instant
// the assertion that moved it happened.
//
// last_updated_at is not in the list and is still written — querygen appends the
// conventional stamp for a table whose column list carries it, from the server's
// clock. That is the division the schema describes: this statement's two
// assignments are the ceremony's facts, and the stamp is the row's.
var UseColumns = []string{SignCountColumn, LastUsedAtColumn}

// keyedColumns is the table's shape as a read keyed on something other than the
// row's own id sees it: every column but the id.
//
// querygen derives a single-row statement's predicates from the column list it
// is handed — the id predicate is rendered when the list has an id, exactly as
// the archived one is — so leaving the id out is how a statement says it keys on
// something else. What it does not decide is what comes back: the projection is
// a separate list, and the read below still returns the id.
func keyedColumns() []string {
	kept := make([]string, 0, len(CredentialColumns))
	for _, column := range CredentialColumns {
		if column != querygen.IDColumn {
			kept = append(kept, column)
		}
	}

	return kept
}

// Render returns the canonical sqlc input for d: the seven statements this store
// executes, in one file's worth of text.
//
// It is what authentication/passkeys/internal/queriesgen writes to the .sql
// files beside this one, and what CI regenerates to check the committed copies
// still match. Those files are sqlc-gen-unison's input, so what the store
// executes is this text exactly — the generated passkeysdb package carries it per
// dialect, with the consumer's table prefix substituted once at construction.
//
// There is no StandardCRUD call, and the absence is a decision rather than an
// omission. The standard set brings a paged list with a filter window, a cursor
// and two counts; nothing pages passkeys, because a person has a handful and the
// read every ceremony makes wants all of them. What is left of the set after
// that is a get, a create and an archive, each of which this file needs in a
// shape the standard one does not have: the archive is keyed on the owner as
// well as the row, and the create's column list drops last_used_at.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// The table is registered for existing, not for the queries below — see
	// querygen.Registry. Registering the list here keeps it fed by the table
	// existing rather than by what currently produces its SQL.
	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		create(g),
		read(g),
		lookup(g),
		archivedRead(g),
		listForUser(g),
		recordUse(g),
		archiveForUser(g),
	})
}

// create is the insert of one registered passkey.
//
// It is a plain insert rather than an upsert or an insert-ignore, and the unique
// index is why. A registration that collides has found a credential already
// enrolled in this scope, and there is no converging write that is right for it:
// replacing the row would hand one authenticator's counter to another row's
// history, and ignoring the collision would report success while storing
// nothing. The store turns the constraint violation into a sentinel; see
// authentication/passkeys.ErrCredentialRegistered.
func create(g *querygen.Generator) *querygen.Query {
	return g.InsertQuery(CreateCredentialQuery, CredentialsTable, InsertColumns(), nil)
}

// read is the keyed get: one live row of the scope, by its id.
//
// It is not on authentication/passkeys.Store. Nothing outside this package holds
// a passkey's row id before it has been handed one, and what this statement is
// for is the read-back: the create and the sign-count write both answer with the
// row they left, read on the caller's own transaction.
func read(g *querygen.Generator) *querygen.Query {
	return g.GetQuery(GetCredentialQuery, CredentialsTable, CredentialColumns,
		querygen.Match{Column: ScopeColumn})
}

// lookup is the read a login runs: the live row for the credential ID the
// authenticator just returned.
//
// It keys on the credential id rather than on the row's, which is what
// keyedColumns is for — a column list with no id renders no id predicate. The
// projection is the whole table, because the caller reading this row is about to
// verify an assertion against every column in it.
func lookup(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery(GetCredentialByCredentialIDQuery, CredentialsTable, keyedColumns(),
		querygen.Read{Projection: CredentialColumns},
		querygen.Match{Column: CredentialIDColumn},
		querygen.Match{Column: ScopeColumn},
	)
}

// archivedRead is the read the archive answers with: the row it just hid, on the
// transaction that hid it.
//
// It exists because MySQL has no RETURNING, so a write that answers with its row
// reads it back in a second statement — and because archiving is the one write
// here whose result no other read can see. Every other single-row statement over
// this table filters archived_at IS NULL, so a store that archived a row and read
// it back through one of them would find nothing.
//
// It is rendered from no column list at all, which is how a read says it must
// see archived rows: querygen derives the archived predicate from the columns it
// is handed, so a statement keyed entirely on its matches carries none. What
// takes that predicate's place is its complement — archived_at IS NOT NULL — so
// the read-back asserts the thing it was called to confirm, and a guard that
// matched nothing cannot be read back as a success.
//
// There is no companion for the create or for the sign-count write. A row those
// two just wrote is live, so [read] reaches it on the same transaction.
func archivedRead(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery(GetArchivedCredentialQuery, CredentialsTable, nil,
		querygen.Read{Projection: CredentialColumns},
		querygen.Match{Column: querygen.IDColumn},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: querygen.ArchivedAtColumn, Against: querygen.NoValue, Exclude: true},
	)
}

// listForUser is every live passkey one user has, which is what a ceremony
// assembles its webauthn.User from and what a settings page lists.
//
// It is unpaged, and that is a ruling rather than a shortcut. A paged read would
// bring this corpus a cursor, a filter window and a second statement for the
// descending direction, all to page a set the platform itself bounds: an
// authenticator is a physical thing, and a person who has enrolled more than a
// handful has enrolled a handful. It is authentication/passwordreset's argument
// on a table with the same shape — paging a handful means a caller who forgets
// to loop reads some of somebody's credentials and treats the rest as absent,
// and here that caller is a login, which would then refuse a passkey the person
// is holding.
//
// It is [querygen.Generator.JunctionListAllQuery] over a nil junction, which is
// how that constructor spells "this list reads one table": the paged form with
// the window, the cursor, the LIMIT and the counts removed, leaving the
// projection, the matches, the archived predicate and the ordering.
//
// The order is created_at and then the id, ascending — a person's passkeys in
// the order they enrolled them, with the id breaking a tie so two registered in
// the same instant come back in a stable order rather than whichever the engine
// happened to scan first.
func listForUser(g *querygen.Generator) *querygen.Query {
	return g.JunctionListAllQuery(ListCredentialsForUserQuery, CredentialsTable, CredentialColumns, nil,
		[]querygen.Order{{Column: querygen.CreatedAtColumn}, {Column: querygen.IDColumn}},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: UserColumn},
	)
}

// recordUse is the sign-count write-back, which is the statement this whole
// table is here for.
//
// The counter it assigns is the authenticator's, as of the assertion that has
// just verified. The next assertion compares against it, and a count that did
// not go up means two authenticators are answering for one credential — which is
// the only clone detection WebAuthn has. A deployment that treats this write as
// best-effort has a working passkey login and no clone detection, and nothing
// says so; the store therefore reports its failure rather than swallowing it,
// and its row count is the answer to whether it wrote at all.
//
// It is keyed on the row's id, the scope, and archived_at IS NULL, which the
// column list renders. A revoked passkey verifies nothing, so a write that
// reached one would be writing a counter nobody will ever compare against.
func recordUse(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(RecordCredentialUseQuery, CredentialsTable, CredentialColumns, UseColumns, nil,
		querygen.Match{Column: ScopeColumn})
}

// archiveForUser is the revocation: the soft delete of one of a user's passkeys.
//
// The owner is a predicate rather than a check the caller makes first, and that
// is the whole of the authorization. A read-then-archive is two statements with
// a window between them, and the row count of this one is the answer: a
// credential that belongs to somebody else, or is already archived, or is in
// another scope, moves nothing and reports zero.
func archiveForUser(g *querygen.Generator) *querygen.Query {
	return g.ArchiveQuery(ArchiveCredentialForUserQuery, CredentialsTable, CredentialColumns,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: UserColumn},
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

package queries

import (
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// RegisteredClientsTable is the one table this package owns, at its canonical
// spelling — what the emitted .sql names, and what oauth2clients' own prefix
// rendering starts from.
//
// It is oauth2_registered_clients and not oauth2_clients, and the second name
// was not available: authentication/oauth2server/database already creates a
// table called that, for the anonymous RFC 7591 registrations its /register
// endpoint writes. A deployment runs both schemas, both are CREATE TABLE IF NOT
// EXISTS, and two tables of one name would leave the second migration a silent
// no-op followed by a store selecting columns that are not there.
const RegisteredClientsTable = "oauth2_registered_clients"

// TableNames is every table oauth2clients owns.
//
// authentication/oauth2clients/migrations is where a consumer gets these names
// rendered at their prefix. This list is the canonical spelling, and
// migrations.Tables reads the DDL, so the two are cross-checked against each
// other in this package's tests rather than one being derived from the other.
var TableNames = []string{RegisteredClientsTable}

// ScopeColumn is the tenancy dimension every consumer-reachable statement is
// keyed on. It is a column, not a convention.
//
// One statement below names it nowhere, and that is the only one: the
// authorization server's lookup by client_id, which takes no scope because it
// *resolves* one. See byClientID.
const ScopeColumn = "scope"

// BelongsToUserColumn is the person who owns a registration, and the empty
// string is one the deployment or the tenant administers on behalf of nobody.
//
// It is a second key rather than something the scope encodes, for the reason
// the tenancy convention gives about composite values: "the clients in this
// tenant" and "the clients this person owns" are two questions, and a scope
// carrying a user identifier answers neither as the two facts it is. It is also
// what would break the administered page — a Scope matches only itself, so an
// administrator listing a tenant would find none of its people's clients.
const BelongsToUserColumn = "belongs_to_user"

// ClientIDColumn is the identifier the client sends at /authorize and /token.
//
// It is not the row's primary key. Keeping the two apart is what lets a
// client_id be rotated later without orphaning every audit entry and console
// URL that names the row, and it is why there are two reads below rather than
// one: GetRegisteredClient takes the row's id and a scope, and the lookup takes
// this and neither.
const ClientIDColumn = "client_id"

// The remaining columns the statements below name outside the column list.
//
// SecretHashColumn holds oauth2server.Hash of the client secret and never the
// secret. It is immutable: a row that could assign its own digest would make
// rotation something an update did quietly rather than something a caller was
// handed a new secret for.
//
// RedirectURIsColumn and ScopesColumn are JSON lists. Both are mutable, because
// revising them is the whole reason this table carries an update: the
// alternative to fixing a mistyped redirect URI is archiving the registration
// and minting a new one, which invalidates every token the client holds.
const (
	SecretHashColumn   = "secret_hash"
	NameColumn         = "name"
	DescriptionColumn  = "description"
	RedirectURIsColumn = "redirect_uris"
	ScopesColumn       = "scopes"
)

// RegisteredClients is a client somebody registered on purpose: named, listed,
// revised, archived, and answerable to a permission.
//
// It takes querygen's standard set, unlike comments and issuereports, because
// it is a resource in the sense that set assumes — addressable by its own id,
// scoped by one ownership column, and soft-deleted. What the standard set
// cannot express is the other two reads, which are written out below.
var RegisteredClients = Table{
	Name: RegisteredClientsTable,
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		BelongsToUserColumn,
		NameColumn,
		DescriptionColumn,
		ClientIDColumn,
		SecretHashColumn,
		RedirectURIsColumn,
		ScopesColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
}

// options renders the table's shape as the options StandardCRUD reads.
//
// The ownership column is the scope, so every single-row statement and the page
// carry it and a row in another registry is not found rather than found and
// returned. The three immutable columns are the ones an update assigning them
// would undermine: the owner, because a row that can reassign its own owner
// makes the owner check a formality; the client_id, because it is the value
// every live token was minted against; and the digest, because rotation is a
// call that hands back a new secret rather than an UPDATE nobody sees.
//
// Two of the standard statements are omitted. The existence check, because
// nothing here asks whether a registration exists without also wanting to read
// it — so emitting one would leave a generated method nobody calls beside a read
// path that answers with less scoping than the caller expects. And the create,
// which is replaced by [create]: this table has a unique index the standard
// insert would raise on, and raising means every backing store parsing a
// dialect's own constraint text to tell a duplicate from a broken database.
func options() []querygen.Option {
	return []querygen.Option{
		querygen.WithEntity("RegisteredClient", "RegisteredClients"),
		querygen.WithOwnership(ScopeColumn),
		querygen.WithImmutable(BelongsToUserColumn, ClientIDColumn, SecretHashColumn),
		querygen.WithOmitted(querygen.ExistsQuery, querygen.CreateQuery),
	}
}

// Render returns the canonical sqlc input for d: every statement the
// oauth2clients store runs, in one file's worth of text.
//
// It is what authentication/oauth2clients/internal/queriesgen writes to the
// .sql beside this file and what CI regenerates to check the committed copy
// still matches. That .sql is sqlc-gen-unison's input, so what the store
// executes is this text exactly: the generated oauth2clientsdb package carries
// it per dialect, with the consumer's table prefix substituted once at
// construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	rendered := g.StandardCRUD(RegisteredClientsTable, RegisteredClients.Columns, options()...)
	rendered = append(rendered, create(g), createdAtRead(g), byClientID(g))

	return querygen.RenderFile(append(rendered, byOwner(g)...))
}

// create records a registration, and reports a client_id already in use as zero
// rows rather than as a raised constraint.
//
// It is an insert-ignore rather than the standard set's plain INSERT for the
// reason authentication/oauth2server/database gives about its own client table:
// the alternative is every caller parsing a dialect's SQLSTATE to tell "this
// identifier is taken" from "the database is broken", and the three engines
// spell that three ways. Zero affected rows says it once, portably.
//
// The conflict is keyed on client_id and not on the row's id, because client_id
// is the column carrying the unique index and the one a collision could
// plausibly happen on — the id is minted per row and never compared across
// registries.
func create(g *querygen.Generator) *querygen.Query {
	return g.InsertIgnoreQuery("CreateRegisteredClient", RegisteredClientsTable,
		RegisteredClients.InsertColumns(), RegisteredClients.Nullable,
		querygen.Match{Column: ClientIDColumn})
}

// createdAtRead is the read-back of the one column the create does not carry:
// the creation time the database assigned it.
//
// created_at is database-owned — it is not in the create's column list, and the
// schema gives it a DEFAULT — so the value the caller handed over still holds
// the zero time when the INSERT returns, and the store reads it back inside the
// same transaction.
//
// It keys on the id alone. The scope is absent because this is not a read a
// caller reaches: it is the create's read-back of the row it has just written,
// by the id it minted for it, and the row is not visible to anything else until
// the transaction commits. The column list is the id and nothing else, which is
// also what leaves the archived predicate off a row that cannot be archived yet.
func createdAtRead(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery("GetRegisteredClientCreatedAt", RegisteredClientsTable,
		[]string{querygen.IDColumn},
		querygen.Read{Projection: []string{querygen.CreatedAtColumn}})
}

// byClientID is the authorization server's lookup, and the one statement in
// this package that names no scope.
//
// It does not omit the scope. It resolves one: client_id is server-minted, is
// globally unique by the index the schema declares, and the row it finds is the
// only thing in the system that knows which registry the client is in. There is
// no scope the caller could have passed, which is what makes this the machinery
// carve-out the tenancy convention names rather than the unscoped read it
// forbids — the same reading webhooks' delivery worker gets. A caller who
// already has a scope wants GetRegisteredClient.
//
// It carries no archived predicate either, and that is deliberate twice over:
// the lookup has to find a withdrawn registration in order to refuse it by
// name, and the store — not the statement — is where that refusal is written,
// so there is one place deciding what an archived client means rather than a
// predicate deciding it silently.
//
// The id leaves the column list and returns in the projection. That is
// querygen's own idiom for a statement keyed on a natural key: the column list
// is what the id and archived predicates are derived from, and the projection is
// what comes back, so dropping both from the first does not drop them from the
// second.
func byClientID(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery("GetRegisteredClientByClientID", RegisteredClientsTable,
		RegisteredClients.ColumnsExcept(querygen.IDColumn, querygen.ArchivedAtColumn),
		querygen.Read{Projection: RegisteredClients.Columns},
		querygen.Match{Column: ClientIDColumn})
}

// byOwner is the self-service page: one person's own registrations, in one
// registry.
//
// It is a second pair rather than an argument on the first, because the
// administered page and this one are different questions and the standard
// list's ownership column is already spent on the scope. Both directions, for
// the reason ListQueries gives: a corpus carrying only the ascending half
// answers sortBy=desc with an ascending page.
func byOwner(g *querygen.Generator) []*querygen.Query {
	return g.ListQueries("ListRegisteredClientsForOwner", RegisteredClientsTable,
		RegisteredClients.Columns,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: BelongsToUserColumn})
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

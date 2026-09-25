package queries

import (
	"slices"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// GrantsTable is the one table this package owns, at its canonical, unprefixed
// spelling — what the emitted .sql names, and what the store's own prefix
// rendering starts from.
//
// It is oauth2_grants rather than grants. The package above is named for what a
// product calls the thing; the table is named for the protocol that decides its
// shape, and it sits beside oauth2_clients and oauth2_access_tokens in a
// consumer's schema, which is where a reader looking for it will look.
const GrantsTable = "oauth2_grants"

// TableNames is every table this package owns, in the order the DDL creates it.
var TableNames = []string{GrantsTable}

// ScopeColumn is the tenancy dimension the table carries and every statement is
// keyed on. It is a column, not a convention: an unscoped read of this schema is
// not expressible, because there is no statement that omits it.
const ScopeColumn = "scope"

// The columns the reads and writes below name. Exported because the store spells
// them too, and two spellings of one column is the drift this package exists to
// prevent.
const (
	// SubjectColumn is the consumer's own identifier for who consented.
	SubjectColumn = "subject"
	// ProviderColumn is the consumer's label for the third party.
	ProviderColumn = "provider"
	// ProviderAccountIDColumn is the provider's own name for the account.
	ProviderAccountIDColumn = "provider_account_id"
	// GrantedScopesColumn is the OAuth2 scopes the provider granted,
	// space-separated as RFC 6749 spells a scope list.
	GrantedScopesColumn = "granted_scopes"
	// AccessTokenColumn is the sealed access token.
	AccessTokenColumn = "access_token"
	// AccessTokenExpiresAtColumn is when the provider said the access token
	// stops working, NULL where it said nothing.
	AccessTokenExpiresAtColumn = "access_token_expires_at"
	// RefreshTokenColumn is the sealed refresh token.
	RefreshTokenColumn = "refresh_token"
	// RevocationReasonColumn says who revoked an archived grant.
	RevocationReasonColumn = "revocation_reason"
)

// GrantColumns is the table's full shape, in the order the emitted SELECTs
// project it, which is also the order the store's conversions are written in.
var GrantColumns = []string{
	querygen.IDColumn,
	ScopeColumn,
	SubjectColumn,
	ProviderColumn,
	ProviderAccountIDColumn,
	GrantedScopesColumn,
	AccessTokenColumn,
	AccessTokenExpiresAtColumn,
	RefreshTokenColumn,
	RevocationReasonColumn,
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	querygen.ArchivedAtColumn,
}

// MetadataColumns is GrantColumns without the two sealed tokens: what the
// export read projects.
//
// A subject access request is owed the fact of a grant and not the credential
// in it, and a read that never selects the ciphertext is one whose answer cannot
// carry it by mistake — a converter that forgot to drop a field would still have
// nothing to drop.
var MetadataColumns = []string{
	querygen.IDColumn,
	ScopeColumn,
	SubjectColumn,
	ProviderColumn,
	ProviderAccountIDColumn,
	GrantedScopesColumn,
	AccessTokenExpiresAtColumn,
	RevocationReasonColumn,
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	querygen.ArchivedAtColumn,
}

// The query names the generated querier's methods are built from.
const (
	CreateGrantQuery            = "CreateGrant"
	GetGrantQuery               = "GetGrant"
	GetGrantForProviderQuery    = "GetGrantForProvider"
	GetRevokedGrantQuery        = "GetRevokedGrant"
	ListGrantsForSubjectsQuery  = "ListGrantsForSubjects"
	RefreshGrantQuery           = "RefreshGrant"
	RevokeGrantQuery            = "RevokeGrant"
	ArchiveGrantQuery           = "ArchiveGrant"
	DeleteGrantForProviderQuery = "DeleteGrantForProvider"
	DeleteGrantsForSubjectQuery = "DeleteGrantsForSubject"
)

// The two arguments named apart from the column they bind against.
const (
	// ExpectedAccessTokenArg is the compare half of the refresh's
	// compare-and-set. It is not access_token because the SET list assigns
	// that column, and one argument name would set the column to the value it
	// was requiring it to already hold — legal SQL that guards nothing.
	ExpectedAccessTokenArg = "expected_access_token"
	// SubjectsArg is the set the export read binds its batch of subjects
	// through.
	SubjectsArg = "subjects"
)

// InsertColumns is what the create supplies values for: everything but the
// columns the database owns, and the revocation reason, which a grant nobody has
// revoked leaves at its default.
func InsertColumns() []string {
	return querygen.ForInsert(GrantColumns, RevocationReasonColumn)
}

// RefreshColumns is what the compare-and-set assigns: the three token facts a
// refresh response carries.
var RefreshColumns = []string{AccessTokenColumn, AccessTokenExpiresAtColumn, RefreshTokenColumn}

// RevokeColumns is what the revocation assigns: who revoked it, and the two
// token columns emptied. The stamp that says when is the archive's.
var RevokeColumns = []string{RevocationReasonColumn, AccessTokenColumn, RefreshTokenColumn}

// nullable is the one column a write assigns that may be absent: a provider that
// names no expiry leaves the access token's unknown.
var nullable = []string{AccessTokenExpiresAtColumn}

// without is columns less the ones named, order preserved.
//
// querygen derives a statement's id and liveness predicates from the column list
// it is handed, so dropping a column from the list is how a statement says it
// keys on something else, or must see archived rows.
func without(columns []string, dropped ...string) []string {
	kept := make([]string, 0, len(columns))

	for _, column := range columns {
		if !slices.Contains(dropped, column) {
			kept = append(kept, column)
		}
	}

	return kept
}

// Render returns the canonical sqlc input for d: the ten statements this store
// executes, in one file's worth of text.
//
// There is no StandardCRUD call. Nothing pages grants — a subject holds one per
// provider — and every write here is a narrower statement than the standard
// set's: the create is preceded by a replacement, the update is a
// compare-and-set, and the archive travels with a write that empties the tokens.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		create(g),
		read(g),
		readForProvider(g),
		revokedRead(g),
		listForSubjects(g),
		refresh(g),
		revoke(g),
		archive(g),
		deleteForProvider(g),
		deleteForSubject(g),
	})
}

// create is the insert of one grant. It always follows [deleteForProvider] on
// the same transaction, which is what makes a consent a replacement rather than
// a collision.
func create(g *querygen.Generator) *querygen.Query {
	return g.InsertQuery(CreateGrantQuery, GrantsTable, InsertColumns(), nullable)
}

// read is the keyed get: one live grant of the scope, by its id. It is what the
// writes read back through, on the caller's own transaction, and what the
// compare-and-set reads the stored access token from before it writes.
func read(g *querygen.Generator) *querygen.Query {
	return g.GetQuery(GetGrantQuery, GrantsTable, GrantColumns,
		querygen.Match{Column: ScopeColumn})
}

// readForProvider is the read a caller holding a subject makes: the live grant
// that subject gave one provider.
//
// It keys on the subject and the provider rather than the row's id, which is
// what dropping the id from the column list says — querygen renders the id
// predicate only for a list that carries the column. archived_at stays in the
// list, so a revoked grant is not a grant this read will answer with.
func readForProvider(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery(GetGrantForProviderQuery, GrantsTable, without(GrantColumns, querygen.IDColumn),
		querygen.Read{Projection: GrantColumns},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: SubjectColumn},
		querygen.Match{Column: ProviderColumn},
	)
}

// revokedRead is the read the revocation answers with: the row it just
// archived, on the transaction that archived it.
//
// It is rendered from no column list at all, which is how a read says it must
// see archived rows, and it asserts the complement — archived_at IS NOT NULL —
// so the read-back confirms the thing it was called to confirm.
func revokedRead(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery(GetRevokedGrantQuery, GrantsTable, nil,
		querygen.Read{Projection: GrantColumns},
		querygen.Match{Column: querygen.IDColumn},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: querygen.ArchivedAtColumn, Against: querygen.NoValue, Exclude: true},
	)
}

// listForSubjects is every grant a batch of subjects hold, revoked ones
// included, projected without either token — which is what
// authentication/grants/privacy's collector is built on.
//
// The column list carries neither the id nor archived_at, so no id predicate
// and no liveness predicate is rendered: an export says what the table holds,
// and a grant somebody revoked is a thing it holds. The projection is
// [MetadataColumns], so the ciphertext is never selected at all.
func listForSubjects(g *querygen.Generator) *querygen.Query {
	return g.SetReadQuery(ListGrantsForSubjectsQuery, GrantsTable,
		without(GrantColumns, querygen.IDColumn, querygen.ArchivedAtColumn),
		querygen.Read{Order: querygen.CreatedAtColumn, Projection: MetadataColumns},
		querygen.SetKey{Column: SubjectColumn, Arg: SubjectsArg},
		querygen.Match{Column: ScopeColumn},
	)
}

// refresh is the compare-and-set a refresh writes through.
//
// It is keyed on the row's id, the scope, archived_at IS NULL, and the access
// token the caller refreshed from — bound under its own argument name, because
// the SET list assigns the same column and one name would set the column to the
// value it was requiring it to hold. Two replicas refreshing one grant both
// read the same token and both call the provider; the first to write moves the
// column and the second matches nothing, which is the zero its caller reads as
// stale.
func refresh(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(RefreshGrantQuery, GrantsTable, GrantColumns, RefreshColumns, nullable,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: AccessTokenColumn, Arg: ExpectedAccessTokenArg},
	)
}

// revoke records who revoked a live grant and empties both token columns. It is
// the first half of a revocation; [archive] is the second, on the same
// transaction, and stamps when from the server's clock.
//
// A revoked grant is a credential nobody should use, and one the provider has
// refused is a credential nobody can — so neither is kept.
func revoke(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(RevokeGrantQuery, GrantsTable, GrantColumns, RevokeColumns, nil,
		querygen.Match{Column: ScopeColumn})
}

// archive is the soft delete: the revocation's stamp.
func archive(g *querygen.Generator) *querygen.Query {
	return g.ArchiveQuery(ArchiveGrantQuery, GrantsTable, GrantColumns,
		querygen.Match{Column: ScopeColumn})
}

// deleteForProvider clears the key a new consent is about to take, live or
// revoked. It is what makes a consent a replacement: the row it deletes is the
// one the unique index would otherwise refuse the insert over.
func deleteForProvider(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(DeleteGrantForProviderQuery, GrantsTable,
		without(GrantColumns, querygen.IDColumn, querygen.ArchivedAtColumn),
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: SubjectColumn},
		querygen.Match{Column: ProviderColumn},
	)
}

// deleteForSubject is the hard delete of every grant one subject holds, revoked
// ones included, which is what authentication/grants/privacy's eraser is built
// on.
func deleteForSubject(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(DeleteGrantsForSubjectQuery, GrantsTable,
		without(GrantColumns, querygen.IDColumn, querygen.ArchivedAtColumn),
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: SubjectColumn},
	)
}

// FileName is the file one dialect's rendered queries are committed to.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}

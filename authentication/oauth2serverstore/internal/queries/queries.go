package queries

import (
	"slices"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// The four tables at their canonical, unprefixed spellings — what the emitted
// .sql names, what sqlc resolves against the DDL, and what the generated
// querier substitutes the consumer's prefix into.
const (
	// ClientsTable holds registrations, which are the only rows here that are
	// not a credential and the only ones keyed on an id.
	ClientsTable = "oauth2_clients"
	// CodesTable holds issued authorization codes, one row per login attempt.
	CodesTable = "oauth2_authorization_codes"
	// AccessTokensTable holds opaque access tokens, checked on every
	// resource-server request.
	//
	//nolint:gosec // G101: a table name, and the table stores digests rather than tokens.
	AccessTokensTable = "oauth2_access_tokens"
	// RefreshTokensTable holds rotating refresh tokens, grouped by family.
	//
	//nolint:gosec // G101: a table name, and the table stores digests rather than tokens.
	RefreshTokensTable = "oauth2_refresh_tokens"
)

// Every column of every table, spelled once.
//
// Three of these tables share most of their column names, so a literal would
// appear in three lists and a rename would have to reach all three — and the
// half that got renamed would go on rendering statements sqlc accepts against
// whichever table still had the old spelling. Naming them is what makes a
// rename one edit.
//
// The ones carrying a decision rather than a value say so; the rest are the
// table's own shape and are documented by the DDL.
const (
	// IDColumn is the registration's identifier, and the only surrogate key in
	// this schema. The three credential tables have none — see HashColumn.
	IDColumn = querygen.IDColumn
	// HashColumn is the hex SHA-256 digest a credential is stored under. It is
	// the primary key of all three credential tables, and it is the whole of
	// what makes a dump of this database unredeemable: the credential itself is
	// never written down.
	HashColumn = "hash"
	// FamilyIDColumn groups the tokens one login issued, which is what a
	// detected reuse revokes as a unit.
	FamilyIDColumn = "family_id"
	// CreatedAtColumn is when a registration was made. It is supplied by the
	// caller rather than defaulted by the schema — see clientInsertColumns.
	CreatedAtColumn = querygen.CreatedAtColumn
	// ExpiresAtColumn is the deadline every guard and every sweep here reads.
	// It is nullable on registrations, where NULL is "does not lapse", and NOT
	// NULL on the three credential tables, where everything expires.
	ExpiresAtColumn = "expires_at"
	// RedeemedAtColumn is NULL until a single-use credential is spent, which is
	// what makes spending it a guarded write rather than a read and a write.
	RedeemedAtColumn = "redeemed_at"
	// RevokedAtColumn is NULL until a token is revoked.
	RevokedAtColumn = "revoked_at"

	// SecretHashColumn is the digest of a client secret, empty for a public
	// client — the same rule as HashColumn, one level up: what a registration
	// stores is never what a client presents.
	SecretHashColumn = "secret_hash"
	// NameColumn is the registration's display name, which is
	// attacker-supplied: render it, never trust it.
	NameColumn = "name"
	// RedirectURIsColumn holds the registered set, as JSON.
	RedirectURIsColumn = "redirect_uris"
	// GrantTypesColumn holds the registered grant types, as JSON.
	GrantTypesColumn = "grant_types"
	// ResponseTypesColumn holds the registered response types, as JSON.
	ResponseTypesColumn = "response_types"
	// ScopesColumn holds a granted scope list, as JSON.
	ScopesColumn = "scopes"
	// TokenEndpointAuthMethodColumn is how the client authenticates at /token.
	TokenEndpointAuthMethodColumn = "token_endpoint_auth_method"

	// ClientIDColumn is the registration a credential was issued to.
	ClientIDColumn = "client_id"
	// RedirectURIColumn is the one URI an authorization request nominated,
	// re-checked at the token endpoint.
	RedirectURIColumn = "redirect_uri"
	// CodeChallengeColumn is the PKCE challenge the redemption must answer.
	CodeChallengeColumn = "code_challenge"
	// NonceColumn is the OIDC nonce carried through to the id token.
	NonceColumn = "nonce"
	// SubjectIDColumn is who the credential was issued for.
	SubjectIDColumn = "subject_id"
	// SubjectClaimsColumn holds the application-shaped half of the subject, as
	// JSON. This store does not interpret it and must not lose it.
	SubjectClaimsColumn = "subject_claims"
	// AudienceColumn holds the resource servers a token is for, as JSON.
	AudienceColumn = "audience"
	// ResourcesColumn holds the RFC 8707 resource indicators, as JSON.
	ResourcesColumn = "resources"
	// IssuedAtColumn is when the credential was minted.
	IssuedAtColumn = "issued_at"
)

// NowArg is the argument every deadline comparison in this corpus binds.
//
// The comparisons are against a bound time rather than against the server's
// clock, and that is this store's decision rather than querygen's default: the
// deadlines in these columns were stamped by the authorization server's clock,
// so the comparison that decides whether one has passed has to be made against
// that same clock, or "issued for fifteen minutes" and "expired" are measured
// by two clocks that agree only by luck. Sweep binds it as well, and there the
// instant is not even now — a caller sweeping at a horizon an hour back
// reclaims only what nothing is still deciding about. See
// [querygen.AtMostArgument].
const NowArg = "now"

// The query names, which become the generated querier's method names. They are
// spelled here because the drift gate beside this file asserts on this exact
// set: a statement emitted and not executed is sqlc checking SQL nobody runs,
// which is the same green check over an unchecked store as the other way round.
const (
	CreateClientQuery = "CreateClient"
	GetClientQuery    = "GetClient"
	DeleteClientQuery = "DeleteClient"
	SweepClientsQuery = "SweepClients"

	CreateAuthorizationCodeQuery  = "CreateAuthorizationCode"
	GetAuthorizationCodeQuery     = "GetAuthorizationCode"
	ConsumeAuthorizationCodeQuery = "ConsumeAuthorizationCode"
	SweepAuthorizationCodesQuery  = "SweepAuthorizationCodes"

	CreateAccessTokenQuery       = "CreateAccessToken"
	GetAccessTokenQuery          = "GetAccessToken"
	RevokeAccessTokenQuery       = "RevokeAccessToken"
	RevokeAccessTokenFamilyQuery = "RevokeAccessTokenFamily"
	SweepAccessTokensQuery       = "SweepAccessTokens"

	CreateRefreshTokenQuery       = "CreateRefreshToken"
	GetRefreshTokenQuery          = "GetRefreshToken"
	ConsumeRefreshTokenQuery      = "ConsumeRefreshToken"
	RevokeRefreshTokenQuery       = "RevokeRefreshToken"
	RevokeRefreshTokenFamilyQuery = "RevokeRefreshTokenFamily"
	SweepRefreshTokensQuery       = "SweepRefreshTokens"
)

// ClientColumns is the registration table's shape, in the order every read
// projects it.
//
// It carries created_at and none of the rest of the convention triple: no
// last_updated_at, because a registration is written once and never edited, and
// no archived_at, because a registration is deleted rather than stamped — under
// RFC 7591 the client that registered itself is the one that removes itself,
// and a soft delete would leave its identifier taken.
var ClientColumns = []string{
	IDColumn,
	SecretHashColumn,
	NameColumn,
	RedirectURIsColumn,
	GrantTypesColumn,
	ResponseTypesColumn,
	ScopesColumn,
	TokenEndpointAuthMethodColumn,
	CreatedAtColumn,
	ExpiresAtColumn,
}

// CodeColumns is the authorization code table's shape, in projection order.
var CodeColumns = []string{
	HashColumn,
	ClientIDColumn,
	FamilyIDColumn,
	RedirectURIColumn,
	CodeChallengeColumn,
	NonceColumn,
	SubjectIDColumn,
	SubjectClaimsColumn,
	ScopesColumn,
	ResourcesColumn,
	IssuedAtColumn,
	ExpiresAtColumn,
	RedeemedAtColumn,
}

// AccessTokenColumns is the access token table's shape, in projection order.
var AccessTokenColumns = []string{
	HashColumn,
	ClientIDColumn,
	FamilyIDColumn,
	SubjectIDColumn,
	SubjectClaimsColumn,
	ScopesColumn,
	AudienceColumn,
	IssuedAtColumn,
	ExpiresAtColumn,
	RevokedAtColumn,
}

// RefreshTokenColumns is the refresh token table's shape, in projection order.
//
// It is the access token's list plus the resources a refresh carries forward
// and the redeemed_at stamp that makes it single-use, which is the whole of the
// difference between the two credentials.
var RefreshTokenColumns = []string{
	HashColumn,
	ClientIDColumn,
	FamilyIDColumn,
	SubjectIDColumn,
	SubjectClaimsColumn,
	ScopesColumn,
	AudienceColumn,
	ResourcesColumn,
	IssuedAtColumn,
	ExpiresAtColumn,
	RedeemedAtColumn,
	RevokedAtColumn,
}

// The columns each table's writes may set to NULL, which lives in the schema
// neither this package nor querygen reads.
//
// Every one of them is a timestamp, and every one of them is nullable for the
// same reason: NULL is "has not happened", which is not the same fact as a
// stamp at the zero time. It is what lets "does not lapse", "not yet redeemed"
// and "not yet revoked" each be an IS NULL rather than a comparison against a
// magic date — and it is what the guards below are written against.
var (
	clientNullable       = []string{ExpiresAtColumn}
	codeNullable         = []string{RedeemedAtColumn}
	accessTokenNullable  = []string{RevokedAtColumn}
	refreshTokenNullable = []string{RedeemedAtColumn, RevokedAtColumn}
)

// clientInsertColumns is every column of a registration, created_at included.
//
// It is spelled out rather than taken from [querygen.ForInsert], which subtracts
// created_at as database-owned. That is the right subtraction for a conventional
// table, whose schema defaults the column so that a row's creation time cannot
// disagree with its id. This table has no such default and no such id: a
// registration's creation time is the authorization server's, stamped from the
// same clock as the expiry beside it, so the insert names it and the caller
// supplies it.
func clientInsertColumns() []string {
	return slices.Clone(ClientColumns)
}

// Render returns the canonical sqlc input for d: the nineteen statements this
// store executes, in one file's worth of text.
//
// It is what authentication/oauth2serverstore/internal/queriesgen writes to
// the .sql files beside this one, and what CI regenerates to check the
// committed copies still match. Those files are sqlc-gen-unison's input, so
// what the store executes is this text exactly — the generated oauth2serverdb
// package carries it per dialect, with the consumer's table prefix substituted
// once at construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// The four tables this package owns. None of them gets a standard set —
	// three have no id and the fourth has no list — so nothing here registers a
	// table as a side effect of emitting one, and the registration is made by
	// the table existing instead. A consumer reading the registry back to
	// truncate a database between integration tests misses a table otherwise,
	// and the symptom is a different test failing later on rows this one left.
	querygen.RegisterTable(ClientsTable, CodesTable, AccessTokensTable, RefreshTokensTable)

	return querygen.RenderFile(slices.Concat(
		clientQueries(g),
		authorizationCodeQueries(g),
		accessTokenQueries(g),
		refreshTokenQueries(g),
	))
}

// clientQueries renders the registration statements: written once, read by id,
// removed outright, and swept when they lapse.
func clientQueries(g *querygen.Generator) []*querygen.Query {
	return []*querygen.Query{
		// The insert ignores a row already there rather than failing on it,
		// which is what makes ErrClientExists reportable without parsing a
		// driver's error: a duplicate primary key leaves zero rows affected
		// instead of raising a dialect-specific SQLSTATE. Registration is open
		// to anonymous callers, so the row already there has to win — an
		// upsert here would let one of them take over another's client by
		// guessing an identifier.
		g.InsertIgnoreQuery(CreateClientQuery, ClientsTable,
			clientInsertColumns(), clientNullable,
			querygen.Match{Column: IDColumn},
		),
		g.GetQuery(GetClientQuery, ClientsTable, ClientColumns),
		g.DeleteQuery(DeleteClientQuery, ClientsTable, ClientColumns),
		// The IS NOT NULL is what keeps a registration with no expiry — stored
		// as NULL — out of a predicate that would otherwise read it as having
		// lapsed at the beginning of time. It is the excluded form of the same
		// comparand the deadline guards use, so "has an expiry" and "has none"
		// are one Match with one bool between them.
		g.DeleteQuery(SweepClientsQuery, ClientsTable, sweepShape,
			querygen.Match{Column: ExpiresAtColumn, Against: querygen.NoValue, Exclude: true},
			elapsed(),
		),
	}
}

// authorizationCodeQueries renders the code statements. The consume is the one
// that carries the design.
func authorizationCodeQueries(g *querygen.Generator) []*querygen.Query {
	return []*querygen.Query{
		g.InsertIgnoreQuery(CreateAuthorizationCodeQuery, CodesTable,
			CodeColumns, codeNullable,
			querygen.Match{Column: HashColumn},
		),
		g.GetQuery(GetAuthorizationCodeQuery, CodesTable, CodeColumns,
			querygen.Match{Column: HashColumn},
		),
		// The predicate is the whole guarantee, and neither half of it can move
		// into Go. `redeemed_at IS NULL` is what makes two concurrent
		// redemptions of one code resolve to exactly one winner; the deadline
		// guard closes the window in which a code expires between a read and
		// the write that follows it. A store that checked either in Go would
		// have both races, and neither would show up in a test that redeems one
		// code at a time.
		g.UpdateQuery(ConsumeAuthorizationCodeQuery, CodesTable, CodeColumns,
			[]string{RedeemedAtColumn}, nil,
			querygen.Match{Column: HashColumn},
			unredeemed(),
			stillLive(),
		),
		g.DeleteQuery(SweepAuthorizationCodesQuery, CodesTable, sweepShape, elapsed()),
	}
}

// accessTokenQueries renders the access token statements.
//
// There is no consume among them: an access token is presented rather than
// spent, so what the resource server's read has to decide is whether it is
// still live, and the store decides that in Go against the record it read.
func accessTokenQueries(g *querygen.Generator) []*querygen.Query {
	return []*querygen.Query{
		g.InsertIgnoreQuery(CreateAccessTokenQuery, AccessTokensTable,
			AccessTokenColumns, accessTokenNullable,
			querygen.Match{Column: HashColumn},
		),
		g.GetQuery(GetAccessTokenQuery, AccessTokensTable, AccessTokenColumns,
			querygen.Match{Column: HashColumn},
		),
		g.UpdateQuery(RevokeAccessTokenQuery, AccessTokensTable, AccessTokenColumns,
			[]string{RevokedAtColumn}, nil,
			querygen.Match{Column: HashColumn},
			unrevoked(),
		),
		g.UpdateQuery(RevokeAccessTokenFamilyQuery, AccessTokensTable, AccessTokenColumns,
			[]string{RevokedAtColumn}, nil,
			querygen.Match{Column: FamilyIDColumn},
			unrevoked(),
		),
		g.DeleteQuery(SweepAccessTokensQuery, AccessTokensTable, sweepShape, elapsed()),
	}
}

// refreshTokenQueries renders the refresh token statements: the access token's
// set with a consume added, since a refresh is spent rather than presented.
func refreshTokenQueries(g *querygen.Generator) []*querygen.Query {
	return []*querygen.Query{
		g.InsertIgnoreQuery(CreateRefreshTokenQuery, RefreshTokensTable,
			RefreshTokenColumns, refreshTokenNullable,
			querygen.Match{Column: HashColumn},
		),
		g.GetQuery(GetRefreshTokenQuery, RefreshTokensTable, RefreshTokenColumns,
			querygen.Match{Column: HashColumn},
		),
		// The code's consume with a third guard, and the one the code has no
		// use for: a revoked token must not read as a replay. It was never
		// exchanged, so reporting reuse would revoke a family every time
		// somebody signs out and their client retries.
		g.UpdateQuery(ConsumeRefreshTokenQuery, RefreshTokensTable, RefreshTokenColumns,
			[]string{RedeemedAtColumn}, nil,
			querygen.Match{Column: HashColumn},
			unredeemed(),
			unrevoked(),
			stillLive(),
		),
		g.UpdateQuery(RevokeRefreshTokenQuery, RefreshTokensTable, RefreshTokenColumns,
			[]string{RevokedAtColumn}, nil,
			querygen.Match{Column: HashColumn},
			unrevoked(),
		),
		g.UpdateQuery(RevokeRefreshTokenFamilyQuery, RefreshTokensTable, RefreshTokenColumns,
			[]string{RevokedAtColumn}, nil,
			querygen.Match{Column: FamilyIDColumn},
			unrevoked(),
		),
		g.DeleteQuery(SweepRefreshTokensQuery, RefreshTokensTable, sweepShape, elapsed()),
	}
}

// sweepShape is the column list every sweep is rendered from, and it is empty.
//
// querygen derives a statement's id predicate from the list it is handed, and a
// sweep keys on a deadline rather than on a row: handing over the registration
// table's shape would render a DELETE that reached exactly one lapsed
// registration, the one whose id the caller also passed. The three credential
// tables have no id to render one from, so this says for all four what would
// otherwise be true of three by accident.
var sweepShape []string

// unredeemed is the guard that makes a single-use credential single-use: the
// stamp saying it has been spent is not there yet.
//
// It binds nothing, which is what makes it a guard rather than a predicate —
// there is no argument a caller could leave unset to relax it, and a caller has
// no value to bind for "has not happened".
func unredeemed() querygen.Match {
	return querygen.Match{Column: RedeemedAtColumn, Against: querygen.NoValue}
}

// unrevoked is the guard every revocation carries.
//
// It is what makes revoking idempotent in the way the caller needs: a second
// revocation matches nothing and reports zero rows rather than moving the
// timestamp, so the record still says when the token actually stopped working.
func unrevoked() querygen.Match {
	return querygen.Match{Column: RevokedAtColumn, Against: querygen.NoValue}
}

// elapsed is the deadline comparison the four sweeps key on: this row's
// deadline is at or before the instant the caller named.
//
// A revoked token whose deadline has not passed is deliberately not among what
// it matches. A resource server holding one is entitled to be told "no" rather
// than to have its request read as carrying a token nobody ever issued.
func elapsed() querygen.Match {
	return querygen.Match{Column: ExpiresAtColumn, Against: querygen.AtMostArgument, Arg: NowArg}
}

// stillLive is elapsed's complement, and it is derived from it rather than
// written beside it.
//
// The two are one boundary read in two directions — the rows a sweep collects
// and the rows a consumption is allowed to spend — and there is no reading under
// which a row should be in neither set or in both. Spelled separately they could
// come to disagree about the instant a deadline falls on, which is a disagreement
// no test of either statement alone can see.
func stillLive() querygen.Match {
	live := elapsed()
	live.Exclude = true

	return live
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

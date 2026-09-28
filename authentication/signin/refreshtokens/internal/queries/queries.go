package queries

import (
	"strings"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// TokensTable is the refresh token table at its canonical, unprefixed spelling
// — what the emitted .sql names, and what the store's own prefix rendering
// starts from.
//
// The signin_ segment is the schema's own, so a table says which package created
// it even in a database shared between applications, and so that this table and
// oauth2_refresh_tokens — the same mechanism for a different protocol — are told
// apart by their names rather than by a prefix somebody remembered to set.
const TokensTable = "signin_refresh_tokens"

// TableNames is every table this package owns, which is one.
//
// It is a list rather than the constant above because the querygen registry
// takes one, and because a consumer reading that registry back to truncate a
// database between integration tests is asking "what tables does this component
// have rows in" rather than "what does it generate SQL for".
var TableNames = []string{TokensTable}

// The columns the statements below name, and the store binds by.
//
// Exported because both halves spell them: the arguments the generated params
// carry are named from these, and a column spelled twice is a column that can be
// spelled differently.
//
// The four identifier columns answer four different questions, and the DDL is
// where that is written out at length — see the migrations package.
const (
	// HashColumn is the hex digest a token is stored under, and the primary key.
	// It is the whole of what makes a dump of this table unredeemable: the token
	// itself is never written down. It is bound by the insert, compared against
	// by the read and the exchange, and projected by nothing — see
	// [RecordColumns].
	HashColumn = "hash"
	// ScopeColumn is whose directory the login was made in. Every statement here
	// but the sweep filters on it, and it is bound as the tenancy.Scope itself
	// rather than as a string derived from one; see unison.yaml, where that type
	// override lives.
	ScopeColumn = "scope"
	// FamilyIDColumn groups the tokens one login issued, which is what a detected
	// reuse revokes as a unit. A family is one sign-in — deliberately not a
	// session, which is a different package in this module.
	FamilyIDColumn = "family_id"
	// SubjectIDColumn is which person signed in. It carries no REFERENCES — see
	// the migrations package — so it is an identifier this table cannot resolve
	// rather than a foreign key.
	SubjectIDColumn = "subject_id"
	// ActiveAccountIDColumn is which account inside the directory the access
	// token this row mints is for. Without it an exchange would hand back a token
	// for whatever the user's default account has since become rather than for
	// the account proven at sign-in.
	ActiveAccountIDColumn = "active_account_id"
	// AdministrativeColumn is which door minted this login, and the one column
	// here that is not an identifier. An administrative sign-in's tokens are
	// shorter lived than an ordinary one's on purpose, so an exchange that could
	// not read this back would hand an administrative session an ordinary token
	// on an ordinary lifetime — the hardening undone at the first refresh.
	AdministrativeColumn = "administrative"
	// IssuedAtColumn is when the token was minted.
	IssuedAtColumn = "issued_at"
	// SignedInAtColumn is when the login this token belongs to began: the first
	// token's issued_at, carried onto every successor by the mint that writes
	// it. It is a column rather than the earliest issued_at a family still has,
	// because that row is swept at its purge deadline and a long-lived login
	// would then report having begun whenever its oldest surviving row did.
	SignedInAtColumn = "signed_in_at"
	// ExpiresAtColumn is the deadline the token stops being exchangeable at. It
	// is guarded on by the exchange — see [stillLive] — and it is deliberately
	// not what the sweep is keyed on.
	ExpiresAtColumn = "expires_at"
	// PurgeAfterColumn is when the row may be deleted, which is past expires_at
	// by the store's retention window. It is what the sweep is keyed on, and
	// nothing else reads it: a row collected at its own expiry could no longer
	// tell "already used" from "no such token", which is the distinction reuse
	// detection is built on.
	PurgeAfterColumn = "purge_after"
	// RedeemedAtColumn is when the token was exchanged, and NULL until it is. It
	// is the column the exchange assigns and one of the columns the exchange
	// guards on, which is what makes single use a property of the statement
	// rather than of whoever read the row first.
	RedeemedAtColumn = "redeemed_at"
	// RevokedAtColumn is when the token was revoked, and NULL until it is. A
	// family revocation and a single-token revocation both write it.
	RevokedAtColumn = "revoked_at"
	// RedeemedWithKeyColumn is the idempotency key the exchange that spent this
	// row presented, and NULL both before the row is spent and when it is spent
	// without one. It is assigned by [exchangeWithKey] and both assigned and
	// compared against by [claimRemint], which is what makes "one re-mint per
	// key" a property of a statement rather than of whoever read the row first.
	RedeemedWithKeyColumn = "redeemed_with_key"
	// SuccessorHashColumn is the digest of the row this exchange minted, and
	// NULL until it is spent. It is how the retry path names the one row it may
	// revoke — revoking the family instead is the outcome the retry exists to
	// avoid, and revoking nothing leaves one login holding two live tokens.
	SuccessorHashColumn = "successor_hash"
	// AccessTokenIDColumn is the "jti" of the access token minted alongside
	// this row, and NULL on a row minted before the column existed. It is
	// written by the mint and read by [readLive], which is how a per-request
	// check tells a login's current access token from one the login has since
	// replaced.
	AccessTokenIDColumn = "access_token_id"
)

// The arguments the two clock comparisons bind, named for the comparison rather
// than for the column.
//
// Both columns are already argument names in this corpus — the insert binds
// expires_at and purge_after as the values it writes — and a guard or a horizon
// binding under the same name would be one name for two different facts about a
// row.
const (
	// NowArg is the instant the exchange's liveness guard compares against.
	//
	// It is bound rather than read off the server's clock, and that is this
	// store's decision rather than querygen's default: expires_at was stamped by
	// the store's own clock, so the comparison that decides whether it has passed
	// has to be made against that same clock — or "issued for thirty days" and
	// "expired" are measured by two clocks that agree only by luck, and under a
	// test clock that only moves when a test moves it they are years apart.
	NowArg = "now"

	// PurgeBeforeArg is the horizon the sweep binds. A caller sweeping at an
	// instant an hour back reclaims only what nothing is still deciding about.
	PurgeBeforeArg = "purge_before"

	// ExpectedKeyArg is the key [claimRemint] requires the row to still be
	// carrying, as against the value it assigns.
	//
	// Both ends of that comparison are redeemed_with_key, so under one argument
	// name the statement would set the column to the value it was requiring it
	// to already hold — legal SQL that guards nothing. Naming the predicate's
	// end separately is what makes the claim a claim.
	ExpectedKeyArg = "expected_key"

	// KeepFamilyIDArg is the one login [lockOtherFamiliesForSubject] leaves
	// standing, as against the family_id the other locking reads select.
	//
	// It is an argument of its own because the predicate it binds is the
	// inverse of theirs: family_id <> keep, not family_id = the family. Under
	// the column's name the generated params would carry a FamilyID that meant
	// "end this one" on two statements and "spare this one" on a third.
	KeepFamilyIDArg = "keep_family_id"
)

// Columns is the whole row, in the order the DDL declares it.
//
// It is what the three guarded writes and the sweep are rendered from. querygen
// derives a statement's id predicate from the list it is handed and this table
// has no id — the key is the digest of a credential — so every predicate below
// is one the statement names explicitly.
var Columns = []string{
	HashColumn,
	ScopeColumn,
	FamilyIDColumn,
	SubjectIDColumn,
	ActiveAccountIDColumn,
	AdministrativeColumn,
	IssuedAtColumn,
	SignedInAtColumn,
	ExpiresAtColumn,
	PurgeAfterColumn,
	RedeemedAtColumn,
	RevokedAtColumn,
	RedeemedWithKeyColumn,
	SuccessorHashColumn,
	AccessTokenIDColumn,
}

// RecordColumns is what the read projects, in the order the generated row type
// carries them: the whole table less the two digest columns and the key.
//
// The hash's absence is the point, and it is why this is a projection rather
// than a SELECT * with a Go-side drop. Nothing in this package reads the column
// back — it is bound by the insert and compared against by the read and the
// exchange — and a projection that included it would put a stored credential's
// digest in whatever a caller did next with the row.
//
// successor_hash is left out for exactly that reason and is read by one
// statement of its own — see [readRedemption], which is the only read in this
// corpus that projects a digest and the only caller that has somewhere to put
// one. redeemed_with_key rides along with it because the two are read together
// or not at all: neither alone answers whether a presentation is a retry.
var RecordColumns = []string{
	ScopeColumn,
	FamilyIDColumn,
	SubjectIDColumn,
	ActiveAccountIDColumn,
	AdministrativeColumn,
	IssuedAtColumn,
	SignedInAtColumn,
	ExpiresAtColumn,
	PurgeAfterColumn,
	RedeemedAtColumn,
	RevokedAtColumn,
	AccessTokenIDColumn,
}

// InsertColumns is what a mint writes: every column but the two stamps, which
// are NULL by definition on a token nobody has spent or revoked yet.
//
// Their absence from the list is what says so; a bound NULL would only restate
// it. created_at is not among them because this table has none — issued_at is
// the row's creation time under the name the mechanism actually uses, and a
// second column carrying the same instant is a second column free to disagree.
var InsertColumns = []string{
	HashColumn,
	ScopeColumn,
	FamilyIDColumn,
	SubjectIDColumn,
	ActiveAccountIDColumn,
	AdministrativeColumn,
	IssuedAtColumn,
	SignedInAtColumn,
	ExpiresAtColumn,
	PurgeAfterColumn,
	AccessTokenIDColumn,
}

// FamilyColumns is what the listing of a person's live logins projects: one
// row per login, which is its current token, less everything about that token
// a screen showing the login has no use for.
//
// No stamp is among them because every row the listing returns has neither,
// and no deadline past expires_at because purge_after is storage's business
// rather than the login's. The subject and the scope are the statement's own
// predicates, so projecting them back would be reading out what the caller
// just bound.
var FamilyColumns = []string{
	FamilyIDColumn,
	ActiveAccountIDColumn,
	AdministrativeColumn,
	IssuedAtColumn,
	SignedInAtColumn,
	ExpiresAtColumn,
}

// LockColumns is what the locking reads project: whose login each
// locked row is and which one, and nothing a revocation keyed on the family
// has any use for.
//
// The subject is projected even by the reads that bind it, so the three answer
// in one row shape; the family-only read is the one that needs it, because an
// operator ending a login by its id is not told whose it is.
var LockColumns = []string{
	SubjectIDColumn,
	FamilyIDColumn,
}

// RedeemColumns is what the exchange assigns, which is the stamp and nothing
// else.
var RedeemColumns = []string{RedeemedAtColumn}

// RedeemWithKeyColumns is what the idempotent exchange assigns: the same stamp,
// and the key that spent the row.
//
// The two are one assignment rather than two statements because they are one
// fact. A row stamped spent by a write that had not yet recorded which key spent
// it is a row whose own client's retry is indistinguishable from a replay — the
// precise failure the key exists to remove, reintroduced in a narrower window.
var RedeemWithKeyColumns = []string{RedeemedAtColumn, RedeemedWithKeyColumn}

// KeyColumns is what the re-mint claim assigns, which is the key column and
// nothing else — set to NULL, since honoring a retry spends the evidence.
var KeyColumns = []string{RedeemedWithKeyColumn}

// SuccessorColumns is what the successor record assigns.
var SuccessorColumns = []string{SuccessorHashColumn}

// RevokeColumns is what both revocations assign.
//
// The single-token revocation and the family one share the list rather than each
// naming its own, because they can race for one row and the row has to end up in
// one shape whichever wins.
var RevokeColumns = []string{RevokedAtColumn}

// The query names the generated querier's methods are built from. They are
// spelled here because the store names them too — through the generated params
// types — and because the drift gate beside this file asserts on this exact set.
const (
	InsertTokenQuery            = "InsertRefreshToken"
	GetTokenQuery               = "GetRefreshToken"
	GetRedemptionQuery          = "GetRefreshTokenRedemption"
	GetLiveTokenQuery           = "GetLiveRefreshTokenForFamily"
	RedeemTokenQuery            = "RedeemRefreshToken"
	RedeemTokenWithKeyQuery     = "RedeemRefreshTokenWithKey"
	ClaimRemintQuery            = "ClaimRefreshTokenRemint"
	RecordSuccessorQuery        = "RecordRefreshTokenSuccessor"
	RevokeTokenQuery            = "RevokeRefreshToken"
	RevokeFamilyQuery           = "RevokeRefreshTokenFamily"
	LockFamilyQuery             = "LockLiveRefreshTokenFamily"
	LockSubjectFamilyQuery      = "LockLiveRefreshTokenFamilyForSubject"
	LockFamiliesForSubjectQuery = "LockLiveRefreshTokenFamiliesForSubject"
	LockOtherFamiliesQuery      = "LockOtherLiveRefreshTokenFamiliesForSubject"
	ListLiveFamiliesQuery       = "ListLiveRefreshTokenFamilies"
	SweepTokensQuery            = "SweepRefreshTokens"
)

// exclusiveLock is the clause the locking reads carry, and it is the same
// text on both dialects that have row locking at all.
//
// It is a constant rather than a literal in the statements for the reason the
// column names above are: this package's tests read the clause instead of
// spelling it a second time.
const exclusiveLock = "FOR UPDATE"

// Render returns the canonical sqlc input for d: the statements this store
// executes, in one file's worth of text.
//
// It is what authentication/signin/refreshtokens/internal/queriesgen writes to
// the .sql files beside this one, and what CI regenerates to check the committed
// copies still match. Those files are sqlc-gen-unison's input, so what the store
// executes is this text exactly — the generated signindb package carries it per
// dialect, with the consumer's table prefix substituted once at construction.
//
// The order is the order a token goes through: minted, read — twice, since the
// idempotent path reads what the ordinary one does not — exchanged, with or
// without a key, and then the two writes a retry of that exchange makes; or
// revoked, alone or as one of a family's; locked, while it is the live one of
// its login, by the family or by its owner, so that a revocation can say what it
// ended; listed, while it is still that, or read as that one by its family; and
// finally collected once its purge deadline has passed.
//
// # The five statements the idempotent path adds
//
// [exchangeWithKey] is [exchange] with one more column in its SET. The rest are
// the retry, and each is a statement rather than a Go decision for the reason
// [exchange] is: the count is the answer. [readRedemption] is the only read here
// that projects a digest; [claimRemint] is the write that admits exactly one
// retry per key, by requiring the key it clears; [recordSuccessor] is what makes
// "the successor" a row anything can name; and [revoke] is the single-row
// revocation the retry does instead of ending a family.
//
// # Where liveness is decided, and why it is here
//
// This corpus parts company with its two nearest siblings on one point.
// authentication/passwordreset and links/database both decide liveness in Go,
// against the record as it was read, and keep every deadline comparison out of
// their guarded writes. This one puts it in the predicate, and follows
// authentication/oauth2serverstore instead.
//
// The reason is what the affected-row count has to mean. There, zero rows is
// already unambiguous — one guard, one meaning — so the deadline could be moved
// out without losing anything. Here the exchange has to tell a *spent* token
// from every other refusal, because a spent one revokes a family and the others
// do not, and it tells them apart by reading the row back on the same
// transaction afterwards. Once that read-back exists, folding the deadline into
// the guard costs nothing and buys the thing a Go-side check cannot: a token that
// expires between the read and the write cannot be exchanged by the write. See
// [exchange].
//
// # A note on timestamps, because one dialect does something surprising
//
// Every instant this corpus binds is a UTC time.Time and stays one all the way
// down: the store reads its clock as UTC, and the generated SQLite arm converts
// again before it binds. Postgres and MySQL store these as real temporal types.
// SQLite has no date type at all, so a DATETIME column holds text, and both the
// exchange's `expires_at > sqlc.arg(now)` and the sweep's
// `purge_after <= sqlc.arg(purge_before)` are string comparisons there.
//
// Those comparisons are chronological rather than merely lexical because both
// sides are rendered "YYYY-MM-DD HH:MM:SS" in UTC — a fixed-width prefix, one
// zone. A value bound in any other zone would put that zone's wall clock in those
// leading characters and every comparison would be off by the offset, silently,
// and only for the deployments whose clock is not UTC.
//
// The rendering is whole seconds there, so an instant carrying a fraction is
// stored truncated down, and both directions are the ones that fail closed. A
// token on that engine goes dead up to a second early rather than living a second
// past its deadline, and a row is collected up to a second late rather than while
// something might still be deciding about it.
//
// # Why there is no standard set
//
// [querygen.Generator.StandardCRUD] serves a table with a surrogate id, a paged
// list keyed on it, and the convention triple of timestamps. This table has none
// of that, and every absence is deliberate — see the migrations package. Its key
// is the digest of a credential rather than a surrogate; the one read that lists
// rows lists a person's live logins, a bounded set with no position a caller
// holds between round trips — see [listLiveFamilies];
// and an archived_at would keep rows nothing can read while making the sweep the
// one write unable to reach the rows it exists for.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// The one table this package owns. StandardCRUD would have registered it, and
	// StandardCRUD cannot serve this table at all — so the registration is made by
	// the table existing rather than by something choosing to emit its standard
	// set, which is the distinction the registry is built around.
	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		insert(g),
		read(g),
		readRedemption(g),
		exchange(g),
		exchangeWithKey(g),
		claimRemint(g),
		recordSuccessor(g),
		revoke(g),
		revokeFamily(g),
		lockFamily(g),
		lockSubjectFamily(g),
		lockFamiliesForSubject(g),
		lockOtherFamiliesForSubject(g),
		listLiveFamilies(g),
		readLive(g),
		sweep(g),
	})
}

// insert is the mint write, and it is one statement for both mints: the one a
// sign-in makes and the successor an exchange issues into the same family.
//
// It is a plain INSERT rather than an upsert or an insert-ignore, and the
// difference is what the key means. The hash is the digest of the token, so a
// second row bearing one would mean the generator produced the same token twice.
// The primary key refuses that and this statement lets it: a mint failing loudly
// is the correct outcome of randomness that has stopped being random, where an
// ignore would hand the caller a token that redeems somebody else's row.
func insert(g *querygen.Generator) *querygen.Query {
	return g.InsertQuery(InsertTokenQuery, TokensTable, InsertColumns, nil)
}

// read is the lookup the exchange falls back to when its guarded write matched
// nothing.
//
// It keys on the hash and the scope and on neither the deadline nor the two
// stamps, because what it exists to answer is which of those refused the write —
// a read that hid a spent row would hide the one case that has to be told apart
// from the rest. The scope is a predicate rather than a check made on the row
// afterwards, so a token presented in the wrong directory matches nothing and
// reads as absent, which is what it is from there.
//
// It is a [querygen.Generator.ReadQuery] rather than a get because both lists are
// narrower than the table: the key goes over as the two columns it names, and the
// projection goes back without the hash.
func read(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery(GetTokenQuery, TokensTable, Columns,
		querygen.Read{Projection: RecordColumns},
		querygen.Match{Column: HashColumn},
		querygen.Match{Column: ScopeColumn},
	)
}

// exchange is the write that spends a refresh token, and it is the statement
// that decides single use.
//
// Five predicates, and every one of them repeats a row-state test the answer
// depends on. Two requests presenting one token at the same instant both reach
// this statement; the first one's update matches, the second one's finds
// redeemed_at already set and reports no rows. The count is the answer, which is
// why a read cannot be, and why the statement is annotated :execrows.
//
// The three guards bind nothing, which is what makes them guards rather than
// predicates: there is no argument a caller could leave unset to relax them, and
// a caller has no value to bind for "has not happened". The deadline is the one
// comparison that binds, and it binds the store's own clock — see [NowArg].
//
// Nothing here is checked in Go first and then again in the predicate. A write
// that repeated only some of the row-state tests its own decision rests on is
// the defect outbox's lease mode paid for: every test the answer depends on is
// made by the write, at the instant the row changes, rather than by a caller a
// round trip earlier.
func exchange(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(RedeemTokenQuery, TokensTable, Columns, RedeemColumns, nil,
		querygen.Match{Column: HashColumn},
		querygen.Match{Column: ScopeColumn},
		unredeemed(),
		unrevoked(),
		stillLive(),
	)
}

// readRedemption is the one read here that projects a digest, and it exists so
// that RecordColumns does not have to.
//
// What it answers is "was this presentation a retry of the exchange that spent
// this row, and if so what did that exchange mint" — two columns nothing outside
// the retry path has any use for, and one of which names a live credential. A
// projection carrying them everywhere would put a successor's digest into
// whatever a caller did next with an ordinary redemption's row; a read of their
// own confines them to the one function that has somewhere to put them.
//
// It is keyed exactly as [read] is, and it is a second statement rather than a
// widened first one because the retry path is the rare one: an exchange that
// succeeds never issues it.
func readRedemption(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery(GetRedemptionQuery, TokensTable, Columns,
		querygen.Read{Projection: []string{RedeemedWithKeyColumn, SuccessorHashColumn}},
		querygen.Match{Column: HashColumn},
		querygen.Match{Column: ScopeColumn},
	)
}

// exchangeWithKey is [exchange] carrying the key that spent the row.
//
// Every predicate is the same one, deliberately: the idempotency key changes
// what a *later* presentation of this token means and changes nothing about
// which caller may spend it now. Relaxing a guard here would make the key a way
// to exchange a token the ordinary statement refuses.
//
// The assignment is the part that matters, and it is one statement rather than a
// spend followed by a record. primitives-go's idempotency.Manager records its
// result outside the work's transaction and says what that cannot promise —
// "work that has its effect and then fails" — and for a payment that is an
// accepted risk. Here it is the exact failure the key exists to remove: a token
// spent by a write that had not yet recorded which key spent it is a token whose
// own client's retry reads as a replay, so the family would be revoked by the
// mechanism added to stop revoking it. Either both land or neither does.
func exchangeWithKey(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(RedeemTokenWithKeyQuery, TokensTable, Columns, RedeemWithKeyColumns, nil,
		querygen.Match{Column: HashColumn},
		querygen.Match{Column: ScopeColumn},
		unredeemed(),
		unrevoked(),
		stillLive(),
	)
}

// claimRemint is the write that admits exactly one re-mint per key, and it is
// the whole of that bound.
//
// It clears redeemed_with_key while requiring the row to still be carrying it,
// so two presentations of one captured request resolve to one winner and the
// loser falls through to the reuse branch — the same shape [exchange] uses to
// decide single use, one column over. Both ends of the comparison are the key
// column, which is why the predicate binds [ExpectedKeyArg] rather than the
// column's own name: under one argument the statement would set the column to
// the value it was requiring it to already hold, which is legal SQL that guards
// nothing.
//
// The bound is not a refinement. Without it a single captured exchange request —
// token and key together — mints a fresh live token for every presentation until
// the grace window closes, which is a renewable session granted out of evidence
// that is worth nothing at all today. With it the attacker's take is one token
// whose use revokes the legitimate client's, and is therefore visible within one
// request.
//
// The assignment is a narg because what it assigns is NULL. There is no "unset"
// spelling for a column a statement clears, and a second statement spelling
// `= NULL` inline would be a second rendering of one write.
func claimRemint(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(ClaimRemintQuery, TokensTable, Columns, KeyColumns, KeyColumns,
		querygen.Match{Column: HashColumn},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: RedeemedWithKeyColumn, Arg: ExpectedKeyArg},
	)
}

// recordSuccessor writes onto a spent row the digest of the row its exchange
// minted.
//
// It is what makes "the successor" nameable, and it runs in the same transaction
// as the spend and the mint, so there is no committed state in which a row is
// spent and its successor is unknown. A retry reaching such a row would have
// nothing to revoke and would have to end the family, which is the answer this
// whole path exists to avoid.
//
// It carries no guard of its own beyond the two key columns. The row it writes
// to was spent by this same transaction, so "is it still spendable" is a
// question already answered; a revoked_at IS NULL added here would refuse the
// write on a family revoked between the two statements and leave the row saying
// it minted nothing.
//
// The value is bound rather than narg'd — a successor this statement could not
// name is a statement with no reason to run — which is the one place it parts
// company with [claimRemint] beside it, whose whole assignment is a NULL.
func recordSuccessor(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(RecordSuccessorQuery, TokensTable, Columns, SuccessorColumns, nil,
		querygen.Match{Column: HashColumn},
		querygen.Match{Column: ScopeColumn},
	)
}

// revoke ends one token, which is what honoring a retry does to the successor
// the first attempt already minted.
//
// It is [revokeFamily] keyed on the row instead of on the login, and the whole
// difference between a retry and a detected theft is which of the two runs. A
// family revocation here would sign the client out for having lost a response,
// and no revocation at all would leave one login holding two live refresh tokens
// — which is the property rotation exists to hold, given up by the feature meant
// to make rotation survivable.
//
// The guard is the revocation's own, so a row already revoked reports zero
// rather than moving the stamp.
func revoke(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(RevokeTokenQuery, TokensTable, Columns, RevokeColumns, nil,
		querygen.Match{Column: HashColumn},
		querygen.Match{Column: ScopeColumn},
		unrevoked(),
	)
}

// revokeFamily ends one login, which is what a detected token reuse does.
//
// It keys on the family and the scope and nothing else, so a caller holding the
// family id off a spent row ends every token that login ever issued — including
// the successor a thief is holding and the successor the victim is holding, which
// is the point: the two are indistinguishable from here, and ending both is the
// only answer that does not leave the thief signed in.
//
// The guard is the revocation's own, so a second revocation matches nothing and
// reports zero rows rather than moving the timestamp — the record still says when
// the family actually stopped working.
//
// Expired rows are matched and moved, because this corpus carries no liveness
// predicate outside the exchange. A row that lapsed on its own and one somebody
// withdrew both end up revoked, and after a detected reuse "somebody withdrew
// this" is the more useful of the two true sentences.
func revokeFamily(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(RevokeFamilyQuery, TokensTable, Columns, RevokeColumns, nil,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: FamilyIDColumn},
		unrevoked(),
	)
}

// lockFamily is the live row of one login, locked, named by its family alone.
//
// It is the first half of every revocation that has to say what it ended — an
// operator's by family id, and the one a detected reuse performs — and the
// second half is [revokeFamily], keyed on the family it read. See
// [lockFamiliesForSubject] for why the lock comes first.
func lockFamily(g *querygen.Generator) *querygen.Query {
	return lockLive(g, LockFamilyQuery,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: FamilyIDColumn},
	)
}

// lockSubjectFamily is [lockFamily] with the subject in the key, which is what
// a self-service door ending one login by its id runs.
//
// The subject predicate is the whole of that door's authorization. A family
// identifier is not a secret — it is on every issued token, and identifiers.New's
// values carry a timestamp and a counter — so the read a signed-in caller's
// request reaches must be unable to lock anybody else's login. Keyed on the
// caller's subject as well, a guessed or borrowed family id locks nothing, and
// the revocation keyed on what it locked ends nothing, exactly as for a family
// already ended.
func lockSubjectFamily(g *querygen.Generator) *querygen.Query {
	return lockLive(g, LockSubjectFamilyQuery,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: SubjectIDColumn},
		querygen.Match{Column: FamilyIDColumn},
	)
}

// lockFamiliesForSubject is the live row of every login one person holds,
// locked: "sign out everywhere", "disable this account", and the erasure a
// dataprivacy run performs.
//
// # Why a locked read and then a revocation by id
//
// A revocation keyed on the subject cannot say which logins it ended, and MySQL
// has no RETURNING to make it — so the read comes first, on every engine, and
// the revocation is then keyed on each family the read returned. Unlocked, that
// is two races. A login committed between the read and the revocation would be
// ended without being reported, which a revocation keyed on the subject would
// do; revoking by the ids read instead leaves it live and unreported, which is
// consistent. And an exchange racing the revocation could spend the row the
// read saw and mint a successor the revocation's snapshot never sees, carrying
// the login past a sign-out that reported it ended. The lock closes the second:
// the exchange's guarded write waits on the row, and finds it revoked.
//
// SQLite has no FOR UPDATE and needs none — one writer at a time is that
// engine's whole storage model, so the interleaving the lock excludes is
// unreachable there. It renders the same statement without the clause, which is
// what keeps the roster of statement names from varying by dialect.
//
// It is bounded, as every sweep is, and the store runs it until a pass comes
// back short: a revoked family is no longer live, so the next pass locks the
// ones the last pass did not reach. The order is the family, which is total
// over live rows because a login has exactly one, and which makes two
// concurrent revocations of one person lock in the same order.
func lockFamiliesForSubject(g *querygen.Generator) *querygen.Query {
	return lockLive(g, LockFamiliesForSubjectQuery,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: SubjectIDColumn},
	)
}

// lockOtherFamiliesForSubject is [lockFamiliesForSubject] with one family
// spared: "sign out my other devices".
//
// It is a locking read of its own rather than the subject-wide one filtered in
// Go, so the family it spares is never locked at all: the login the request
// came through goes on exchanging while its siblings end. The spared family is
// excluded by its id and nothing else, so one that is not this subject's spares
// nothing and every login the subject holds is selected — the direction a
// sign-out should fail in. The door refuses an empty keep before it gets here,
// so "no family" can never render as "every family".
func lockOtherFamiliesForSubject(g *querygen.Generator) *querygen.Query {
	return lockLive(g, LockOtherFamiliesQuery,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: SubjectIDColumn},
		querygen.Match{Column: FamilyIDColumn, Arg: KeepFamilyIDArg, Exclude: true},
	)
}

// lockLive renders one of the locking reads: the live rows key matches,
// under the three guards the exchange carries, projected to [LockColumns] and
// locked where the dialect locks.
//
// The guards are the listing's, and so the exchange's, rather than a second
// spelling of them: what these lock is exactly what an exchange would still
// accept, which is what "this revocation ended a login" has to mean.
func lockLive(g *querygen.Generator, name string, key ...querygen.Match) *querygen.Query {
	read := g.SweepQuery(name, TokensTable, Columns,
		querygen.Sweep{
			Order:      []querygen.Order{{Column: FamilyIDColumn}},
			Projection: LockColumns,
		},
		append(key, unredeemed(), unrevoked(), stillLive())...,
	)

	if g.Dialect() != dialect.SQLite {
		lock(read, exclusiveLock)
	}

	return read
}

// lock puts a locking clause on a rendered read, ahead of its terminator.
//
// A sweep's content carries no terminator of its own — Query.Render supplies
// one — but a clause after a semicolon would not be a clause on that statement
// at all, so one that is there is moved rather than written around.
func lock(q *querygen.Query, clause string) {
	q.Content = strings.TrimSuffix(q.Content, ";") + "\n" + clause + ";"
}

// listLiveFamilies is a person's live logins: one row per family, which is the
// family's current token.
//
// A family has exactly one row that is unspent, unrevoked and unexpired — an
// exchange spends one row and mints its successor in one transaction, and the
// retry path revokes the successor it supersedes before minting another — so the
// three guards the exchange carries are, read here, "this login is still going",
// and they pick that one row without a GROUP BY. They are the same Match values
// the exchange is rendered from rather than a second spelling of them, so the
// rows this lists are exactly the rows an exchange would still accept.
//
// It is a bounded scan rather than a paged list, deliberately. The paged list
// keys its cursor on a surrogate id this table does not have, and a person's
// live logins are a set a screen shows whole rather than one a caller walks;
// the limit bounds what a subject with an unreasonable number of them costs,
// and the order puts the most recently refreshed first so that what a limit
// leaves out is what has been idle longest. family_id breaks ties, so the
// order is total.
func listLiveFamilies(g *querygen.Generator) *querygen.Query {
	return g.SweepQuery(ListLiveFamiliesQuery, TokensTable, Columns,
		querygen.Sweep{
			Order:      []querygen.Order{{Column: IssuedAtColumn, Descending: true}, {Column: FamilyIDColumn}},
			Projection: FamilyColumns,
		},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: SubjectIDColumn},
		unredeemed(),
		unrevoked(),
		stillLive(),
	)
}

// readLive is one login's current token, read by its family: the row
// [listLiveFamilies] would list for it, found by the family rather than by the
// person.
//
// It is what a per-request check reads, so it is keyed on what an access token
// carries — the scope and the family, which version 1's family index serves —
// and it carries the exchange's three guards for [listLiveFamilies]' reason: a
// family has exactly one row they admit while the login is going and none once
// it has ended, so "no row" is the answer "this login is over" and a row is the
// answer "it is not, and this is the access token it minted last".
//
// No Order, because there is no second row to choose between. A key that could
// admit two would need one; this one admits one by the same invariant the
// listing already rests on.
func readLive(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery(GetLiveTokenQuery, TokensTable, Columns,
		querygen.Read{Projection: RecordColumns},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: FamilyIDColumn},
		unredeemed(),
		unrevoked(),
		stillLive(),
	)
}

// sweep is the removal of every row past its purge deadline.
//
// Redeemed and revoked rows go with them, at that deadline rather than at their
// redemption, and that is the whole retention policy. It is also what keeps reuse
// detection working: the exchange tells a spent token from an unknown one by
// reading the row back, so a row collected at its own expiry would turn a replay
// into "no such token" — the one answer that lets a thief's second presentation
// pass unnoticed. The deadline is later than expires_at for exactly that reason.
//
// It spans every scope, which is the one statement here that does, and it carries
// no cap. Token rows are small and the index on purge_after makes the delete
// proportional to what is actually dead rather than to the table, so this is
// [querygen.Generator.DeleteQuery] with a horizon rather than
// [querygen.Generator.PruneQuery]: there is no backlog for a bound to protect
// against, and a bounded pass would make Sweep's count a loop condition rather
// than an answer.
func sweep(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(SweepTokensQuery, TokensTable, Columns,
		querygen.Match{Column: PurgeAfterColumn, Against: querygen.AtMostArgument, Arg: PurgeBeforeArg},
	)
}

// unredeemed is the guard that makes a single-use credential single-use: the
// stamp saying it has been spent is not there yet.
func unredeemed() querygen.Match {
	return querygen.Match{Column: RedeemedAtColumn, Against: querygen.NoValue}
}

// unrevoked is the guard the exchange and both revocations carry.
//
// On the exchange it is what keeps a revoked token from reading as a replay: it
// was never spent, so reporting reuse would revoke a family every time somebody
// signs out and their client retries. On the revocations it is what makes
// revoking idempotent in the way a caller needs.
func unrevoked() querygen.Match {
	return querygen.Match{Column: RevokedAtColumn, Against: querygen.NoValue}
}

// stillLive is the exchange's deadline guard: this row's deadline has not been
// reached at the instant the caller named.
//
// It is derived from [elapsed] rather than written beside it. The two are one
// boundary read in two directions — the rows an exchange may spend, and the rows
// that have gone past it — and spelled separately they could come to disagree
// about the instant a deadline falls on, which is a disagreement no test of
// either alone can see.
func stillLive() querygen.Match {
	live := elapsed()
	live.Exclude = true

	return live
}

// elapsed is the uninverted reading of the same boundary: this row's deadline is
// at or before the instant the caller named.
//
// No statement here matches on it directly — the sweep is keyed on purge_after
// rather than on the expiry — and it exists so that [stillLive] is one operator
// away from the comparison it inverts rather than a second spelling of it.
func elapsed() querygen.Match {
	return querygen.Match{Column: ExpiresAtColumn, Against: querygen.AtMostArgument, Arg: NowArg}
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

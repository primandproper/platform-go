package queries

import (
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// LinksTable is the sign-in link table at its canonical, unprefixed spelling —
// what the emitted .sql names, and what the store's own prefix rendering starts
// from.
//
// The signin_ segment is the schema's own, so a table says which package created
// it even in a database shared between applications, and so that this table and
// the links schema — a general-purpose link minter with its own store, serving
// every action an application mails a URL for — are told apart by their names
// rather than by a prefix somebody remembered to set.
const LinksTable = "signin_magic_links"

// TableNames is every table this package owns, which is one.
//
// It is a list rather than the constant above because the querygen registry
// takes one, and because a consumer reading that registry back to truncate a
// database between integration tests is asking "what tables does this component
// have rows in" rather than "what does it generate SQL for".
var TableNames = []string{LinksTable}

// The columns the statements below name, and the store binds by.
//
// Exported because both halves spell them: the arguments the generated params
// carry are named from these, and a column spelled twice is a column that can be
// spelled differently.
//
// There are two identifier columns where the refresh token corpus beside this
// one has four, and the DDL is where each absence is argued — see the migrations
// package. The address beside them is not a third: it names no row and resolves
// nothing, it is what the link was mailed to, and it is here so that a
// redemption can be refused when the subject no longer holds it.
const (
	// HashColumn is the hex digest a token is stored under, and the primary key.
	// It is the whole of what makes a dump of this table unredeemable: the token
	// itself is never written down. It is bound by the insert, compared against
	// by the read and the redemption, and projected by nothing — see
	// [RecordColumns].
	HashColumn = "hash"
	// ScopeColumn is whose directory the link was minted in. Every statement here
	// but the sweep filters on it, and it is bound as the tenancy.Scope itself
	// rather than as a string derived from one; see unison.yaml, where that type
	// override lives.
	ScopeColumn = "scope"
	// SubjectIDColumn is which person the link signs in. It carries no REFERENCES
	// — see the migrations package — so it is an identifier this table cannot
	// resolve rather than a foreign key.
	SubjectIDColumn = "subject_id"
	// EmailAddressColumn is the address the link was mailed to, folded the way
	// the directory folds a handle.
	//
	// It is bound by the insert and projected by the read, and no statement here
	// compares against it. The comparison that matters is made a layer up,
	// against the address the subject holds at the moment of redemption — see
	// signin.Service.RedeemMagicLink, where what a disagreement means is argued.
	// A predicate here could only ask the question the store has no second side
	// to: this package reads no user table.
	//
	// It is stored as it is rather than digested, and the distinction is one this
	// table already makes about hash. A digest is worth something against
	// thirty-two bytes from a CSPRNG and nothing against an address, which comes
	// from a set somebody can enumerate; digesting it would buy the appearance of
	// protection and the loss of a column an operator can read.
	EmailAddressColumn = "email_address"
	// IssuedAtColumn is when the link was minted.
	IssuedAtColumn = "issued_at"
	// ExpiresAtColumn is the deadline the link stops being redeemable at. It is
	// guarded on by the redemption — see [stillLive] — and it is deliberately not
	// what the sweep is keyed on.
	ExpiresAtColumn = "expires_at"
	// PurgeAfterColumn is when the row may be deleted, which is past expires_at
	// by the store's retention window. It is what the sweep is keyed on, and
	// nothing else reads it.
	PurgeAfterColumn = "purge_after"
	// RedeemedAtColumn is when the link was followed, and NULL until it is. It is
	// the column the redemption assigns and one of the columns the redemption
	// guards on, which is what makes single use a property of the statement
	// rather than of whoever read the row first.
	RedeemedAtColumn = "redeemed_at"
	// RevokedAtColumn is when the link was withdrawn, and NULL until it is. The
	// subject-wide revocation is the only writer.
	RevokedAtColumn = "revoked_at"
)

// The arguments the two clock comparisons bind, named for the comparison rather
// than for the column.
//
// Both columns are already argument names in this corpus — the insert binds
// expires_at and purge_after as the values it writes — and a guard or a horizon
// binding under the same name would be one name for two different facts about a
// row.
const (
	// NowArg is the instant the redemption's liveness guard compares against.
	//
	// It is bound rather than read off the server's clock, and that is this
	// store's decision rather than querygen's default: expires_at was stamped by
	// the store's own clock, so the comparison that decides whether it has passed
	// has to be made against that same clock — or "good for fifteen minutes" and
	// "expired" are measured by two clocks that agree only by luck, and under a
	// test clock that only moves when a test moves it they are years apart.
	NowArg = "now"

	// PurgeBeforeArg is the horizon the sweep binds. A caller sweeping at an
	// instant an hour back reclaims only what nothing is still deciding about.
	PurgeBeforeArg = "purge_before"
)

// Columns is the whole row, in the order the DDL declares it.
//
// It is what the two guarded writes and the sweep are rendered from. querygen
// derives a statement's id predicate from the list it is handed and this table
// has no id — the key is the digest of a credential — so every predicate below
// is one the statement names explicitly.
var Columns = []string{
	HashColumn,
	ScopeColumn,
	SubjectIDColumn,
	EmailAddressColumn,
	IssuedAtColumn,
	ExpiresAtColumn,
	PurgeAfterColumn,
	RedeemedAtColumn,
	RevokedAtColumn,
}

// RecordColumns is what the read projects, in the order the generated row type
// carries them: the whole table less the hash.
//
// The hash's absence is the point, and it is why this is a projection rather
// than a SELECT * with a Go-side drop. Nothing in this package reads the column
// back — it is bound by the insert and compared against by the read and the
// redemption — and a projection that included it would put a stored credential's
// digest in whatever a caller did next with the row.
var RecordColumns = []string{
	ScopeColumn,
	SubjectIDColumn,
	EmailAddressColumn,
	IssuedAtColumn,
	ExpiresAtColumn,
	PurgeAfterColumn,
	RedeemedAtColumn,
	RevokedAtColumn,
}

// InsertColumns is what a mint writes: every column but the two stamps, which
// are NULL by definition on a link nobody has followed or withdrawn yet.
//
// Their absence from the list is what says so; a bound NULL would only restate
// it. created_at is not among them because this table has none — issued_at is
// the row's creation time under the name the mechanism actually uses, and a
// second column carrying the same instant is a second column free to disagree.
var InsertColumns = []string{
	HashColumn,
	ScopeColumn,
	SubjectIDColumn,
	EmailAddressColumn,
	IssuedAtColumn,
	ExpiresAtColumn,
	PurgeAfterColumn,
}

// RedeemColumns is what the redemption assigns, which is the stamp and nothing
// else.
var RedeemColumns = []string{RedeemedAtColumn}

// RevokeColumns is what the revocation assigns.
var RevokeColumns = []string{RevokedAtColumn}

// The query names the generated querier's methods are built from. They are
// spelled here because the store names them too — through the generated params
// types — and because the drift gate beside this file asserts on this exact set.
const (
	InsertLinkQuery            = "InsertMagicLink"
	GetLinkQuery               = "GetMagicLink"
	RedeemLinkQuery            = "RedeemMagicLink"
	RevokeLinksForSubjectQuery = "RevokeMagicLinksForSubject"
	SweepLinksQuery            = "SweepMagicLinks"
)

// Render returns the canonical sqlc input for d: the five statements this store
// executes, in one file's worth of text.
//
// It is what authentication/signin/magiclinks/internal/queriesgen writes to the
// .sql files beside this one, and what CI regenerates to check the committed
// copies still match. Those files are sqlc-gen-unison's input, so what the store
// executes is this text exactly — the generated magiclinkdb package carries it
// per dialect, with the consumer's table prefix substituted once at
// construction.
//
// The order is the order a link goes through: minted, read, followed — or
// withdrawn as one of a subject's — and finally collected once its purge
// deadline has passed.
//
// # Where liveness is decided, and why it is here
//
// In the predicate, following authentication/signin/refreshtokens and
// authentication/oauth2serverstore rather than authentication/passwordreset and
// links/database, which both decide it in Go against the record as it was read.
//
// The reason is narrower here than next door. There, the deadline is folded into
// a guard that already had to exist for reuse detection, and the argument is
// that it costs nothing once the read-back is there. Here there is no reuse
// detection to piggyback on — a spent link revokes nothing and ends no family —
// so the guard earns its place on its own: a link that expires between the read
// and the write must not be redeemable by the write, and a Go-side check made a
// round trip earlier cannot say that. It is the same reading the outbox lease
// defect produced, applied before rather than after.
//
// # A note on timestamps, because one dialect does something surprising
//
// Every instant this corpus binds is a UTC time.Time and stays one all the way
// down: the store reads its clock as UTC, and the generated SQLite arm converts
// again before it binds. Postgres and MySQL store these as real temporal types.
// SQLite has no date type at all, so a DATETIME column holds text, and both the
// redemption's `expires_at > sqlc.arg(now)` and the sweep's
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
// link on that engine goes dead up to a second early rather than living a second
// past its deadline, and a row is collected up to a second late rather than while
// something might still be deciding about it. A link's lifetime is minutes, so a
// second is a rounding error against it rather than a meaningful share.
//
// # Why there is no standard set
//
// [querygen.Generator.StandardCRUD] serves a table with a surrogate id, a paged
// list keyed on it, and the convention triple of timestamps. This table has none
// of that, and every absence is deliberate — see the migrations package. Its key
// is the digest of a credential rather than a surrogate; nothing lists these
// rows, because the only way to name one is to hold the token it was minted
// from; and an archived_at would keep rows nothing can read while making the
// sweep the one write unable to reach the rows it exists for.
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
		redeem(g),
		revokeForSubject(g),
		sweep(g),
	})
}

// insert is the mint write.
//
// It is a plain INSERT rather than an upsert or an insert-ignore, and the
// difference is what the key means. The hash is the digest of the token, so a
// second row bearing one would mean the generator produced the same token twice.
// The primary key refuses that and this statement lets it: a mint failing loudly
// is the correct outcome of randomness that has stopped being random, where an
// ignore would hand the caller a link that redeems somebody else's row.
//
// Minting again does not withdraw what is outstanding, because nothing here
// says it should: a person who asks for a link twice and then opens the first
// message has a link that works, which is the behavior the alternative quietly
// breaks. authentication/passwordreset takes the same reading of the same
// situation, and says so on Store.Issue.
func insert(g *querygen.Generator) *querygen.Query {
	return g.InsertQuery(InsertLinkQuery, LinksTable, InsertColumns, nil)
}

// read is the lookup the redemption makes after its guarded write, and it runs
// on both paths.
//
// On the winning path it is how the subject is learned at all: the row is what
// says who signed in, and MySQL has no RETURNING to hand it back from the write.
// On the losing path it is what turns an ambiguous zero into something an
// operator can read — expired, already followed, withdrawn, or no such row — none
// of which the caller is told apart, and all of which a span should distinguish.
//
// It keys on the hash and the scope and on neither the deadline nor the two
// stamps, because what it exists to answer on the losing path is which of those
// refused the write. The scope is a predicate rather than a check made on the row
// afterwards, so a token presented in the wrong directory matches nothing and
// reads as absent, which is what it is from there.
//
// It is a [querygen.Generator.ReadQuery] rather than a get because both lists are
// narrower than the table: the key goes over as the two columns it names, and the
// projection goes back without the hash.
func read(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery(GetLinkQuery, LinksTable, Columns,
		querygen.Read{Projection: RecordColumns},
		querygen.Match{Column: HashColumn},
		querygen.Match{Column: ScopeColumn},
	)
}

// redeem is the write that spends a sign-in link, and it is the statement that
// decides single use.
//
// Five predicates, and every one of them repeats a row-state test the answer
// depends on. Two requests presenting one token at the same instant both reach
// this statement; the first one's update matches, the second one's finds
// redeemed_at already set and reports no rows. The count is the answer, which is
// why a read cannot be, and why the statement is annotated :execrows.
//
// The two stamp guards bind nothing, which is what makes them guards rather than
// predicates: there is no argument a caller could leave unset to relax them, and
// a caller has no value to bind for "has not happened". The deadline is the one
// comparison that binds, and it binds the store's own clock — see [NowArg].
//
// Nothing here is checked in Go first and then again in the predicate. A write
// that repeated only some of the row-state tests its own decision rests on is
// the defect outbox's lease mode paid for: every test the answer depends on is
// made by the write, at the instant the row changes, rather than by a caller a
// round trip earlier.
func redeem(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(RedeemLinkQuery, LinksTable, Columns, RedeemColumns, nil,
		querygen.Match{Column: HashColumn},
		querygen.Match{Column: ScopeColumn},
		unredeemed(),
		unrevoked(),
		stillLive(),
	)
}

// revokeForSubject withdraws every outstanding link one person holds: the
// sign-in that completed through another door, "disable this account", and the
// erasure a dataprivacy run performs.
//
// It is keyed on the subject rather than on the hash, and neither is expressible
// as the other. A caller holding only a subject identifier cannot enumerate that
// person's outstanding links without a read this corpus does not have — the rows
// are addressed by the digest of a credential nobody but the recipient holds —
// and there is nothing to loop over even if they wanted to.
//
// The guard is the revocation's own, so a second revocation matches nothing and
// reports zero rows rather than moving the timestamp — the record still says when
// the links actually stopped working.
//
// Expired rows are matched and moved, because this corpus carries no liveness
// predicate outside the redemption. A link that lapsed on its own and one
// somebody withdrew both end up revoked, and after an account is disabled
// "somebody withdrew this" is the more useful of the two true sentences.
//
// It is confined to one scope, which excludes nothing that exists: a subject id
// is already scope-unique, since identity_users is unique on (scope, username)
// and a user id belongs to exactly one directory. That is where this parts
// company with links' subject-wide revoke, which crosses tenants deliberately
// because its subjects are opaque identifiers it cannot resolve.
func revokeForSubject(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(RevokeLinksForSubjectQuery, LinksTable, Columns, RevokeColumns, nil,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: SubjectIDColumn},
		unrevoked(),
	)
}

// sweep is the removal of every row past its purge deadline.
//
// Followed and withdrawn rows go with them, at that deadline rather than at
// their redemption, and that is the whole retention policy. What the gap buys is
// what an operator reads off a span: a row collected at its own expiry turns a
// second click into "no such link", where the row that is still there says "this
// was already used". Neither is told to the caller — see [redeem] — and the
// difference is the one an incident is reconstructed from.
//
// It spans every scope, which is the one statement here that does, and it carries
// no cap. Link rows are small and the index on purge_after makes the delete
// proportional to what is actually dead rather than to the table, so this is
// [querygen.Generator.DeleteQuery] with a horizon rather than
// [querygen.Generator.PruneQuery]: there is no backlog for a bound to protect
// against, and a bounded pass would make Sweep's count a loop condition rather
// than an answer.
func sweep(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(SweepLinksQuery, LinksTable, Columns,
		querygen.Match{Column: PurgeAfterColumn, Against: querygen.AtMostArgument, Arg: PurgeBeforeArg},
	)
}

// unredeemed is the guard that makes a single-use credential single-use: the
// stamp saying it has been followed is not there yet.
func unredeemed() querygen.Match {
	return querygen.Match{Column: RedeemedAtColumn, Against: querygen.NoValue}
}

// unrevoked is the guard the redemption and the revocation share.
//
// On the redemption it is what keeps a withdrawn link from being followed. On
// the revocation it is what makes revoking idempotent in the way a caller needs.
func unrevoked() querygen.Match {
	return querygen.Match{Column: RevokedAtColumn, Against: querygen.NoValue}
}

// stillLive is the redemption's deadline guard: this row's deadline has not been
// reached at the instant the caller named.
//
// It is derived from [elapsed] rather than written beside it. The two are one
// boundary read in two directions — the rows a redemption may spend, and the rows
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

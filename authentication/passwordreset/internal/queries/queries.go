package queries

import (
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// TokensTable is the reset token table at its canonical, unprefixed spelling —
// what the emitted .sql names, and what the store's own prefix rendering starts
// from.
//
// The password_reset segment is the schema's own, so a table says which package
// created it even in a database shared between applications.
const TokensTable = "password_reset_tokens"

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
// carry are named from these, and a column spelled twice is a column that can
// be spelled differently. The two conventional ones come from querygen rather
// than being restated here.
const (
	// ScopeColumn is whose token a row is — an account, an organization, a
	// workspace, or, as the empty string, nobody. Every read filters on it, and
	// it is bound as the tenancy.Scope itself rather than as a string derived
	// from one; see unison.yaml, where that type override lives.
	ScopeColumn = "scope"
	// UserColumn is the principal the token resets. It carries no REFERENCES —
	// see the migrations package — so it is an identifier this table cannot
	// resolve rather than a foreign key.
	UserColumn = "belongs_to_user"
	// DigestColumn holds the digest of the token and never the token. It is
	// bound by the insert and compared against by the lookup, and it is the one
	// column of this table that no statement here projects — see
	// [TokenColumns].
	DigestColumn = "token_digest"
	// ExpiresAtColumn is the deadline the token stops being spendable at. It is
	// what the sweep is keyed on, and it is compared in Go by the store rather
	// than in the lookup's WHERE clause — see [Render].
	ExpiresAtColumn = "expires_at"
	// RedeemedAtColumn is when the token was spent, and NULL while it is still
	// spendable. It is the column the redemption assigns and the column the
	// redemption guards on, which is what makes single use a property of the
	// statement rather than of whoever read the row first.
	RedeemedAtColumn = "redeemed_at"
)

// Columns is the whole row, in the order the DDL declares it.
//
// No statement here is rendered from this list — the redemption takes it for
// its key and everything else takes a narrower one — and it is here for the
// cross-check against the shipped DDL, which is the one place a column added to
// the schema and not to this package stops being invisible.
var Columns = []string{
	querygen.IDColumn,
	ScopeColumn,
	UserColumn,
	DigestColumn,
	ExpiresAtColumn,
	RedeemedAtColumn,
	querygen.CreatedAtColumn,
}

// TokenColumns is what the lookup projects, in the order the generated row type
// carries them: the whole table less the digest.
//
// The digest's absence is the point, and it is why this is a projection rather
// than a SELECT * with a Go-side drop. Nothing in this package ever reads the
// column back — it is bound by the insert and compared against by the lookup —
// and a projection that included it would be a projection that put a stored
// credential's digest in whatever a caller did next with the row.
var TokenColumns = []string{
	querygen.IDColumn,
	ScopeColumn,
	UserColumn,
	ExpiresAtColumn,
	RedeemedAtColumn,
	querygen.CreatedAtColumn,
}

// KeyedColumns is the table's shape as the statements keyed on something other
// than the id are rendered from: [Columns] without the id.
//
// It is the idiom every keyed read in this module uses. querygen derives a
// statement's key from the column list it is handed, so a statement that must
// not carry an id predicate is handed a list with no id in it — and what a
// statement projects is a separate list, so leaving the column out here does
// not take it out of the answer.
var KeyedColumns = []string{
	ScopeColumn,
	UserColumn,
	DigestColumn,
	ExpiresAtColumn,
	RedeemedAtColumn,
	querygen.CreatedAtColumn,
}

// InsertColumns is what an issuance writes: every column but redeemed_at, which
// is NULL by definition on a token nobody has spent yet.
//
// created_at is in it, which is where this table parts company with the
// convention [querygen.ForInsert] encodes. Everywhere else the database owns
// the creation time, because a caller-supplied one is how a row ends up with a
// creation time that disagrees with the id a cursor walks by. Nothing lists
// these rows, so there is no walk to disagree with — and the issuance reads one
// clock for the creation time and the deadline it computes from it, which is
// the property that makes a token's whole lifetime one clock's.
var InsertColumns = []string{
	querygen.IDColumn,
	ScopeColumn,
	UserColumn,
	DigestColumn,
	ExpiresAtColumn,
	querygen.CreatedAtColumn,
}

// RedeemColumns is what the redemption assigns, which is the stamp and nothing
// else.
//
// There is no last_updated_at beside it, and no stamp of querygen's own, because
// this table carries no such column: redemption is the only mutation a reset
// token row has, so a last_updated_at would be a second copy of redeemed_at
// free to disagree with it.
var RedeemColumns = []string{RedeemedAtColumn}

// The query names the generated querier's methods are built from. They are
// spelled here because the store names them too — through the generated params
// types — and because the drift gate beside this file asserts on this exact
// set.
const (
	InsertTokenQuery         = "InsertToken"
	GetTokenByDigestQuery    = "GetTokenByDigest"
	RedeemTokenQuery         = "RedeemToken"
	RevokeTokensForUserQuery = "RevokeTokensForUser"
	SweepExpiredTokensQuery  = "SweepExpiredTokens"
)

// ExpiresBeforeArg is the sqlc argument the sweep binds its horizon through.
//
// It is named for the comparison rather than for the column, because the column
// is already an argument name in this corpus: the insert binds expires_at as
// the value it writes, and a sweep binding a ceiling under the same name would
// be one name for two different facts about a row.
const ExpiresBeforeArg = "expires_before"

// Render returns the canonical sqlc input for d: the five statements this
// store executes, in one file's worth of text.
//
// It is what authentication/passwordreset/internal/queriesgen writes to the
// .sql files beside this one, and what CI regenerates to check the committed
// copies still match. Those files are sqlc-gen-unison's input, so what the store
// executes is this text exactly — the generated passwordresetdb package carries
// it per dialect, with the consumer's table prefix substituted once at
// construction.
//
// # A note on timestamps, because one dialect does something surprising
//
// Every instant this corpus binds is a UTC time.Time and stays one all the way
// down: the store reads its clock as UTC, and the generated SQLite arm converts
// again before it binds. Postgres and MySQL store these as real temporal types.
// SQLite has no date type at all, so a DATETIME column holds text, and the
// sweep's `expires_at <= sqlc.arg(expires_before)` there is a string
// comparison.
//
// That comparison is chronological rather than merely lexical because both
// sides are rendered "YYYY-MM-DD HH:MM:SS" in UTC — a fixed-width prefix, one
// zone. A value bound in any other zone would put that zone's wall clock in
// those leading characters and every comparison would be off by the offset,
// silently, and only for the deployments whose clock is not UTC. Neither UTC
// conversion is decoration.
//
// The rendering is whole seconds there, so an instant carrying a fraction is
// stored truncated down, and the direction is the one that fails closed.
// Liveness is decided in Go against expires_at as it was read, so a link on
// that engine goes dead up to a second early rather than living a second past
// its deadline.
//
// # Why there is no liveness predicate
//
// The lookup does not filter on the deadline and the redemption does not guard
// on it. The store compares in Go, against the clock it was handed, and that is
// a decision rather than an omission this corpus could tidy away.
//
// The sweep is the one statement that may compare the deadline in SQL, and it
// can afford to because it deletes rows that are dead by any reading. The
// boundary a user hits at the last second of a link's life is worth deciding
// against one clock, in one place, on all three engines — and a guard here would
// be a second copy of it, free to disagree with Token.Live about which second a
// link stopped working, and to collapse "expired" and "already redeemed" into
// one affected-row count of zero.
//
// # Why there is no standard set
//
// [querygen.Generator.StandardCRUD] serves a table with a surrogate id, a paged
// list keyed on it, and the convention triple of timestamps. This table has the
// id and none of the rest, and every absence is deliberate — see
// authentication/passwordreset/migrations. A reset token is issued, spent once
// and gone, so an archived_at would keep rows nothing can ever read and would
// make the sweep the one write unable to reach the rows it exists for; nothing
// lists these rows, so there is no cursor and no window; and redemption is the
// row's only mutation, so there is no last mutation to record beside it.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// The one table this package owns. StandardCRUD would have registered it,
	// and StandardCRUD cannot serve this table at all — so the registration is
	// made by the table existing rather than by something choosing to emit its
	// standard set, which is the distinction the registry is built around.
	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		insert(g),
		lookup(g),
		redeem(g),
		revoke(g),
		sweep(g),
	})
}

// insert is the issuance write.
//
// It is a plain INSERT rather than an upsert, unlike the ceremony store's
// equivalent, and the difference is what the key means. A challenge is a
// ceremony's name and beginning it twice replaces it; a digest is a secret, and
// a second row bearing one would mean the generator produced the same token
// twice. The unique index refuses that, and this statement lets it: an issuance
// failing loudly is the correct outcome of randomness that has stopped being
// random.
//
// Nothing here is nullable. redeemed_at is the row's one nullable column and it
// is not in the list at all — a token nobody has spent yet is a row whose stamp
// has never been written, which is what the column's absence from an insert
// says and what a bound NULL would only restate.
func insert(g *querygen.Generator) *querygen.Query {
	return g.InsertQuery(InsertTokenQuery, TokensTable, InsertColumns, nil)
}

// lookup is the read Verify and Consume both begin with.
//
// It keys on the digest and the scope and on neither the id nor the deadline.
// The scope is a predicate rather than a check made on the row afterwards, so a
// token presented in the wrong tenant matches nothing and reads as absent —
// which is what it is from there.
//
// It is a [querygen.Generator.ReadQuery] rather than a get because both lists
// are narrower than the table: the shape list goes over without the id, since
// the caller holds a secret rather than a row's name, and the projection goes
// back without the digest.
func lookup(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery(GetTokenByDigestQuery, TokensTable, KeyedColumns,
		querygen.Read{Projection: TokenColumns},
		querygen.Match{Column: DigestColumn},
		querygen.Match{Column: ScopeColumn},
	)
}

// redeem is the write that spends a token, and it is the statement that decides
// single use.
//
// The guard is redeemed_at IS NULL rather than an equality against what the read
// saw, because "has not happened yet" is not a value a caller holds. Two
// requests that both read the row live both reach this statement; the first
// one's update matches, the second one's finds redeemed_at already set and
// reports no rows. The count is the answer, which is why the read cannot be, and
// why the statement is annotated :execrows.
//
// It binds nothing for the guard, so the assignment and the predicate need no
// second argument name between them: the value the guard compares against
// belongs to the statement, and there is nothing a caller could pass that would
// relax it.
func redeem(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(RedeemTokenQuery, TokensTable, Columns, RedeemColumns, nil,
		querygen.Match{Column: RedeemedAtColumn, Against: querygen.NoValue},
	)
}

// revoke is the destruction of one principal's outstanding tokens.
//
// Redeemed rows are excluded rather than swept up with the rest. They are
// already unspendable, and they are what answers "this link has already been
// used" for the rest of the link's life — a revocation that removed them would
// turn a completed reset's own token into one that never existed.
//
// Its key names more than one row, which is what separates a delete from an
// archive here: the count is how many outstanding links a completed reset just
// invalidated.
func revoke(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(RevokeTokensForUserQuery, TokensTable, KeyedColumns,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: UserColumn},
		querygen.Match{Column: RedeemedAtColumn, Against: querygen.NoValue},
	)
}

// sweep is the removal of every row whose deadline has passed.
//
// Redeemed rows go with them, at their own expiry rather than at their
// redemption, and that is the whole retention policy: a spent link keeps
// answering "already used" for exactly as long as it would have kept working.
// Deleting on redemption instead would make the most common reason a link fails
// indistinguishable from a link nobody ever issued.
//
// The horizon is bound rather than read off the server's clock, and that is
// [querygen.AtMostArgument]'s named case rather than a preference. expires_at is
// stamped by the store's own clock — now plus a TTL, from a clock the store was
// handed — so comparing it against CURRENT_TIMESTAMP would be two clocks
// deciding one row, and under a test clock that only moves when a test moves it
// the two are years apart.
//
// It spans every scope, which is the one statement here that does, and it
// carries no cap. Token rows are small and the index on expires_at makes the
// delete proportional to what is actually dead rather than to the table, so this
// is [querygen.Generator.DeleteQuery] with a horizon rather than
// [querygen.Generator.PruneQuery]: there is no backlog for a bound to protect
// against, and a bounded pass would make Sweep's count a loop condition rather
// than an answer.
func sweep(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(SweepExpiredTokensQuery, TokensTable, KeyedColumns,
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

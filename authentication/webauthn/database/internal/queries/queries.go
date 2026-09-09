package queries

import (
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// SessionsTable is the ceremony session table at its canonical, unprefixed
// spelling — what the emitted .sql names, and what the store's own prefix
// rendering starts from.
const SessionsTable = "webauthn_sessions"

// The table's columns, spelled once. The store binds arguments under these
// names through the generated params structs, and the emitted statements
// interpolate them; two spellings of one column is the drift this package
// exists to prevent.
const (
	// ChallengeColumn is the primary key and the whole of it. A challenge is at
	// least 16 bytes of cryptographic randomness, which is what lets a natural
	// key stand in for a surrogate id here: nothing else identifies a ceremony,
	// and an id beside it would be a second name for the same row.
	ChallengeColumn = "challenge"
	// SessionDataColumn holds the encoded ceremony state.
	SessionDataColumn = "session_data"
	// ExpiresAtColumn is the deadline the ceremony stops being answerable at.
	ExpiresAtColumn = "expires_at"
)

// Columns is the table's shape, in the order the emitted statements project it.
//
// It carries none of the convention triple and no id, which is what decides
// most of what is emitted here: no created_at or last_updated_at window, no
// archived predicate on any statement, and no standard set at all — see the
// package comment.
var Columns = []string{ChallengeColumn, SessionDataColumn, ExpiresAtColumn}

// StateColumns is what a read of one ceremony projects: the state, and the
// deadline Consume compares before handing it out.
//
// It is a separate list from [Columns] because a keyed read's projection and
// its predicates come from different lists — the challenge is the key rather
// than part of the answer, since the caller already holds it.
var StateColumns = []string{SessionDataColumn, ExpiresAtColumn}

// The query names the generated querier's methods are built from. They are
// spelled here because the store names them too — through the generated params
// types — and because the drift gate below asserts on this exact set.
const (
	UpsertSessionQuery        = "UpsertSession"
	GetSessionQuery           = "GetSession"
	DeleteSessionQuery        = "DeleteSession"
	SweepExpiredSessionsQuery = "SweepExpiredSessions"
)

// Render returns the canonical sqlc input for d: the four statements this
// store executes, in one file's worth of text.
//
// It is what authentication/webauthn/database/internal/queriesgen writes to the
// .sql files beside this one, and what CI regenerates to check the committed
// copies still match. Those files are sqlc-gen-unison's input, so what the
// store executes is this text exactly — the generated webauthndb package
// carries it per dialect, with the consumer's table prefix substituted once at
// construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// The one table this package owns. StandardCRUD would have registered it,
	// and StandardCRUD cannot serve this table at all — so the registration is
	// made by the table existing rather than by something choosing to emit its
	// standard set, which is the distinction the registry is built around.
	querygen.RegisterTable(SessionsTable)

	return querygen.RenderFile([]*querygen.Query{
		upsert(g),
		read(g),
		consume(g),
		sweep(g),
	})
}

// upsert is the write of one ceremony's state.
//
// It converges rather than inserting, and the alternatives are worse in both
// directions: an INSERT that collided would have to be recognized from a
// dialect-specific SQLSTATE, and an insert-ignore would keep the old row and
// hand the next Consume state for a ceremony nobody is running. A challenge is
// cryptographic randomness, so a collision means the same ceremony was begun
// twice and the later one is the live one.
//
// The conflict target is the challenge, which is the table's primary key —
// Postgres and SQLite match ON CONFLICT against an index the table actually
// has, and this is the only one it has. The key column is not assigned in the
// conflict branch; querygen drops it, which is what keeps the statement right
// on MySQL, where a collision may have been detected on some other unique key.
func upsert(g *querygen.Generator) *querygen.Query {
	return g.UpsertQuery(UpsertSessionQuery, SessionsTable,
		Columns,
		querygen.ForInsert(Columns),
		[]string{SessionDataColumn, ExpiresAtColumn},
		nil,
		querygen.Match{Column: ChallengeColumn},
	)
}

// read is the read half of Consume: the state and the deadline for one
// challenge.
//
// It is a [querygen.Generator.ReadQuery] rather than a get because the
// projection is narrower than the table — the challenge is the key, and the
// caller is holding it already — and because the key is a natural one, which
// querygen renders from a column list carrying no id.
//
// It does not filter on the deadline, deliberately. Consume compares it in Go,
// so an expired row is removed by the delete that follows rather than left
// behind by a read that could not see it.
func read(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery(GetSessionQuery, SessionsTable, Columns,
		querygen.Read{Projection: StateColumns},
		querygen.Match{Column: ChallengeColumn},
	)
}

// consume is the delete half of Consume, which is also the half that decides
// who owns the ceremony.
//
// It is annotated :execrows because the count is the answer: two requests
// answering the same challenge at the same instant both read the row, and the
// one whose delete reports no rows is told there is no ceremony.
func consume(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(DeleteSessionQuery, SessionsTable, Columns,
		querygen.Match{Column: ChallengeColumn},
	)
}

// sweep is the removal of every row whose deadline has passed.
//
// The comparison is against the server's clock rather than a bound instant, and
// that is a change of meaning as well as of spelling. A bound time.Time is
// stored by SQLite's driver as Go's own rendering, which compares against
// nothing the server writes; the server's own clock is one expression all three
// dialects agree on and the one every other expiry sweep in this module uses —
// see [querygen.CurrentTime].
//
// It costs nothing here because the sweep is not what makes a ceremony expire.
// Consume refuses a row past its deadline against the store's clock, so a row
// the sweep has not reached is already unusable, and what the sweep does is
// stop the table growing by a row for every ceremony ever begun.
func sweep(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(SweepExpiredSessionsQuery, SessionsTable, Columns,
		querygen.Match{Column: ExpiresAtColumn, Against: querygen.CurrentTime},
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

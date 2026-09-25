package queries

import (
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// CodesTable is the recovery code table at its canonical, unprefixed spelling —
// what the emitted .sql names, and what the store's own prefix rendering starts
// from.
//
// The signin_ segment is the schema's own, so a table says which package created
// it even in a database shared between applications.
const CodesTable = "signin_recovery_codes"

// TableNames is every table this package owns, which is one.
//
// It is a list rather than the constant above because the querygen registry
// takes one, and because a consumer reading that registry back to truncate a
// database between integration tests is asking "what tables does this component
// have rows in" rather than "what does it generate SQL for".
var TableNames = []string{CodesTable}

// The columns the statements below name, and the store binds by.
//
// Exported because both halves spell them: the arguments the generated params
// carry are named from these, and a column spelled twice is a column that can be
// spelled differently.
const (
	// ScopeColumn is whose directory the code was minted in. Every statement here
	// filters on it, and it is bound as the tenancy.Scope itself rather than as a
	// string derived from one; see unison.yaml, where that type override lives.
	ScopeColumn = "scope"
	// UserIDColumn is whose code this is. It carries no REFERENCES — see the
	// migrations package — so it is an identifier this table cannot resolve
	// rather than a foreign key.
	UserIDColumn = "user_id"
	// HashColumn is the hex digest a code is stored under. It is the whole of
	// what makes a dump of this table useless to a thief: the code itself is
	// never written down. It is bound by the insert, compared against by the
	// check and the spend, and projected by nothing — see [RecordColumns].
	HashColumn = "hash"
	// IssuedAtColumn is when the set the code belongs to was minted.
	IssuedAtColumn = "issued_at"
	// UsedAtColumn is when the code was spent, and NULL until it is. It is the
	// column the spend assigns and the one it guards on, which is what makes
	// single use a property of the statement rather than of whoever read the row
	// first.
	UsedAtColumn = "used_at"
)

// Columns is the whole row, in the order the DDL declares it.
//
// It is what the writes and the key-matched reads are rendered from. querygen
// derives a statement's id predicate from the list it is handed and this table
// has no id — the key is the owner and the digest of a code — so every predicate
// below is one the statement names explicitly.
var Columns = []string{
	ScopeColumn,
	UserIDColumn,
	HashColumn,
	IssuedAtColumn,
	UsedAtColumn,
}

// RecordColumns is what the list projects, in the order the generated row type
// carries them: the whole table less the hash.
//
// The hash's absence is the point, and it is why this is a projection rather
// than a SELECT * with a Go-side drop. A digest of sixty random bits is a
// digest somebody with a GPU can reverse in an afternoon, and the list is the
// read a subject access request exports — so it is the one read that must not
// carry it.
var RecordColumns = []string{
	ScopeColumn,
	UserIDColumn,
	IssuedAtColumn,
	UsedAtColumn,
}

// InsertColumns is what a mint writes: every column but the stamp, which is
// NULL by definition on a code nobody has spent yet.
var InsertColumns = []string{
	ScopeColumn,
	UserIDColumn,
	HashColumn,
	IssuedAtColumn,
}

// SpendColumns is what the spend assigns, which is the stamp and nothing else.
var SpendColumns = []string{UsedAtColumn}

// The query names the generated querier's methods are built from. They are
// spelled here because the store names them too — through the generated params
// types — and because the drift gate beside this file asserts on this exact set.
const (
	InsertCodeQuery         = "InsertRecoveryCode"
	CodeUnspentQuery        = "RecoveryCodeUnspent"
	SpendCodeQuery          = "SpendRecoveryCode"
	CountUnspentCodesQuery  = "CountUnspentRecoveryCodes"
	ListCodesForUserQuery   = "ListRecoveryCodesForUser"
	DeleteCodesForUserQuery = "DeleteRecoveryCodesForUser"
)

// Render returns the canonical sqlc input for d: the six statements this store
// executes, in one file's worth of text.
//
// It is what authentication/signin/recoverycodes/internal/queriesgen writes to
// the .sql files beside this one, and what CI regenerates to check the committed
// copies still match. Those files are sqlc-gen-unison's input, so what the store
// executes is this text exactly — the generated recoverycodedb package carries it
// per dialect, with the consumer's table prefix substituted once at
// construction.
//
// The order is the order a set goes through: minted, checked, spent, counted,
// listed, and finally deleted — by a replacement, or with its holder.
//
// # Why there is no clock in any predicate
//
// A recovery code does not lapse, so nothing here compares a column against an
// instant, and SQLite's whole-second rendering of these DATETIME columns is a
// rounding of a record rather than of a decision. The two stamps are written
// and read back; neither is compared.
//
// # Why there is no standard set
//
// [querygen.Generator.StandardCRUD] serves a table with a surrogate id, a paged
// list keyed on it, and the convention triple of timestamps. This table has none
// of that, and every absence is deliberate — see the migrations package. Its key
// is the owner and the digest of a credential rather than a surrogate; the one
// list is unpaged, because a set is eight rows rather than a history; and an
// archived_at would keep a replaced set readable when the whole point of
// replacing it is that it stops working.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// The one table this package owns. StandardCRUD would have registered it, and
	// StandardCRUD cannot serve this table at all — so the registration is made by
	// the table existing rather than by something choosing to emit its standard
	// set, which is the distinction the registry is built around.
	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		insert(g),
		unspent(g),
		spend(g),
		countUnspent(g),
		listForUser(g),
		deleteForUser(g),
	})
}

// insert is the mint write, run once per code in a set.
//
// It is a plain INSERT rather than an upsert or an insert-ignore, and the
// difference is what the key means. Two rows under one owner bearing one digest
// would mean the generator produced the same code twice inside a set — and a
// write that ignored the second would hand a person one fewer code than they
// were shown, which is a code that fails on the day they need it. The primary
// key refuses that and this statement lets it, so the whole replacement rolls
// back loudly instead.
func insert(g *querygen.Generator) *querygen.Query {
	return g.InsertQuery(InsertCodeQuery, CodesTable, InsertColumns, nil)
}

// unspent is the check a sign-in makes before it opens a transaction: does this
// person hold this code, still unspent?
//
// It is a read and it decides nothing. The spend below repeats every test this
// one makes, in its own predicate, at the instant the row changes — this read
// exists so that a door whose second-factor check runs before its transaction
// can tell "wrong code" from "right code" without writing, and so a wrong code
// costs a read rather than a write.
func unspent(g *querygen.Generator) *querygen.Query {
	return g.ExistsQuery(CodeUnspentQuery, CodesTable, Columns,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: UserIDColumn},
		querygen.Match{Column: HashColumn},
		unused(),
	)
}

// spend is the write that burns a code, and it is the statement that decides
// single use.
//
// Four predicates, and every one of them repeats a row-state test the answer
// depends on. Two sign-ins presenting one code at the same instant both reach
// this statement; the first one's update matches, the second one's finds
// used_at already set and reports no rows. The count is the answer, which is
// why [unspent] cannot be, and why the statement is annotated :execrows.
func spend(g *querygen.Generator) *querygen.Query {
	return g.UpdateQuery(SpendCodeQuery, CodesTable, Columns, SpendColumns, nil,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: UserIDColumn},
		querygen.Match{Column: HashColumn},
		unused(),
	)
}

// countUnspent is how many codes a person has left, which is what a settings
// page shows and what the hook a spend runs is handed.
func countUnspent(g *querygen.Generator) *querygen.Query {
	return g.CountQuery(CountUnspentCodesQuery, CodesTable, Columns,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: UserIDColumn},
		unused(),
	)
}

// listForUser is every code one person holds, spent and unspent, for the
// export a subject access request makes.
//
// It is unpaged, and the bound is structural: a replacement deletes the set it
// replaces, so what one person holds is one set — eight rows by default — rather
// than their history. A nil junction on the unpaged list is the construct for a
// many-row read keyed on something other than an id.
//
// It projects [RecordColumns], so the digest never leaves the table this way.
func listForUser(g *querygen.Generator) *querygen.Query {
	return g.JunctionListAllQuery(ListCodesForUserQuery, CodesTable, RecordColumns, nil,
		[]querygen.Order{{Column: IssuedAtColumn}},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: UserIDColumn},
	)
}

// deleteForUser removes every code one person holds, spent or not.
//
// It is the first half of a replacement — the set being replaced goes in the
// same transaction the new one is written in — and the whole of an erasure. A
// spent code goes with the rest because nothing reads one back but the export,
// and a replacement that left the old set's spent rows beside the new one would
// make "how many codes has this person used" a question about every set they
// ever held.
func deleteForUser(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery(DeleteCodesForUserQuery, CodesTable, Columns,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: UserIDColumn},
	)
}

// unused is the guard that makes a single-use credential single-use: the stamp
// saying it has been spent is not there yet.
func unused() querygen.Match {
	return querygen.Match{Column: UsedAtColumn, Against: querygen.NoValue}
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

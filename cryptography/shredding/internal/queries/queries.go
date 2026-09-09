package queries

import (
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// SubjectKeysTable is the one table this package owns, at its canonical
// spelling — what the emitted .sql names, and what shredding's own prefix
// rendering starts from.
const SubjectKeysTable = "shredding_subject_keys"

// TableNames is every table shredding owns, which is one.
//
// It is a list rather than the constant above because the querygen registry
// takes one, and because a consumer reading that registry back to truncate a
// database is asking "what tables does this component have rows in" rather than
// "what does it generate SQL for". Those are the same question here and were
// not in identity, which is the reason the registry exists.
var TableNames = []string{SubjectKeysTable}

// The columns the statements below name, and the store binds by.
//
// Exported because both halves spell them: the arguments the generated params
// carry are named from these, and a column spelled twice is a column that can
// be spelled differently. The three conventional ones come from querygen rather
// than being restated here.
const (
	// SubjectTypeColumn says what kind of principal a row's subject is. It is
	// the first half of the natural key.
	SubjectTypeColumn = "subject_type"
	// SubjectIDColumn identifies the subject within its type, and is the second
	// half of the natural key.
	SubjectIDColumn = "subject_id"
	// WrappedKeyColumn holds the data key encrypted under the root key, and
	// NULL once it has been destroyed.
	WrappedKeyColumn = "wrapped_key"
	// ShreddedAtColumn is when the key material was destroyed, and NULL while
	// it still exists. It is the column the shred assigns and the column the
	// shred guards on, which is why it is the one place in this schema an
	// argument name and a predicate meet — see [Render].
	ShreddedAtColumn = "shredded_at"
)

// Columns is the whole row, in the order the DDL declares it.
//
// Nothing renders a statement from this list — the statements take
// [RecordColumns] — and it is here for the cross-check against the shipped DDL,
// which is the one place a column added to the schema and not to this package
// stops being invisible.
var Columns = []string{
	SubjectTypeColumn,
	SubjectIDColumn,
	WrappedKeyColumn,
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	querygen.ArchivedAtColumn,
	ShreddedAtColumn,
}

// RecordColumns is the row as a shredding.Record sees it: what every read
// projects, in the order it projects them, and what the insert supplies values
// for.
//
// One list rather than two, because the two would have to be equal — see the
// package comment. It is also the shape list every statement here is rendered
// from, which is what leaves them with no archived predicate and no stamp of
// their own.
//
// created_at is in it, which is where this table parts company with the
// convention [querygen.ForInsert] encodes: everywhere else the database owns
// the creation time, because a caller-supplied one is how a row ends up with a
// creation time that disagrees with the id a cursor walks by. This table has no
// id and no list, so there is no walk to disagree with, and the time a key was
// minted is the caller's fact rather than the row's — a shredding.Record
// carries it, and the tombstone binds the instant of the destruction to it.
var RecordColumns = []string{
	SubjectTypeColumn,
	SubjectIDColumn,
	WrappedKeyColumn,
	querygen.CreatedAtColumn,
	ShreddedAtColumn,
}

// NullableColumns names the columns a statement here may write NULL to.
//
// Three of them, and each records something that has not happened. wrapped_key
// is NULL on a tombstone, where there was never key material to destroy;
// shredded_at is NULL while the key is live; last_updated_at is NULL until the
// shred rewrites the row, which is the only update this table takes.
var NullableColumns = []string{
	WrappedKeyColumn,
	ShreddedAtColumn,
	querygen.LastUpdatedAtColumn,
}

// ShredColumns is what the destruction assigns, in the order the SET list
// renders them.
//
// last_updated_at is in the list rather than left to querygen's own stamp, and
// that is the one place this schema asks for something the conventional tables
// do not. querygen stamps the column from the server's clock; here it has to
// hold the same instant as shredded_at, because this is the only statement that
// rewrites a key row and the two columns therefore describe one event. Two
// clock reads could have them disagree about when the destruction happened,
// which is a thing to have to explain to a regulator. So the shape list handed
// to [querygen.Generator.UpdateQuery] omits the column — no stamp — and this
// list names it, bound to the instant the caller supplied.
var ShredColumns = []string{
	WrappedKeyColumn,
	ShreddedAtColumn,
	querygen.LastUpdatedAtColumn,
}

// keyMatches is the natural key as predicates: the pair that addresses exactly
// one row, and the conflict target the insert skips a collision on.
//
// It is one function rather than a package-level slice because every caller
// appends to what it returns, and a shared slice appended to is a slice whose
// backing array two statements can come to share.
func keyMatches() []querygen.Match {
	return []querygen.Match{
		{Column: SubjectTypeColumn},
		{Column: SubjectIDColumn},
	}
}

// Render returns the canonical sqlc input for d: the three statements this
// package's store executes, in one file's worth of text.
//
// It is what cryptography/shredding/internal/queriesgen writes to the .sql
// beside this file and what CI regenerates to check the committed copy still
// matches. That .sql is sqlc-gen-unison's input, so what the store executes is
// this text exactly: the generated shreddingdb package carries it per dialect,
// with the consumer's table prefix substituted once at construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	querygen.RegisterTable(TableNames...)

	return querygen.RenderFile([]*querygen.Query{
		readQuery(g),
		insertQuery(g),
		shredQuery(g),
	})
}

// readQuery is the single-subject read.
//
// It keys on the pair and on nothing else. There is no id predicate because
// there is no id, and no archived predicate because the shape list leaves the
// column out: a tombstone is the evidence a destruction happened, and a read
// that hid an archived row would report a shredded subject as one that never
// had a key.
func readQuery(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery("GetSubjectKey", SubjectKeysTable, RecordColumns, querygen.Read{}, keyMatches()...)
}

// insertQuery is the mint and the tombstone, which are one statement.
//
// Writing a row for a subject nothing was ever encrypted for is not a special
// case of the mint — it is the mint with no key material and a destruction time
// already stamped. The tombstone is what stops a key being issued for that
// subject afterwards, and erasure that only worked for subjects who happened to
// have data already is erasure that fails in exactly the case nobody tests.
//
// Skipping a row that is already there is what turns a race between two replicas
// into a count the caller can react to, rather than a dialect-specific constraint
// violation it would have to parse. Reacting matters here more than it usually
// does: the loser of that race has generated a key that must be thrown away.
func insertQuery(g *querygen.Generator) *querygen.Query {
	return g.InsertIgnoreQuery("InsertSubjectKey", SubjectKeysTable,
		RecordColumns, NullableColumns, keyMatches()...)
}

// shredQuery is the destruction: the row survives and the key material does not.
//
// The guard is the mechanism rather than a check. shredded_at IS NULL is what
// makes the operation idempotent without a read first — a second call matches
// nothing, and zero rows affected is how the caller learns the destruction was
// somebody else's — and it is why this is the one column in the schema that is
// both assigned and compared in the same statement. The comparison binds
// nothing, so the two ends need no second argument name: the guard is the
// statement's own and there is nothing a caller could pass that would relax it.
func shredQuery(g *querygen.Generator) *querygen.Query {
	matches := append(keyMatches(), querygen.Match{Column: ShreddedAtColumn, Against: querygen.NoValue})

	return g.UpdateQuery("ShredSubjectKey", SubjectKeysTable,
		RecordColumns, ShredColumns, NullableColumns, matches...)
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

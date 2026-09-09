package queries

import (
	"slices"

	"github.com/primandproper/primitives-go/database/querygen"
)

// Table is one webhooks table's shape.
//
// Columns is the full list, in the order the emitted SELECTs project it. The
// rest is what a column list cannot say — which of them a write may set to
// NULL, and which of them a converging write carries over onto the row it
// found.
type Table struct {
	// Name is the canonical, unprefixed table name.
	Name string

	// Columns is every column, in projection order.
	Columns []string
	// Nullable names the columns a write may set to NULL. It lives in the
	// schema, which neither this package nor querygen reads, so it is declared
	// rather than inferred: a column omitted here yields a parameter that
	// cannot express the NULL the column accepts, and one named here that is
	// NOT NULL yields a parameter that can express a NULL the server will
	// reject. Both are quiet.
	Nullable []string
	// Updatable names the columns a converging write assigns to the row it
	// found, and everything else the table carries is left as it was.
	//
	// It is stated positively for the reason identity states it positively:
	// getting it wrong is not a small thing. querygen assigns whatever it is
	// handed, and an endpoint's scope or its provenance joining that list would
	// let a re-registration move somebody else's subscriber into the caller's
	// tenant — which is precisely what the check in front of the upsert exists
	// to refuse.
	Updatable []string
}

// InsertColumns returns the columns a write supplies values for: everything but
// the ones the database owns.
//
// created_at is among those, which is why the schema gives every table here a
// DEFAULT for it. A caller-supplied creation time is how a row ends up with one
// that disagrees with its id, and the cursor walks here order by id.
//
// Two of this package's inserts are authored rather than rendered precisely
// because their creation column is not the database's to own — see the
// package comment.
func (t *Table) InsertColumns() []string {
	return querygen.ForInsert(t.Columns)
}

// ColumnsExcept returns the table's shape without the named columns, in
// projection order.
//
// It is how a statement says what it does not key on. querygen derives a
// single-row statement's id predicate from the column list it is handed, and
// its archived predicate the same way, so a read keyed on a natural key hands
// over a list without the id and a read that must return retired rows hands
// over one without archived_at. What comes back is a separate list, so leaving
// a column out here does not take it out of the answer.
func (t *Table) ColumnsExcept(excluded ...string) []string {
	kept := make([]string, 0, len(t.Columns))

	for _, column := range t.Columns {
		if !slices.Contains(excluded, column) {
			kept = append(kept, column)
		}
	}

	return kept
}

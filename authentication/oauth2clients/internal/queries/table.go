package queries

import (
	"slices"

	"github.com/primandproper/platform-go/v14/database/querygen"
)

// Table is the registered clients table's shape.
//
// Columns is the full list, in the order the emitted SELECTs project it, which
// is also the order this package's row conversions are written in. The rest is
// what a column list cannot say — see the package comment.
//
// It is deliberately the same shape comments/internal/queries declares. This
// table is keyed on more than the scope for its writes, on the scope alone for
// its listing reads, and on neither for the authorization server's lookup, so it
// takes no standard set and there is no Omitted to declare; what a statement
// keys on is written at the statement instead, which is what querygen.Match is
// for.
type Table struct {
	// Name is the canonical, unprefixed table name.
	Name string

	// Columns is every column, in projection order.
	Columns []string
	// Nullable names the columns a write may set to NULL.
	Nullable []string
}

// InsertColumns returns the columns the create supplies values for: everything
// but the database-owned ones.
//
// created_at is among those the database owns, which is why the schema gives it
// a DEFAULT — see authentication/oauth2clients/migrations. A caller-supplied
// creation time is how a row ends up with one that disagrees with its id, and
// the cursor walk orders by id while the filter window compares created_at.
func (t *Table) InsertColumns() []string {
	return querygen.ForInsert(t.Columns)
}

// ColumnsExcept returns the table's shape without the named columns, in
// projection order.
//
// It is how a statement says it keys on something other than the row's own id,
// or on nothing the column list carries: querygen renders the id predicate when
// the column list it is handed has an id and not when it does not, exactly as it
// renders the archived one. What a statement projects is a separate list, so
// leaving a column out here does not take it out of the answer.
func (t *Table) ColumnsExcept(excluded ...string) []string {
	kept := make([]string, 0, len(t.Columns))

	for _, column := range t.Columns {
		if !slices.Contains(excluded, column) {
			kept = append(kept, column)
		}
	}

	return kept
}

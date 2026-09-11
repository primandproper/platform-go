package queries

import (
	"slices"

	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// Table is the registered clients table's shape.
//
// Columns is the full list, in the order the emitted SELECTs project it, which
// is also the order this package's row conversions are written in. The rest is
// what a column list cannot say — see the package comment.
//
// The type is deliberately the same shape comments/internal/queries declares,
// and not for the same reason. comments takes no standard set at all; this table
// takes one, minus two members. The existence check is omitted because nothing
// here asks whether a registration exists without also wanting to read it, and
// the create because a unique index on client_id makes the insert an
// insert-ignore rather than the raising one the standard set emits — see
// [options], which is where both omissions are declared.
//
// What that set has no way to express is the other three reads, and those are
// written at the statement instead with querygen.Match: the create's read-back
// of the creation time the database assigned it, the self-service page keyed on
// the scope and the owner both, and the authorization server's lookup keyed on
// client_id and no scope at all. [Render] is where the generated set and the
// authored statements are appended to each other.
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

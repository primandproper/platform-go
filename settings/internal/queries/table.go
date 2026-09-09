package queries

import (
	"slices"

	"github.com/primandproper/primitives-go/database/querygen"
)

// Table is one settings table's shape.
//
// Columns is the full list, in the order the emitted SELECTs project it, which
// is also the order the store's conversions are written in. The rest is what a
// column list cannot say — see the package comment.
type Table struct {
	// Name is the canonical, unprefixed table name.
	Name string
	// Singular and Plural name the entity the emitted query names are built
	// from: GetDefinition rather than GetSettingsDefinitions.
	Singular string
	Plural   string

	// Columns is every column, in projection order.
	Columns []string
	// Nullable names the columns a write may set to NULL.
	Nullable []string
	// Updatable names the columns a write may assign after the insert: what the
	// standard update sets, and what an upsert's conflict branch carries over.
	// Everything else this table would otherwise let an update assign becomes
	// immutable to it — see Immutable.
	Updatable []string
	// Omitted names the standard queries this table has no caller for.
	Omitted []querygen.StandardQuery
}

// InsertColumns returns the columns the create supplies values for: everything
// but the database-owned ones.
//
// created_at is among those the database owns, which is the whole reason the
// schema gives it a DEFAULT — see settings/migrations. A caller-supplied
// creation time is how a row ends up with one that disagrees with its id, and
// the cursor walk orders by id while the filter window compares created_at.
func (t *Table) InsertColumns() []string {
	return querygen.ForInsert(t.Columns)
}

// Immutable returns the columns the standard update must not assign: everything
// assignable that Updatable does not name, plus the scope.
//
// Derived rather than declared, so that a column added to a table is immutable
// until somebody says otherwise. The other direction — a new column silently
// joining the update set — is the one with a failure mode, and it is the failure
// mode Updatable's doc describes.
func (t *Table) Immutable() []string {
	assignable := querygen.ForUpdate(t.Columns, ScopeColumn)

	immutable := make([]string, 0, len(assignable))
	for _, column := range assignable {
		if !slices.Contains(t.Updatable, column) {
			immutable = append(immutable, column)
		}
	}

	return immutable
}

// UpdateColumns returns the columns the standard update assigns, in projection
// order.
//
// Order matters because the generated code reads it: the canonical .sql assigns
// these columns in this order and the querier's parameter struct follows, so a
// list derived from the column list stays in step with the projection rather
// than being remembered alongside it.
func (t *Table) UpdateColumns() []string {
	return querygen.ForUpdate(t.Columns, append(t.Immutable(), ScopeColumn)...)
}

// KeyedColumns returns the table's shape as a read keyed on something other
// than the row's own id sees it: every column but the id.
//
// querygen derives a single-row statement's predicates from the column list it
// is handed — the id predicate is rendered when the list has an id, exactly as
// the archived one is — so leaving the id out is how a statement says it keys on
// something else. What it does not decide is what comes back: the projection is
// a separate list, and every keyed read below still returns the id where the
// caller needs it.
func (t *Table) KeyedColumns() []string {
	return t.ColumnsExcept(querygen.IDColumn)
}

// ColumnsExcept returns the table's shape without the named columns, in
// projection order.
//
// It is the general form of KeyedColumns, and it is the same trick: a predicate
// querygen renders from the column list is left off by handing over a list
// without the column it derives from. What a statement projects is a separate
// list, so leaving a column out here does not take it out of the answer.
func (t *Table) ColumnsExcept(excluded ...string) []string {
	kept := make([]string, 0, len(t.Columns))

	for _, column := range t.Columns {
		if !slices.Contains(excluded, column) {
			kept = append(kept, column)
		}
	}

	return kept
}

// Options renders this table's shape as the options StandardCRUD reads.
func (t *Table) Options() []querygen.Option {
	opts := []querygen.Option{
		querygen.WithEntity(t.Singular, t.Plural),
		querygen.WithOwnership(ScopeColumn),
		querygen.WithNullable(t.Nullable...),
		querygen.WithImmutable(t.Immutable()...),
	}

	if len(t.Omitted) > 0 {
		opts = append(opts, querygen.WithOmitted(t.Omitted...))
	}

	return opts
}

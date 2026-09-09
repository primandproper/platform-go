package queries

import (
	"slices"

	"github.com/primandproper/primitives-go/database/querygen"
)

// Table is one billing table's shape.
//
// Columns is the full list, in the order the emitted SELECTs project it, which
// is also the order this package's row conversions are written in. The rest is
// what a column list cannot say — see the package comment.
//
// It is deliberately the same shape waitlists/internal/queries declares. All
// four tables here take the standard set — this is the schema that shape was
// written for — and what differs between them is which standard queries they
// omit and how many keyed statements they carry beside it.
type Table struct {
	// Name is the canonical, unprefixed table name.
	Name string
	// Singular and Plural name the entity the emitted query names are built
	// from: GetList rather than GetWaitlists.
	Singular string
	Plural   string

	// Columns is every column, in projection order.
	Columns []string
	// Nullable names the columns a write may set to NULL.
	Nullable []string
	// Updatable names the columns a write may assign after the insert: what the
	// standard update sets, and what a keyed update carries over. Everything
	// else this table would otherwise let an update assign becomes immutable to
	// it — see Immutable.
	Updatable []string
	// Omitted names the standard queries this table has no caller for.
	Omitted []querygen.StandardQuery
}

// InsertColumns returns the columns the create supplies values for: everything
// but the database-owned ones.
//
// created_at is among those the database owns, which is the whole reason the
// schema gives it a DEFAULT — see billing/migrations. A caller-supplied
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

// UnarchivedBlindColumns returns the table's shape as this schema's four
// provider-id statements see it: every column but the id and the soft delete.
//
// querygen derives a single-row statement's predicates from the column list it
// is handed — the id predicate is rendered when the list has an id, exactly as
// the archived one is — so leaving a column out is how a statement says it does
// not key on it. Dropping the id is how these say they key on the provider's
// identifier instead; dropping archived_at is how they say they must see
// archived rows, because the unique indexes they stand in for cover archived
// rows too.
//
// Neither omission decides what comes back. The projection is a separate list,
// and every one of those statements still returns the whole row — including the
// id the caller needs and the archived_at the store reads to tell a collision
// from a hit.
func (t *Table) UnarchivedBlindColumns() []string {
	return t.ColumnsExcept(querygen.IDColumn, querygen.ArchivedAtColumn)
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
